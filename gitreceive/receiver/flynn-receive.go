package main

import (
	"archive/tar"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	api "github.com/flynn/flynn/controller/api"
	"github.com/flynn/flynn/controller/authz"
	controller "github.com/flynn/flynn/controller/client"
	"github.com/flynn/flynn/controller/tokensigner"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/host/resource"
	host "github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/flynn/pkg/dockerimage"
	"github.com/flynn/flynn/pkg/exec"
	"github.com/flynn/flynn/pkg/random"
	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/flynn/flynn/pkg/version"
	"github.com/flynn/go-docopt"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func init() {
	log.SetFlags(0)
}

const (
	stackHeroku24  = "heroku-24"
	stackContainer = "container"

	blobstoreURL = "http://blobstore.discoverd"

	// buildTokenPath is where the minted, app-scoped build token is mounted
	// (root-only, read-only) inside slug/dockerbuilder jobs. create-artifact
	// reads it and authenticates to the controller/tarreceive with a bearer
	// token instead of the cluster-wide CONTROLLER_KEY.
	buildTokenPath = "/run/secrets/controller_token"

	// defaultBuildTimeout is the build timeout used when no override is
	// configured. It bounds how long a single build may run and also the
	// lifetime of the minted build token (a token only needs to outlive the
	// build that uses it).
	defaultBuildTimeout = 15 * time.Minute

	// maxBuildTimeout is the cluster-wide ceiling for a build. An operator
	// override (FLYNN_BUILD_TIMEOUT) is clamped to this. Because the token TTL
	// is derived from the same value, the authorizer's ACCESS_TOKEN_MAX_VALIDITY
	// applies independently on top, so a misconfigured or malicious override can
	// never produce a longer-lived token than policy allows.
	maxBuildTimeout = 30 * time.Minute

	// buildTimeoutEnv lets an operator adjust the build timeout per app
	// (e.g. `flynn -a gitreceive env set FLYNN_BUILD_TIMEOUT=25m` for a
	// cluster-wide default, or on the app being built). The value is a Go
	// duration string; it is clamped to maxBuildTimeout. This single knob drives
	// both the build kill-timeout and the minted build-token TTL.
	buildTimeoutEnv = "FLYNN_BUILD_TIMEOUT"
)

// resolveBuildTimeout determines the build timeout, preferring the
// FLYNN_BUILD_TIMEOUT override from the app's release env, then the receiver's
// own process env (a gitreceive app-level default), then the built-in default.
// The result is always clamped to (0, maxBuildTimeout]. The same value bounds
// build execution and the minted token's TTL.
func resolveBuildTimeout(releaseEnv map[string]string) time.Duration {
	raw := releaseEnv[buildTimeoutEnv]
	if raw == "" {
		raw = os.Getenv(buildTimeoutEnv)
	}
	timeout := defaultBuildTimeout
	if raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			timeout = d
		} else {
			fmt.Printf("-----> WARN: ignoring invalid %s=%q, using default %s\n", buildTimeoutEnv, raw, defaultBuildTimeout)
		}
	}
	if timeout > maxBuildTimeout {
		timeout = maxBuildTimeout
	}
	return timeout
}

// buildSigner returns the token signer configured via ACCESS_TOKEN_SIGNING_KEY,
// or nil (no error) when no signing key is set, in which case the receiver
// falls back to the legacy CONTROLLER_KEY behavior so existing clusters keep
// working until re-bootstrapped.
func buildSigner() (*tokensigner.Signer, error) {
	return tokensigner.ParseSigningKey(os.Getenv("ACCESS_TOKEN_SIGNING_KEY"))
}

// mintBuildToken signs a short-lived AccessToken scoped to a single app and the
// build:artifacts action. The token can create this build's artifact and write
// only to this app; it is worthless for anything else and expires quickly, so a
// buildpack/Dockerfile step that exfiltrates it cannot escalate to the cluster.
func mintBuildToken(signer *tokensigner.Signer, app *ct.App, ttl time.Duration) (string, error) {
	now := time.Now()
	return signer.Sign(&api.AccessToken{
		UserEmail:  "build:" + app.Name,
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(ttl)),
		Scopes:     []string{authz.ScopeBuildArtifacts},
		AppGrants: []*api.AppGrant{
			{AppId: app.ID, Permissions: []string{"app:write"}},
		},
	})
}

// applyBuildCredential delivers the build credential to a job. When a signing
// key is configured it mints an app-scoped token, delivers it via a root-only
// secret mount (never the env), and removes CONTROLLER_KEY from jobEnv so
// attacker-controlled build steps can no longer read the cluster key. Otherwise
// it leaves the legacy CONTROLLER_KEY env in place.
func applyBuildCredential(signer *tokensigner.Signer, job *host.Job, app *ct.App, releaseEnv map[string]string) error {
	if signer == nil {
		return nil
	}
	token, err := mintBuildToken(signer, app, resolveBuildTimeout(releaseEnv))
	if err != nil {
		return err
	}
	delete(job.Config.Env, "CONTROLLER_KEY")
	job.Config.Secrets = append(job.Config.Secrets, host.ContainerSecret{
		Path: buildTokenPath,
		Data: []byte(token),
	})
	return nil
}

// SEC-013: signedBuildCacheURL generates a build cache URL with an HMAC
// token scoped to the app ID, preventing one app's build from accessing
// another app's cache.
func signedBuildCacheURL(appID, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(appID))
	token := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%s/%s-cache.tgz?token=%s", blobstoreURL, appID, token)
}

func parsePairs(args *docopt.Args, str string) (map[string]string, error) {
	pairs := args.All[str].([]string)
	item := make(map[string]string, len(pairs))
	for _, s := range pairs {
		v := strings.SplitN(s, "=", 2)
		if len(v) != 2 {
			return nil, fmt.Errorf("invalid var format: %q", s)
		}
		item[v[0]] = v[1]
	}
	return item, nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalln("ERROR:", err)
	}
}

func run() error {
	client, err := controller.NewClient("", os.Getenv("CONTROLLER_KEY"))
	if err != nil {
		return fmt.Errorf("Unable to connect to controller: %s", err)
	}

	usage := `
Usage: flynn-receiver <app> <rev> [-e <var>=<val>]... [-m <key>=<val>]...

Options:
	-e,--env <var>=<val>
	-m,--meta <key>=<val>
`[1:]
	args, _ := docopt.Parse(usage, nil, true, version.String(), false)

	appName := args.String["<app>"]
	env, err := parsePairs(args, "--env")
	if err != nil {
		return err
	}
	meta, err := parsePairs(args, "--meta")
	if err != nil {
		return err
	}

	app, err := client.GetApp(appName)
	if err == controller.ErrNotFound {
		return fmt.Errorf("Unknown app %q", appName)
	} else if err != nil {
		return fmt.Errorf("Error retrieving app: %s", err)
	}
	prevRelease, err := client.GetAppRelease(app.Name)
	if err == controller.ErrNotFound {
		prevRelease = &ct.Release{}
	} else if err != nil {
		return fmt.Errorf("Error getting current app release: %s", err)
	}

	releaseEnv := make(map[string]string, len(env))
	if prevRelease.Env != nil {
		for k, v := range prevRelease.Env {
			releaseEnv[k] = v
		}
	}
	for k, v := range env {
		releaseEnv[k] = v
	}

	stack, err := resolveStack(releaseEnv)
	if err != nil {
		return err
	}

	switch stack {
	case stackContainer:
		return deployContainer(client, app, prevRelease, args, releaseEnv, meta)
	default:
		return deployBuildpack(client, app, prevRelease, args, releaseEnv, meta, stack)
	}
}

// resolveStack returns the build stack for a release, defaulting to heroku-24
// when FLYNN_STACK is unset. It errors on unknown stack values.
func resolveStack(releaseEnv map[string]string) (string, error) {
	stack := stackHeroku24
	if s := releaseEnv["FLYNN_STACK"]; s != "" {
		stack = s
	}
	switch stack {
	case stackHeroku24, stackContainer:
		return stack, nil
	default:
		return "", fmt.Errorf("Unknown FLYNN_STACK: %q", stack)
	}
}

func deployBuildpack(client controller.Client, app *ct.App, prevRelease *ct.Release, args *docopt.Args, releaseEnv map[string]string, meta map[string]string, stackName string) error {
	slugbuilderImageID := os.Getenv("SLUGBUILDER_24_IMAGE_ID")
	slugrunnerImageID := os.Getenv("SLUGRUNNER_24_IMAGE_ID")
	cfStackName := "cflinuxfs4"

	slugBuilder, err := client.GetArtifact(slugbuilderImageID)
	if err != nil {
		return fmt.Errorf("Error getting slugbuilder image: %s", err)
	}

	slugRunnerID := slugrunnerImageID
	if _, err := client.GetArtifact(slugRunnerID); err != nil {
		return fmt.Errorf("Error getting slugrunner image: %s", err)
	}

	fmt.Printf("-----> Building %s...\n", app.Name)

	slugImageID := random.UUID()
	jobEnv := map[string]string{
		"BUILD_CACHE_URL": signedBuildCacheURL(app.ID, os.Getenv("CONTROLLER_KEY")),
		"CONTROLLER_KEY":  os.Getenv("CONTROLLER_KEY"),
		"SLUG_IMAGE_ID":   slugImageID,
		"SOURCE_VERSION":  args.String["<rev>"],
		"STACK":           stackName,
		"CF_STACK":        cfStackName,
	}
	if buildpackURL, ok := releaseEnv["BUILDPACK_URL"]; ok {
		jobEnv["BUILDPACK_URL"] = buildpackURL
	}
	for _, k := range []string{"SSH_CLIENT_KEY", "SSH_CLIENT_HOSTS"} {
		if v := os.Getenv(k); v != "" {
			jobEnv[k] = v
		}
	}

	job := buildJob(slugBuilder, app, prevRelease, jobEnv, "slugbuilder", "/builder/build.sh")
	if sb, ok := prevRelease.Processes["slugbuilder"]; ok {
		job.Resources = sb.Resources
	} else if rawLimit := os.Getenv("SLUGBUILDER_DEFAULT_MEMORY_LIMIT"); rawLimit != "" {
		if limit, err := resource.ParseLimit(resource.TypeMemory, rawLimit); err == nil {
			job.Resources[resource.TypeMemory] = resource.Spec{Limit: &limit, Request: &limit}
		}
	}

	signer, err := buildSigner()
	if err != nil {
		return fmt.Errorf("error loading build signing key: %s", err)
	}
	if err := applyBuildCredential(signer, job, app, releaseEnv); err != nil {
		return fmt.Errorf("error minting build token: %s", err)
	}

	cmd := buildJobCmd(slugBuilder, job)
	cmd.Volumes = []*ct.VolumeReq{{Path: "/tmp", DeleteOnStop: true}}
	var output bytes.Buffer
	out := syncStdout(io.MultiWriter(os.Stdout, &output))
	cmd.Stdout = out
	cmd.Stderr = out

	if err := runBuildCmd(cmd, releaseEnv); err != nil {
		return fmt.Errorf("Build failed: %s", err)
	}

	artifact, err := client.GetArtifact(slugImageID)
	if err != nil {
		return fmt.Errorf("Error getting slug image: %s", err)
	}
	var processTypes []string
	if metaVal, ok := artifact.Meta["slugbuilder.process_types"]; ok {
		processTypes = strings.Split(metaVal, ",")
	}

	fmt.Printf("-----> Creating release...\n")

	release := &ct.Release{
		ArtifactIDs: []string{slugRunnerID, slugImageID},
		Env:         releaseEnv,
		Meta:        prevRelease.Meta,
	}
	if release.Meta == nil {
		release.Meta = make(map[string]string, len(meta))
	}
	for k, v := range meta {
		release.Meta[k] = v
	}
	release.Meta["slugrunner.stack"] = stackName

	procs := make(map[string]ct.ProcessType)
	for _, t := range processTypes {
		proc := prevRelease.Processes[t]
		proc.Args = []string{"/runner/init", "start", t}
		if (t == "web" || strings.HasSuffix(t, "-web")) && proc.Service == "" {
			proc.Service = app.Name + "-" + t
			proc.Ports = []ct.Port{{
				Port:  8080,
				Proto: "tcp",
				Service: &host.Service{
					Name:   proc.Service,
					Create: true,
					Check:  &host.HealthCheck{Type: "tcp"},
				},
			}}
		}
		procs[t] = proc
	}
	if sb, ok := prevRelease.Processes["slugbuilder"]; ok {
		procs["slugbuilder"] = sb
	}
	release.Processes = procs

	return finishDeploy(client, app, prevRelease, release, procs)
}

func deployContainer(client controller.Client, app *ct.App, prevRelease *ct.Release, args *docopt.Args, releaseEnv map[string]string, meta map[string]string) error {
	dockerbuilderImageID := os.Getenv("DOCKERBUILDER_24_IMAGE_ID")
	if dockerbuilderImageID == "" {
		return fmt.Errorf("DOCKERBUILDER_24_IMAGE_ID not configured on gitreceive")
	}

	dockerBuilder, err := client.GetArtifact(dockerbuilderImageID)
	if err != nil {
		return fmt.Errorf("Error getting dockerbuilder image: %s", err)
	}

	fmt.Printf("-----> Building %s from Dockerfile...\n", app.Name)

	imageArtifactID := random.UUID()
	jobEnv := dockerBuildJobEnv(os.Getenv("CONTROLLER_KEY"), imageArtifactID, args.String["<rev>"], releaseEnv)

	// Build-job capabilities (CAP_SYS_ADMIN/NET_ADMIN/NET_RAW etc.) are applied
	// centrally by flynn-host for dockerbuilder jobs; see isBuildJob in
	// host/libcontainer_backend.go.
	job := buildJob(dockerBuilder, app, prevRelease, jobEnv, "dockerbuilder", "/builder/build.sh")
	if limit, err := resource.ParseLimit(resource.TypeTempDisk, "2G"); err == nil {
		job.Resources[resource.TypeTempDisk] = resource.Spec{Limit: &limit, Request: &limit}
	}
	if db, ok := prevRelease.Processes["dockerbuilder"]; ok {
		job.Resources = db.Resources
	} else if rawLimit := os.Getenv("DOCKERBUILDER_DEFAULT_MEMORY_LIMIT"); rawLimit != "" {
		if limit, err := resource.ParseLimit(resource.TypeMemory, rawLimit); err == nil {
			job.Resources[resource.TypeMemory] = resource.Spec{Limit: &limit, Request: &limit}
		}
	}

	signer, err := buildSigner()
	if err != nil {
		return fmt.Errorf("error loading build signing key: %s", err)
	}
	if err := applyBuildCredential(signer, job, app, releaseEnv); err != nil {
		return fmt.Errorf("error minting build token: %s", err)
	}

	cmd := buildJobCmd(dockerBuilder, job)
	cmd.Volumes = []*ct.VolumeReq{{Path: "/tmp", DeleteOnStop: true}}
	// Merge stderr into stdout so build output goes through the git hook pipe
	// (pre-receive only pipes flynn-receiver stdout to the client).
	out := syncStdout(os.Stdout)
	cmd.Stdout = out
	cmd.Stderr = out

	if err := runBuildCmd(cmd, releaseEnv); err != nil {
		return fmt.Errorf("Build failed: %s", err)
	}

	artifact, err := client.GetArtifact(imageArtifactID)
	if err != nil {
		return fmt.Errorf("Error getting image artifact: %s", err)
	}

	fmt.Printf("-----> Creating release...\n")

	releaseMeta := prevRelease.Meta
	if releaseMeta == nil {
		releaseMeta = make(map[string]string, len(meta))
	}
	for k, v := range meta {
		releaseMeta[k] = v
	}
	releaseMeta["git"] = "true"
	releaseMeta["slugrunner.stack"] = stackContainer

	extra := map[string]ct.ProcessType{}
	if db, ok := prevRelease.Processes["dockerbuilder"]; ok {
		extra["dockerbuilder"] = db
	}

	release := dockerimage.NewAppReleaseFromArtifact(app.Name, prevRelease, artifact, dockerimage.ReleaseOptions{
		Env:                releaseEnv,
		Meta:               releaseMeta,
		ServiceHealthCheck: true,
		ExtraProcesses:     extra,
	})

	procs := release.Processes
	return finishDeploy(client, app, prevRelease, release, procs)
}

// buildJobCmd schedules a build job on the same host as gitreceive so attach
// streams stay local and are less likely to drop mid-build.
func buildJobCmd(artifact *ct.Artifact, job *host.Job) *exec.Cmd {
	if hostID := localHostID(); hostID != "" {
		if h, err := cluster.NewClient().Host(hostID); err == nil {
			return exec.JobUsingHost(h, artifact, job)
		}
	}
	return exec.Job(artifact, job)
}

func localHostID() string {
	jobID := os.Getenv("FLYNN_JOB_ID")
	if i := strings.IndexByte(jobID, '-'); i > 0 {
		return jobID[:i]
	}
	return ""
}

// dockerBuildJobEnv builds the environment for a dockerbuilder job. The
// DOCKERFILE override is copied from the release env when present.
func dockerBuildJobEnv(controllerKey, imageArtifactID, sourceVersion string, releaseEnv map[string]string) map[string]string {
	jobEnv := map[string]string{
		"CONTROLLER_KEY":    controllerKey,
		"IMAGE_ARTIFACT_ID": imageArtifactID,
		"SOURCE_VERSION":    sourceVersion,
		"BUILDKITD_FLAGS":   "--root=/tmp/buildkitd --oci-worker-snapshotter=native",
		"CI":                "true",
		"BUILDKIT_PROGRESS": "plain",
	}
	if dockerfile, ok := releaseEnv["DOCKERFILE"]; ok {
		jobEnv["DOCKERFILE"] = dockerfile
	}
	return jobEnv
}

func buildJob(artifact *ct.Artifact, app *ct.App, prevRelease *ct.Release, jobEnv map[string]string, jobType, script string) *host.Job {
	return &host.Job{
		Config: host.ContainerConfig{
			Args:       []string{script},
			Env:        jobEnv,
			Stdin:      true,
			DisableLog: true,
		},
		Partition: "background",
		Metadata: map[string]string{
			"flynn-controller.app":      app.ID,
			"flynn-controller.app_name": app.Name,
			"flynn-controller.release":  prevRelease.ID,
			"flynn-controller.type":     jobType,
		},
		Resources: resource.Defaults(),
	}
}

func runBuildCmd(cmd *exec.Cmd, releaseEnv map[string]string) error {
	if len(releaseEnv) > 0 {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return err
		}
		go func() {
			if err := appendEnvDir(os.Stdin, stdin, releaseEnv); err != nil {
				log.Fatalln("ERROR:", err)
			}
		}()
	} else {
		cmd.Stdin = os.Stdin
	}

	shutdown.BeforeExit(func() { cmd.Kill() })

	timeout := resolveBuildTimeout(releaseEnv)
	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		cmd.Kill()
		<-done
		return fmt.Errorf("build exceeded timeout of %s (set %s to change, cluster max %s)", timeout, buildTimeoutEnv, maxBuildTimeout)
	}
}

func finishDeploy(client controller.Client, app *ct.App, prevRelease *ct.Release, release *ct.Release, procs map[string]ct.ProcessType) error {
	if err := client.CreateRelease(app.ID, release); err != nil {
		return fmt.Errorf("Error creating release: %s", err)
	}
	if err := client.DeployAppRelease(app.ID, release.ID, nil); err != nil {
		return fmt.Errorf("Error deploying app release: %s", err)
	}

	if needsDefaultScale(app.ID, prevRelease.ID, procs, client) {
		fmt.Println("=====> Scaling initial release to web=1")

		timeout := time.Duration(app.DeployTimeout) * time.Second
		opts := ct.ScaleOptions{
			Processes: map[string]int{"web": 1},
			Timeout:   &timeout,
			JobEventCallback: func(job *ct.Job) error {
				switch job.State {
				case ct.JobStateUp:
					fmt.Println("=====> Initial web job started")
				case ct.JobStateDown:
					return errors.New("Initial web job failed to start")
				}
				return nil
			},
		}
		fmt.Println("-----> Waiting for initial web job to start...")
		if err := client.ScaleAppRelease(app.ID, release.ID, opts); err != nil {
			fmt.Println("-----> WARN: scaling initial release down to web=0 due to error")
			if err := client.DeleteFormation(app.ID, release.ID); err != nil {
				fmt.Println("-----> WARN: could not scale the initial release down (it may continue to run):", err)
			}
			return err
		}
	}

	fmt.Println("=====> Application deployed")
	return nil
}

// needsDefaultScale indicates whether a release needs a default scale based on
// whether it has a web process type and either has no previous release or no
// previous scale.
func needsDefaultScale(appID, prevReleaseID string, procs map[string]ct.ProcessType, client controller.Client) bool {
	if _, ok := procs["web"]; !ok {
		return false
	}
	if prevReleaseID == "" {
		return true
	}
	_, err := client.GetFormation(appID, prevReleaseID)
	return err == controller.ErrNotFound
}

func appendEnvDir(stdin io.Reader, pipe io.WriteCloser, env map[string]string) error {
	defer pipe.Close()
	tr := tar.NewReader(stdin)
	tw := tar.NewWriter(pipe)
	defer tw.Close()
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
	for key, value := range env {
		hdr := &tar.Header{
			Name:    path.Join(".ENV_DIR_bdca46b87df0537eaefe79bb632d37709ff1df18", key),
			Mode:    0644,
			ModTime: time.Now(),
			Size:    int64(len(value)),
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(value)); err != nil {
			return err
		}
	}
	return nil
}

// syncStdout wraps stdout so each write is flushed when attached to a pipe
// (e.g. git pre-receive), giving live build output during git push.
func syncStdout(w io.Writer) io.Writer {
	if f, ok := w.(*os.File); ok {
		return &flushWriter{f: f}
	}
	return w
}

type flushWriter struct {
	mu sync.Mutex
	f  *os.File
}

func (w *flushWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.f.Write(p)
	if err == nil {
		_ = w.f.Sync()
	}
	return n, err
}
