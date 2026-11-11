package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/buildadmit"
	"github.com/randy-girard/flynn/controller/buildtoken"
	controller "github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/controller/tokensigner"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/dockerimage"
	"github.com/randy-girard/flynn/pkg/random"
	"github.com/randy-girard/flynn/pkg/shutdown"
	"github.com/randy-girard/flynn/pkg/version"
	router "github.com/randy-girard/flynn/router/types"
)

func init() {
	log.SetFlags(0)
}

const qemuTranslationNotice = "-----> Notice: qemu-user is translating x86_64 Heroku buildpack/Node binaries on this arm64 host (slower than native amd64)\n"

func printQemuTranslationNotice(w io.Writer) {
	if runtime.GOARCH != "arm64" {
		return
	}
	fmt.Fprint(w, qemuTranslationNotice)
}

type appBuildingStore interface {
	GetApp(string) (*ct.App, error)
	UpdateAppMeta(*ct.App) error
}

// watchGitHangup ends the receive process when the git client's session
// drops. sshd delivers SIGHUP; the default action skips defers and would
// leave the app marked building until the meta TTL.
func watchGitHangup() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		<-ch
		fmt.Println("-----> Build cancelled")
		shutdown.Exit()
	}()
}

// metaAppBuildFailed is set when a git push ends without a deploy and cleared
// when the next push starts or succeeds. The pipeline card reads it because
// slugbuilder jobs started on the host are not stored in the controller.
const metaAppBuildFailed = "flynn-controller.build-failed"

func markAppBuilding(store appBuildingStore, app *ct.App) error {
	if store == nil || app == nil {
		return nil
	}
	app.Meta = ct.MarkAppBuildingMeta(app.Meta)
	delete(app.Meta, metaAppBuildFailed)
	return store.UpdateAppMeta(app)
}

func clearAppBuilding(store appBuildingStore, app *ct.App) error {
	return finishAppBuild(store, app, false)
}

func finishAppBuild(store appBuildingStore, app *ct.App, failed bool) error {
	if store == nil || app == nil || app.ID == "" {
		return nil
	}
	current, err := store.GetApp(app.ID)
	if err != nil || current == nil {
		current = app
	}
	if current.Meta == nil {
		current.Meta = map[string]string{}
	}
	delete(current.Meta, ct.MetaAppBuilding)
	if failed {
		if strings.TrimSpace(current.Meta[metaAppBuildFailed]) == "" {
			current.Meta[metaAppBuildFailed] = time.Now().UTC().Format(time.RFC3339)
		}
	} else {
		delete(current.Meta, metaAppBuildFailed)
	}
	return store.UpdateAppMeta(current)
}

const (
	stackHeroku24  = "heroku-24"
	stackContainer = "container"

	blobstoreURL = "http://blobstore.discoverd"

	// buildTokenPath is where the minted, app-scoped build token is mounted
	// (root-only, read-only) inside slug/dockerbuilder jobs. create-artifact
	// reads it and authenticates to the controller/tarreceive with a bearer
	// token instead of the cluster-wide CONTROLLER_KEY.
	buildTokenPath = buildadmit.TokenPath

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
	maxBuildTimeout = buildadmit.MaxPendingWait

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
// or nil (no error) when scoped build tokens are not fully configured.
// applyBuildCredential then fails the build; CONTROLLER_KEY is never copied
// into slugbuilder/dockerbuilder env. Both the signing key and the matching
// public verify key (ACCESS_TOKEN_KEY, which controller and tarreceive need)
// must be present and form a valid keypair before minting tokens; a mismatched
// pair (e.g. after a partial cluster update) would otherwise produce bearer
// tokens the controller rejects with 401.
func buildSigner() (*tokensigner.Signer, error) {
	publicKey := os.Getenv("ACCESS_TOKEN_KEY")
	if publicKey == "" {
		return nil, nil
	}
	signer, err := tokensigner.ParseSigningKey(os.Getenv("ACCESS_TOKEN_SIGNING_KEY"))
	if err != nil || signer == nil {
		return nil, err
	}
	if !tokensigner.KeyPairValid(publicKey, os.Getenv("ACCESS_TOKEN_SIGNING_KEY")) {
		return nil, nil
	}
	return signer, nil
}

// buildTokenUserID is the controller rate-limit account for a build. A user
// owner is stored without the user: prefix, matching dashboard JWTs. An org
// owner keeps org: so the bucket cannot collide with a user id. Anything else
// non-empty is kept whole. With no owner, CreatedBy is the fallback.
func buildTokenUserID(app *ct.App) string {
	return buildtoken.UserID(app)
}

// mintBuildToken signs a short-lived AccessToken scoped to a single app and the
// build:artifacts action. The controller calls the same helper when a compile
// is about to start. gitreceive keeps this wrapper for the credential tests.
func mintBuildToken(signer *tokensigner.Signer, app *ct.App, ttl time.Duration) (string, error) {
	return buildtoken.Mint(signer, app, ttl)
}

// applyBuildCredential delivers the build credential to a job. It mints an
// app-scoped token and mounts it at a root-only secret path, then removes
// CONTROLLER_KEY from the job env if a caller still set it. The scoped token
// authenticates to the controller, tarreceive, and blobstore slug/tarreceive
// paths. A missing signing key fails the build; cluster-admin CONTROLLER_KEY
// is never left in build job environment.
func applyBuildCredential(signer *tokensigner.Signer, job *host.Job, app *ct.App, releaseEnv map[string]string) error {
	if job != nil && job.Config.Env != nil {
		delete(job.Config.Env, "CONTROLLER_KEY")
	}
	if signer == nil {
		return fmt.Errorf("ACCESS_TOKEN_SIGNING_KEY is required to mint a scoped build token; CONTROLLER_KEY is no longer placed in build job environment")
	}
	token, err := mintBuildToken(signer, app, resolveBuildTimeout(releaseEnv))
	if err != nil {
		return err
	}
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
	return signedNamedCacheURL(appID, key, "cache")
}

func signedDockerBuildCacheURL(appID, key string) string {
	return signedNamedCacheURL(appID, key, "docker-cache")
}

func signedNamedCacheURL(appID, key, name string) string {
	token := blobstoreauth.CacheToken(appID, key)
	return fmt.Sprintf("%s/%s-%s.tgz?token=%s", blobstoreURL, appID, name, token)
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
	// os.Exit from a signal skips defers. BeforeExit still runs, including when
	// the git client hangs up (Ctrl+C), so the dashboard drops "compiling".
	// A push that does not deploy leaves a failed timestamp for the card.
	buildOK := false
	finishBuild := func() {
		if err := finishAppBuild(client, app, !buildOK); err != nil {
			fmt.Printf("-----> WARN: could not update build status: %s\n", err)
		}
	}
	shutdown.BeforeExit(finishBuild)
	watchGitHangup()
	defer finishBuild()
	if err := markAppBuilding(client, app); err != nil {
		fmt.Printf("-----> WARN: could not mark app as building: %s\n", err)
	}
	progress := openAppProgressLog(app.ID, os.Stdout)
	defer progress.Close()
	prevOut := receiveOut
	receiveOut = progress
	defer func() { receiveOut = prevOut }()
	prevRelease, err := controller.HeadRelease(client, app.Name)
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

	buildInput = nil
	skipDefaultScale = false
	afterDeploy = nil
	if err := prepareReceiveManifest(client, app, releaseEnv); err != nil {
		cleanupBuildInput()
		return err
	}
	defer cleanupBuildInput()
	if skipDefaultScale || afterDeploy != nil {
		// prepareReceiveManifest may have written a release. Rebuild the env
		// the slug will copy so add-on URLs and generated secrets are present,
		// then re-apply -e from this push.
		if head, err := controller.HeadRelease(client, app.ID); err == nil && head != nil {
			prevRelease = head
			releaseEnv = map[string]string{}
			for k, v := range head.Env {
				releaseEnv[k] = v
			}
			for k, v := range env {
				releaseEnv[k] = v
			}
		}
	}

	stack, err := resolveStack(releaseEnv)
	if err != nil {
		return err
	}

	var deployErr error
	switch stack {
	case stackContainer:
		deployErr = deployContainer(client, app, prevRelease, args, releaseEnv, meta)
	default:
		deployErr = deployBuildpack(client, app, prevRelease, args, releaseEnv, meta, stack)
	}
	if deployErr == nil {
		buildOK = true
	}
	return deployErr
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

type clusterPolicyStore interface {
	GetClusterPolicy() (*ct.ClusterPolicy, error)
}

// allowedBuildpacks returns the cluster policy's buildpack allowlist as the
// space-separated FLYNN_ALLOWED_BUILDPACKS value for slugbuilder. It is empty
// (no restriction) for system apps or when the policy is not enforced.
func allowedBuildpacks(store clusterPolicyStore, app *ct.App) (string, error) {
	if app == nil || app.System() {
		return "", nil
	}
	pol, err := store.GetClusterPolicy()
	if err != nil {
		return "", err
	}
	if !pol.Enforced {
		return "", nil
	}
	return strings.Join(pol.AllowedBuildpacks, " "), nil
}

// processCommandsFromMeta reads Procfile commands stored on the slug artifact.
// Older slugs only have process type names; those get empty command strings.
func processCommandsFromMeta(meta map[string]string) map[string]string {
	cmds := map[string]string{}
	if meta == nil {
		return cmds
	}
	if raw := strings.TrimSpace(meta["slugbuilder.process_commands"]); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cmds); err != nil {
			cmds = map[string]string{}
		}
	}
	if cmds == nil {
		cmds = map[string]string{}
	}
	if types := meta["slugbuilder.process_types"]; types != "" {
		for _, t := range strings.Split(types, ",") {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if _, ok := cmds[t]; !ok {
				cmds[t] = ""
			}
		}
	}
	return cmds
}

// slugReleaseProcesses copies existing process types from the previous release
// and stamps first-seen types with the small runtime.
func slugReleaseProcesses(commands map[string]string, prev *ct.Release, appName string) map[string]ct.ProcessType {
	var prevProcs map[string]ct.ProcessType
	if prev != nil {
		prevProcs = prev.Processes
	}
	procs := make(map[string]ct.ProcessType)
	for t, cmd := range commands {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		proc, existed := prevProcs[t]
		proc.Args = []string{"/runner/init", "start", t}
		if c := strings.TrimSpace(cmd); c != "" {
			proc.Command = c
		}
		if !existed {
			proc.RuntimeProfile = resource.ProfileSmall
		}
		if (t == "web" || strings.HasSuffix(t, "-web")) && proc.Service == "" {
			proc.Service = appName + "-" + t
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
	if sb, ok := prevProcs["slugbuilder"]; ok {
		procs["slugbuilder"] = sb
	}
	return procs
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
	printQemuTranslationNotice(os.Stdout)

	slugImageID := random.UUID()
	jobEnv := map[string]string{
		"BUILD_CACHE_URL": signedBuildCacheURL(app.ID, os.Getenv("CONTROLLER_KEY")),
		"SLUG_IMAGE_ID":   slugImageID,
		"SOURCE_VERSION":  args.String["<rev>"],
		"STACK":           stackName,
		"CF_STACK":        cfStackName,
	}
	if buildpackURL, ok := releaseEnv["BUILDPACK_URL"]; ok {
		jobEnv["BUILDPACK_URL"] = buildpackURL
	}
	allowed, err := allowedBuildpacks(client, app)
	if err != nil {
		return fmt.Errorf("Error getting cluster policy: %s", err)
	}
	if allowed != "" {
		jobEnv["FLYNN_ALLOWED_BUILDPACKS"] = allowed
	}
	for _, k := range []string{"SSH_CLIENT_KEY", "SSH_CLIENT_HOSTS"} {
		if v := os.Getenv(k); v != "" {
			jobEnv[k] = v
		}
	}

	resources := resource.Defaults()
	if sb, ok := prevRelease.Processes["slugbuilder"]; ok && sb.Resources != nil {
		resources = sb.Resources
	} else if rawLimit := os.Getenv("SLUGBUILDER_DEFAULT_MEMORY_LIMIT"); rawLimit != "" {
		if limit, err := resource.ParseLimit(resource.TypeMemory, rawLimit); err == nil {
			resources[resource.TypeMemory] = resource.Spec{Limit: &limit, Request: &limit}
		}
	}

	gitOut := newGitPushFilter(syncStdout(os.Stdout))
	placeholderID, err := runScheduledBuild(client, app, prevRelease, slugBuilder, jobEnv, "slugbuilder", []string{"/builder/build.sh"}, resources, releaseEnv, gitOut, gitOut)
	if err != nil {
		gitOut.flush()
		return fmt.Errorf("Build failed: %s", err)
	}
	gitOut.flush()

	artifact, err := client.GetArtifact(slugImageID)
	if err != nil {
		return fmt.Errorf("Error getting slug image: %s", err)
	}
	receivePrintf("-----> Creating release...\n")

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

	procs := slugReleaseProcesses(processCommandsFromMeta(artifact.Meta), prevRelease, app.Name)
	release.Processes = procs

	return finishDeploy(client, app, prevRelease, release, procs, placeholderID)
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
	printQemuTranslationNotice(os.Stdout)

	imageArtifactID := random.UUID()
	jobEnv := dockerBuildJobEnv(os.Getenv("CONTROLLER_KEY"), imageArtifactID, args.String["<rev>"], releaseEnv, app.ID)

	// Build-job capabilities (CAP_SYS_ADMIN/NET_ADMIN/NET_RAW etc.) are applied
	// centrally by flynn-host for dockerbuilder jobs; see isBuildJob in
	// host/libcontainer_backend.go.
	resources := resource.Defaults()
	if limit, err := resource.ParseLimit(resource.TypeTempDisk, "4G"); err == nil {
		resources[resource.TypeTempDisk] = resource.Spec{Limit: &limit, Request: &limit}
	}
	if db, ok := prevRelease.Processes["dockerbuilder"]; ok && db.Resources != nil {
		resources = db.Resources
	} else if rawLimit := os.Getenv("DOCKERBUILDER_DEFAULT_MEMORY_LIMIT"); rawLimit != "" {
		if limit, err := resource.ParseLimit(resource.TypeMemory, rawLimit); err == nil {
			resources[resource.TypeMemory] = resource.Spec{Limit: &limit, Request: &limit}
		}
	}

	// Build-job capabilities are applied by flynn-host for dockerbuilder jobs.
	// Merge stderr into stdout so build output goes through the git hook pipe.
	gitOut := newGitPushFilter(syncStdout(os.Stdout))
	placeholderID, err := runScheduledBuild(client, app, prevRelease, dockerBuilder, jobEnv, "dockerbuilder", []string{"/builder/build.sh"}, resources, releaseEnv, gitOut, gitOut)
	if err != nil {
		gitOut.flush()
		return fmt.Errorf("Build failed: %s", err)
	}
	gitOut.flush()

	artifact, err := client.GetArtifact(imageArtifactID)
	if err != nil {
		return fmt.Errorf("Error getting image artifact: %s", err)
	}

	receivePrintf("-----> Creating release...\n")

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
		ProcessName:        dockerimage.GitProcessName(prevRelease),
		ServiceHealthCheck: true,
		ExtraProcesses:     extra,
	})

	procs := release.Processes
	return finishDeploy(client, app, prevRelease, release, procs, placeholderID)
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
func dockerBuildJobEnv(controllerKey, imageArtifactID, sourceVersion string, releaseEnv map[string]string, appID string) map[string]string {
	jobEnv := map[string]string{
		"IMAGE_ARTIFACT_ID": imageArtifactID,
		"SOURCE_VERSION":    sourceVersion,
		"BUILDKITD_FLAGS":   "--root=/tmp/buildkitd --oci-worker-snapshotter=native",
		"CI":                "true",
		"BUILDKIT_PROGRESS": "plain",
	}
	if appID != "" {
		jobEnv["BUILD_CACHE_URL"] = signedDockerBuildCacheURL(appID, controllerKey)
	}
	if dockerfile, ok := releaseEnv["DOCKERFILE"]; ok {
		jobEnv["DOCKERFILE"] = dockerfile
	}
	return jobEnv
}

func buildJob(artifact *ct.Artifact, app *ct.App, prevRelease *ct.Release, jobEnv map[string]string, jobType, script string) *host.Job {
	return &host.Job{
		Config: host.ContainerConfig{
			Args:  []string{script},
			Env:   jobEnv,
			Stdin: true,
			// Logs stay on so the dashboard deploy page can stream the same
			// lines the git client sees. Attach reads them back from the log
			// mux when DisableLog is false.
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

// runScheduledBuild asks the controller for a pending compile and waits until
// the scheduler places it. git push stays attached for that wait and for the
// compile. A full cap returns immediately. Receiver exit stops the job,
// including one that is still pending.
func runScheduledBuild(client controller.Client, app *ct.App, prevRelease *ct.Release, artifact *ct.Artifact, jobEnv map[string]string, jobType string, args []string, resources resource.Resources, releaseEnv map[string]string, stdout, stderr io.Writer) (placeholderID string, err error) {
	placeholderID, err = ensureBuildRelease(client, app, prevRelease, artifact)
	if err != nil {
		return "", err
	}
	timeout := resolveBuildTimeout(releaseEnv)
	job, err := client.CreateBuild(app.ID, &ct.BuildRequest{
		Type:           jobType,
		ReleaseID:      prevRelease.ID,
		Env:            jobEnv,
		Artifact:       artifact,
		Args:           args,
		Resources:      resources,
		TimeoutSeconds: int(timeout / time.Second),
	})
	if err != nil {
		if dropErr := dropBuildPlaceholder(client, app, placeholderID); dropErr != nil {
			log.Println("ERROR:", dropErr)
		}
		return "", err
	}
	defer func() {
		if err != nil {
			client.DeleteJob(app.ID, job.UUID)
			if dropErr := dropBuildPlaceholder(client, app, placeholderID); dropErr != nil {
				log.Println("ERROR:", dropErr)
			}
		}
	}()
	shutdown.BeforeExit(func() {
		client.DeleteJob(app.ID, job.UUID)
		if dropErr := dropBuildPlaceholder(client, app, placeholderID); dropErr != nil {
			log.Println("ERROR:", dropErr)
		}
	})

	deadline := time.Now().Add(buildadmit.MaxPendingWait)
	placed, err := buildadmit.WaitForPlacement(func() (buildadmit.PlacementJob, error) {
		current, err := client.GetJob(app.ID, job.UUID)
		if err != nil {
			return buildadmit.PlacementJob{}, err
		}
		return buildadmit.PlacementJob{
			ID:     current.ID,
			HostID: current.HostID,
			State:  string(current.State),
			Type:   current.Type,
		}, nil
	}, deadline, nil, nil)
	if err != nil {
		return placeholderID, err
	}
	h, err := cluster.NewClient().Host(placed.HostID)
	if err != nil {
		return placeholderID, err
	}
	attach, err := h.Attach(&host.AttachReq{
		JobID: placed.ID,
		Flags: host.AttachFlagStdin | host.AttachFlagStdout | host.AttachFlagStderr | host.AttachFlagStream,
	}, true)
	if err != nil {
		return placeholderID, err
	}
	defer attach.Close()

	go func() {
		stdin := attachStdin{AttachClient: attach}
		if len(releaseEnv) > 0 {
			if copyErr := appendEnvDir(archiveReader(), stdin, releaseEnv); copyErr != nil {
				log.Println("ERROR:", copyErr)
			}
			return
		}
		if _, copyErr := io.Copy(attach, archiveReader()); copyErr != nil {
			log.Println("ERROR:", copyErr)
			return
		}
		if copyErr := attach.CloseWrite(); copyErr != nil {
			log.Println("ERROR:", copyErr)
		}
	}()

	done := make(chan error, 1)
	go func() {
		code, recvErr := attach.Receive(stdout, stderr)
		if recvErr != nil {
			done <- recvErr
			return
		}
		if code != 0 {
			done <- fmt.Errorf("build exited %d", code)
			return
		}
		done <- nil
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err = <-done:
		return placeholderID, err
	case <-timer.C:
		err = fmt.Errorf("build exceeded timeout of %s (set %s to change, cluster max %s)", timeout, buildTimeoutEnv, maxBuildTimeout)
		return placeholderID, err
	}
}

type attachStdin struct {
	cluster.AttachClient
}

func (a attachStdin) Close() error { return a.CloseWrite() }

type releaseCreator interface {
	CreateRelease(appID string, release *ct.Release) error
}

type releaseDropper interface {
	DeleteRelease(appID, releaseID string) (*ct.ReleaseDeletion, error)
}

// ensureBuildRelease creates a release row when this app has never had one.
// job_cache.release_id is NOT NULL. The row is not deployed. The builder
// image is not attached, so deleting the row cannot garbage-collect it.
// The marker stays off prev so the deployed release, which copies prev.Meta,
// is not hidden as a placeholder. The returned id is deleted after deploy.
func ensureBuildRelease(client releaseCreator, app *ct.App, prev *ct.Release, artifact *ct.Artifact) (string, error) {
	if prev == nil || prev.ID != "" || client == nil || app == nil {
		return "", nil
	}
	_ = artifact
	rel := &ct.Release{
		Processes: map[string]ct.ProcessType{},
		Meta:      map[string]string{ct.MetaBuildPlaceholder: "true"},
	}
	if err := client.CreateRelease(app.ID, rel); err != nil {
		return "", fmt.Errorf("Error creating release for build: %s", err)
	}
	prev.ID = rel.ID
	return rel.ID, nil
}

func dropBuildPlaceholder(client releaseDropper, app *ct.App, id string) error {
	if client == nil || app == nil || id == "" {
		return nil
	}
	if _, err := client.DeleteRelease(app.ID, id); err != nil {
		return fmt.Errorf("Error removing build release: %s", err)
	}
	return nil
}

func finishDeploy(client controller.Client, app *ct.App, prevRelease *ct.Release, release *ct.Release, procs map[string]ct.ProcessType, placeholderID string) error {
	defer saveReleaseProgress(client, app, release)
	if err := client.CreateRelease(app.ID, release); err != nil {
		return fmt.Errorf("Error creating release: %s", err)
	}
	if err := runReleasePhase(client, app, release); err != nil {
		return err
	}
	if err := client.DeployAppRelease(app.ID, release.ID, nil); err != nil {
		return fmt.Errorf("Error deploying app release: %s", err)
	}
	if err := dropBuildPlaceholder(client, app, placeholderID); err != nil {
		return err
	}

	if !skipDefaultScale && needsDefaultScale(app.ID, prevRelease.ID, procs, client) {
		procName := defaultScaleProcess(procs)
		receivePrintf("=====> Scaling initial release to %s=1\n", procName)

		timeout := time.Duration(app.DeployTimeout) * time.Second
		opts := ct.ScaleOptions{
			Processes:        map[string]int{procName: 1},
			Timeout:          &timeout,
			JobEventCallback: watchInitialScale(procName),
		}
		receivePrintf("-----> Waiting for initial %s job to start...\n", procName)
		if err := client.ScaleAppRelease(app.ID, release.ID, opts); err != nil {
			fmt.Printf("-----> WARN: scaling initial release down to %s=0 due to error\n", procName)
			if err := client.DeleteFormation(app.ID, release.ID); err != nil {
				fmt.Println("-----> WARN: could not scale the initial release down (it may continue to run):", err)
			}
			return err
		}
	}
	if afterDeploy != nil {
		if err := afterDeploy(release, procs); err != nil {
			return err
		}
	}

	receivePrintln("=====> Application deployed")
	printDeployURLs(client, app)
	return nil
}

// saveReleaseProgress stores the git-push transcript on the release after
// finishDeploy returns, including a failure once the release exists. A
// failed store does not fail the push.
func saveReleaseProgress(client controller.Client, app *ct.App, release *ct.Release) {
	if client == nil || app == nil || release == nil || release.ID == "" {
		return
	}
	text := progressText()
	if strings.TrimSpace(text) == "" {
		return
	}
	if err := client.PutReleaseOutput(app.ID, release.ID, []byte(text)); err != nil {
		fmt.Fprintf(os.Stderr, "-----> WARN: could not store deploy output: %s\n", err)
	}
}

func runReleasePhase(client controller.Client, app *ct.App, release *ct.Release) error {
	req := ct.ReleasePhaseNewJob(release)
	if req == nil {
		return nil
	}
	proc := release.Processes[ct.ProcessTypeRelease]
	receivePrintln("-----> Running release command")
	if cmd := ct.ProcessDisplayCommand(proc); cmd != "" {
		receivePrintf("       %s\n", cmd)
	}
	rwc, err := client.RunJobAttached(app.ID, req)
	if err != nil {
		return fmt.Errorf("Error running release command: %s", err)
	}
	defer rwc.Close()
	attachClient := cluster.NewAttachClient(rwc)
	_ = attachClient.CloseWrite()
	// pre-receive only pipes flynn-receiver stdout to `git push`.
	gitOut := newGitPushFilter(syncStdout(receiveWriter()))
	exitStatus, err := attachClient.Receive(gitOut, gitOut)
	gitOut.flush()
	if err != nil {
		return fmt.Errorf("Error running release command: %s", err)
	}
	if exitStatus != 0 {
		return ct.ReleasePhaseExitError(exitStatus)
	}
	receivePrintln("-----> Release command complete")
	return nil
}

func printDeployURLs(client controller.Client, app *ct.App) {
	if client == nil || app == nil {
		return
	}
	id := app.ID
	if id == "" {
		id = app.Name
	}
	routes, err := client.AppRouteList(id)
	if err != nil {
		fmt.Println("-----> WARN: could not list app URLs:", err)
		return
	}
	printRouteURLs(receiveWriter(), routes, os.Getenv("DEFAULT_ROUTE_DOMAIN"))
}

func printRouteURLs(w io.Writer, routes []*router.Route, clusterDomain string) {
	var urls []string
	seen := map[string]struct{}{}
	for _, r := range routes {
		for _, u := range r.PublicURLs(clusterDomain) {
			if _, ok := seen[u]; ok {
				continue
			}
			seen[u] = struct{}{}
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		return
	}
	fmt.Fprintln(w, "=====> URLs")
	for _, u := range urls {
		fmt.Fprintf(w, "       %s\n", u)
	}
}

// initialJobDownThreshold matches controller deploy scale-up: a first web
// job can exit once (host restart, brief overlay blip) and still come up.
const initialJobDownThreshold = 5

func watchInitialScale(procName string) func(*ct.Job) error {
	downs := 0
	return func(job *ct.Job) error {
		if job == nil {
			return nil
		}
		switch job.State {
		case ct.JobStateUp:
			fmt.Printf("=====> Initial %s job started\n", procName)
		case ct.JobStateDown:
			downs++
			if downs <= initialJobDownThreshold {
				fmt.Printf("-----> WARN: initial %s job exited (attempt %d/%d); waiting for a replacement\n", procName, downs, initialJobDownThreshold)
				return nil
			}
			return fmt.Errorf("Initial %s job failed to start", procName)
		}
		return nil
	}
}

// defaultScaleProcess is the process type gitreceive scales to 1 on a first
// deploy. Prefer "web" (buildpack + Dockerfile git push); fall back to "app"
// for historical docker-push releases.
func defaultScaleProcess(procs map[string]ct.ProcessType) string {
	if _, ok := procs["web"]; ok {
		return "web"
	}
	if _, ok := procs["app"]; ok {
		return "app"
	}
	return ""
}

// needsDefaultScale indicates whether a release needs a default scale based on
// whether it has a web (or app) process type and either has no previous
// release or no previous scale.
func needsDefaultScale(appID, prevReleaseID string, procs map[string]ct.ProcessType, client controller.Client) bool {
	if defaultScaleProcess(procs) == "" {
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

// gitPushFilter drops host minute metrics lines from the git client stream.
// The same lines stay in the job log, so `flynn log` and the logs tab still
// show them.
type gitPushFilter struct {
	mu  sync.Mutex
	dst io.Writer
	buf []byte
}

func newGitPushFilter(dst io.Writer) *gitPushFilter {
	return &gitPushFilter{dst: dst}
}

func (w *gitPushFilter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			if len(w.buf) > 64*1024 {
				if err := w.writeRaw(w.buf); err != nil {
					return len(p), err
				}
				w.buf = nil
			}
			return len(p), nil
		}
		line := w.buf[:i+1]
		rest := append([]byte(nil), w.buf[i+1:]...)
		if !gitPushDropLine(line) {
			if err := w.writeRaw(line); err != nil {
				return len(p), err
			}
		}
		w.buf = rest
	}
}

func (w *gitPushFilter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) == 0 {
		return
	}
	if !gitPushDropLine(w.buf) {
		_ = w.writeRaw(w.buf)
	}
	w.buf = nil
}

func (w *gitPushFilter) writeRaw(p []byte) error {
	_, err := w.dst.Write(p)
	return err
}

func gitPushMetricsLine(line []byte) bool {
	s := bytes.TrimSpace(line)
	return bytes.HasPrefix(s, []byte("metrics ")) && bytes.Contains(s, []byte("cpu_percent="))
}

// gitPushDropLine hides host noise from the git client. Metrics stay in the
// job log. Builder start lines only repeat `/builder/build.sh`.
func gitPushDropLine(line []byte) bool {
	if gitPushMetricsLine(line) {
		return true
	}
	return gitPushBuilderStart(bytes.TrimSpace(line))
}

func gitPushBuilderStart(s []byte) bool {
	for _, kind := range [][]byte{[]byte("slugbuilder"), []byte("dockerbuilder")} {
		if bytes.HasPrefix(s, append(append([]byte("Starting "), kind...), ' ')) {
			return true
		}
		if !bytes.HasPrefix(s, kind) {
			continue
		}
		rest := s[len(kind):]
		if bytes.HasPrefix(rest, []byte(" process started")) {
			return true
		}
		if len(rest) > 0 && rest[0] == '-' && bytes.Contains(rest, []byte(" process started")) {
			return true
		}
	}
	return false
}
