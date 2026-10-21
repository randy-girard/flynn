package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/appmanifest"
	"github.com/randy-girard/flynn/pkg/githubapp"
)

// buildInput is the git archive fed to slugbuilder or dockerbuilder. Tests
// that call the deploy helpers leave it unset, and the build reads os.Stdin.
var buildInput io.Reader

// skipDefaultScale is set when flynn-app.json has a formation. Finish scales
// those quantities instead of the implicit web=1.
var skipDefaultScale bool

// afterDeploy runs formation, routes, postdeploy, and GitHub once the release
// is current.
var afterDeploy func(release *ct.Release, procs map[string]ct.ProcessType) error

func archiveReader() io.Reader {
	if buildInput != nil {
		return buildInput
	}
	return os.Stdin
}

func cleanupBuildInput() {
	f, ok := buildInput.(*os.File)
	if !ok {
		return
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	buildInput = nil
}

func prepareReceiveManifest(client controller.Client, app *ct.App, releaseEnv map[string]string) error {
	spooled, err := appmanifest.Spool(os.Stdin)
	if err != nil {
		return err
	}
	manifest, hasBuildpacks, err := appmanifest.RootFiles(spooled)
	if err != nil {
		spooled.Close()
		os.Remove(spooled.Name())
		return err
	}
	if _, err := spooled.Seek(0, io.SeekStart); err != nil {
		spooled.Close()
		os.Remove(spooled.Name())
		return err
	}
	buildInput = spooled

	pending := false
	if app != nil && app.Meta != nil && strings.TrimSpace(app.Meta[appmanifest.MetaStatus]) == appmanifest.StatusPending {
		pending = true
	}
	if len(bytes.TrimSpace(manifest)) == 0 && !pending {
		return nil
	}

	ap, err := appmanifest.New(client, os.Stdout, true)
	if err != nil {
		return err
	}
	ap.Hooks = appmanifest.Hooks{
		Pipeline:        pipelineEnvironment,
		GitHubInstalled: func() bool { return ap.Catalog != nil && ap.Catalog.Lookup("github") != nil },
		GitHubConnected: githubConnected,
		ConnectGitHub:   connectManifestGitHub,
	}
	state, err := ap.LoadState(app.ID)
	if err != nil {
		return err
	}
	action := appmanifest.Decide(state, len(bytes.TrimSpace(manifest)) > 0)
	switch action {
	case appmanifest.ActionNone:
		return nil
	case appmanifest.ActionSkip:
		fmt.Println(appmanifest.SkipLine)
		return nil
	case appmanifest.ActionResume:
		if len(bytes.TrimSpace(manifest)) == 0 {
			return fmt.Errorf("flynn-app.json is pending and the file is missing; cannot resume")
		}
	}

	ph := map[string]string{"app": app.Name}
	if domain := strings.TrimSpace(os.Getenv("DEFAULT_ROUTE_DOMAIN")); domain != "" {
		ph["cluster"] = domain
	}
	res, err := ap.Prepare(app.ID, manifest, appmanifest.PrepareOptions{
		ExistingEnv:       releaseEnv,
		Placeholders:      ph,
		RepoHasBuildpacks: hasBuildpacks,
		HonorImage:        false,
	})
	if err != nil {
		return err
	}
	skipDefaultScale = res.HasFormation
	afterDeploy = func(release *ct.Release, procs map[string]ct.ProcessType) error {
		return ap.Finish(app.ID, appmanifest.FinishInput{
			Release:       release,
			Processes:     procs,
			DefaultScaled: !res.HasFormation,
		})
	}
	if len(res.InjectBuildpacks) == 0 {
		return nil
	}
	if _, err := spooled.Seek(0, io.SeekStart); err != nil {
		return err
	}
	rewritten, err := os.CreateTemp("", "flynn-app-json-build-")
	if err != nil {
		return err
	}
	body := strings.Join(res.InjectBuildpacks, "\n") + "\n"
	if err := appmanifest.InjectRootFile(spooled, rewritten, ".buildpacks", []byte(body)); err != nil {
		rewritten.Close()
		os.Remove(rewritten.Name())
		return err
	}
	if _, err := rewritten.Seek(0, io.SeekStart); err != nil {
		rewritten.Close()
		os.Remove(rewritten.Name())
		return err
	}
	spooled.Close()
	os.Remove(spooled.Name())
	buildInput = rewritten
	return nil
}

func pipelineEnvironment(appID string) (appmanifest.PipelineContext, error) {
	endpoint := "http://pipeline.discoverd/apps/" + url.PathEscape(appID) + "/environment"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return appmanifest.PipelineContext{}, err
	}
	req.SetBasicAuth("", os.Getenv("CONTROLLER_KEY"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return appmanifest.PipelineContext{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return appmanifest.PipelineContext{}, nil
	}
	if res.StatusCode != http.StatusOK {
		return appmanifest.PipelineContext{}, fmt.Errorf("pipeline environment: %s", res.Status)
	}
	var body struct {
		Environment  string            `json:"environment"`
		Placeholders map[string]string `json:"placeholders"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return appmanifest.PipelineContext{}, err
	}
	return appmanifest.PipelineContext{Environment: body.Environment, Placeholders: body.Placeholders}, nil
}

func githubConnected(appID string) (bool, error) {
	endpoint := "http://github.discoverd/apps/" + url.PathEscape(appID) + "/github"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	req.SetBasicAuth("", os.Getenv("CONTROLLER_KEY"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("github: %s", res.Status)
	}
	return true, nil
}

func connectManifestGitHub(appID string, spec appmanifest.GitHub) error {
	owner, name, err := githubapp.SplitOwnerRepo(spec.Repository)
	if err != nil {
		return err
	}
	body, err := json.Marshal(ct.GitHubRepoConnection{
		Owner:         owner,
		Repo:          name,
		Branch:        spec.Branch,
		AutoDeploy:    spec.AutoDeploy,
		WaitForChecks: spec.WaitChecks,
	})
	if err != nil {
		return err
	}
	endpoint := "http://github.discoverd/apps/" + url.PathEscape(appID) + "/github"
	req, err := http.NewRequest(http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth("", os.Getenv("CONTROLLER_KEY"))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(res.Body)
		return fmt.Errorf("github connect: %s", strings.TrimSpace(string(msg)))
	}
	return nil
}
