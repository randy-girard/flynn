package plugin

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
)

const (
	blobstorePrefix = "http://blobstore.discoverd/plugins"
	deployTimeout   = 5 * time.Minute
	pingTimeout     = 2 * time.Minute
)

// Installer deploys any plugin described by flynn-plugin.json. Callers supply
// a controller client and an HTTP client that can resolve *.discoverd (the
// flynn-host CLI already has that).
type Installer struct {
	Client controller.Client
	HTTP   *http.Client
	// GitHubHTTP fetches release assets. Tests swap this; default is a
	// timeout client that does not use discoverd.
	GitHubHTTP *http.Client
	Stdout     io.Writer
	Stderr     io.Writer
	// Build, if set, is invoked as (pluginRoot) when dist/ is missing.
	// Tests replace this; the default runs script/plugin-build.
	Build func(pluginRoot string) error
}

type InstallOptions struct {
	Source      string
	Ref         string
	GitHubOrg   string
	Cwd         string
	NoBuild     bool
	Rebuild     bool
	PluginsFile string
	CredsFile   string
}

func (in *Installer) logf(format string, args ...interface{}) {
	if in.Stdout == nil {
		return
	}
	fmt.Fprintf(in.Stdout, format+"\n", args...)
}

func (in *Installer) Install(opts InstallOptions) error {
	resolved, err := Resolve(opts)
	if err != nil {
		return err
	}

	root := resolved.Dir
	if resolved.GitHub != nil {
		if opts.Rebuild {
			return fmt.Errorf("GitHub install cannot --rebuild; install from a local checkout to build")
		}
		dir, err := in.fetchGitHub(resolved.GitHub, opts.CredsFile)
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		root = dir
		if resolved.Ref == "" {
			resolved.Ref = resolved.GitHub.Ref
		}
	}

	m, err := LoadManifest(root)
	if err != nil {
		return err
	}
	in.logf("installing plugin %s (kind %s) from %s", m.Name, m.Kind, displaySource(resolved, root))

	if resolved.GitHub != nil {
		if !DistReady(root) {
			return fmt.Errorf("plugin %s GitHub release is missing image.json / layers; publish Build and Release or install from a local checkout", m.Name)
		}
	} else if opts.Rebuild || !DistReady(root) {
		if opts.NoBuild {
			return fmt.Errorf("plugin %s has no built image under dist/; run script/plugin-build or omit --no-build", m.Name)
		}
		in.logf("building plugin image (dist/ missing or --rebuild)")
		if err := in.runBuild(root); err != nil {
			return fmt.Errorf("plugin-build: %w", err)
		}
	}

	dist, err := LoadDist(root)
	if err != nil {
		return err
	}

	artifact, err := in.uploadArtifact(m.Name, dist)
	if err != nil {
		return err
	}

	cluster, err := ClusterEnv(in.Client)
	if err != nil {
		return fmt.Errorf("cluster credentials: %w", err)
	}

	if err := in.runHook(root, m, m.installHook(), cluster); err != nil {
		return err
	}

	app, err := in.Client.GetApp(m.App.Name)
	switch {
	case err == nil:
		in.logf("app %s already exists; deploying a new release from uploaded layers", m.App.Name)
		if app.Meta == nil {
			app.Meta = map[string]string{}
		}
		m.AnnotateInstall(app.Meta, resolved.Input, resolved.Ref)
		if err := in.Client.UpdateAppMeta(app); err != nil {
			return fmt.Errorf("update plugin meta on %s: %w", m.App.Name, err)
		}
		if err := in.deployRelease(app, m, artifact, cluster); err != nil {
			return err
		}
	case err == controller.ErrNotFound:
		app, err = in.createAndDeployApp(m, artifact, cluster, resolved)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("get app %s: %w", m.App.Name, err)
	}

	if m.Kind == KindResourceProvider {
		if err := ensureProvider(in.Client, m.Provider.Name, m.Provider.URL); err != nil {
			return err
		}
	}

	if ping := m.PingURL(); ping != "" {
		in.logf("waiting for %s", ping)
		if err := waitHTTP(in.http(), ping, pingTimeout); err != nil {
			return fmt.Errorf("plugin %s did not become ready: %w", m.Name, err)
		}
	}

	_ = app
	in.logf("plugin %s installed", m.Name)
	return nil
}

func (m *Manifest) installHook() string {
	if m.Hooks != nil {
		return m.Hooks.Install
	}
	return ""
}

func (in *Installer) runBuild(root string) error {
	if in.Build != nil {
		return in.Build(root)
	}
	script := filepath.Join(root, "script", "plugin-build")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("missing %s", script)
	}
	cmd := exec.Command(script)
	cmd.Dir = root
	cmd.Stdout = in.Stdout
	cmd.Stderr = in.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

func (in *Installer) uploadArtifact(pluginName string, dist *DistArtifact) (*ct.Artifact, error) {
	httpClient := in.http()
	prefix := fmt.Sprintf("%s/%s", blobstorePrefix, pluginName)
	layerTmpl := prefix + "/layers/{id}.squashfs"

	ls := layers(dist.Artifact)
	in.logf("uploading %d layer(s) for %s", len(ls), pluginName)
	for _, layer := range ls {
		src := dist.LayerPath(layer.ID)
		url := fmt.Sprintf("%s/layers/%s.squashfs", prefix, layer.ID)
		in.logf("uploading layer %s (%d bytes)", layer.ID, layer.Length)
		if err := putFile(httpClient, url, src); err != nil {
			return nil, fmt.Errorf("upload layer %s: %w", layer.ID, err)
		}
	}

	raw := dist.Artifact.RawManifest
	manifestID := dist.Artifact.Manifest().ID()
	if manifestID == "" && dist.Artifact.Hashes != nil {
		manifestID = dist.Artifact.Hashes["sha512_256"]
	}
	manifestURL := fmt.Sprintf("%s/images/%s.json", prefix, manifestID)
	if p := dist.ManifestPath(); p != "" {
		if err := putFile(httpClient, manifestURL, p); err != nil {
			return nil, fmt.Errorf("upload manifest: %w", err)
		}
	} else {
		if err := putBytes(httpClient, manifestURL, raw); err != nil {
			return nil, fmt.Errorf("upload manifest: %w", err)
		}
	}

	meta := map[string]string{"blobstore": "true", "flynn.plugin": "true"}
	for k, v := range dist.Artifact.Meta {
		meta[k] = v
	}
	meta["blobstore"] = "true"

	art := &ct.Artifact{
		Type:             ct.ArtifactTypeFlynn,
		URI:              manifestURL,
		Meta:             meta,
		RawManifest:      raw,
		Hashes:           dist.Artifact.Hashes,
		Size:             dist.Artifact.Size,
		LayerURLTemplate: layerTmpl,
	}
	if art.Size == 0 {
		art.Size = int64(len(raw))
	}
	if err := in.Client.CreateArtifact(art); err != nil {
		return nil, fmt.Errorf("create artifact: %w", err)
	}
	return art, nil
}

func (in *Installer) createAndDeployApp(m *Manifest, image *ct.Artifact, cluster map[string]string, resolved *Resolved) (*ct.App, error) {
	source, ref := "", ""
	if resolved != nil {
		source = resolved.Input
		ref = resolved.Ref
	}
	app := &ct.App{
		Name: m.App.Name,
		Meta: m.AnnotateInstall(m.AppMeta(), source, ref),
	}
	if err := in.Client.CreateApp(app); err != nil {
		return nil, fmt.Errorf("create app %s: %w", m.App.Name, err)
	}
	if err := in.deployRelease(app, m, image, cluster); err != nil {
		return nil, err
	}
	return app, nil
}

func (in *Installer) deployRelease(app *ct.App, m *Manifest, image *ct.Artifact, cluster map[string]string) error {
	release := &ct.Release{
		ArtifactIDs: []string{image.ID},
		Env:         ReleaseEnv(m, image.ID, cluster),
		Processes:   m.App.Processes,
	}
	if err := in.Client.CreateRelease(app.ID, release); err != nil {
		return fmt.Errorf("create release: %w", err)
	}

	procs := map[string]int{}
	n := SingletonWebCount(cluster)
	for name := range m.App.Processes {
		procs[name] = n
	}
	timeout := deployTimeout
	if err := in.Client.ScaleAppRelease(app.ID, release.ID, ct.ScaleOptions{
		Processes: procs,
		Timeout:   &timeout,
	}); err != nil {
		return fmt.Errorf("scale %s: %w", m.App.Name, err)
	}
	if err := in.Client.SetAppRelease(app.ID, release.ID); err != nil {
		return fmt.Errorf("set release: %w", err)
	}
	return nil
}

func (in *Installer) runHook(root string, m *Manifest, rel string, cluster map[string]string) error {
	if rel == "" {
		return nil
	}
	script := filepath.Join(root, rel)
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("hooks.install %s: %w", rel, err)
	}
	cmd := exec.Command(script)
	cmd.Dir = root
	cmd.Stdout = in.Stdout
	cmd.Stderr = in.Stderr
	cmd.Env = append(os.Environ(),
		"CONTROLLER_KEY="+cluster["CONTROLLER_KEY"],
		"CLUSTER_DOMAIN="+cluster["CLUSTER_DOMAIN"],
		"FLYNN_PLUGIN_NAME="+m.Name,
		"FLYNN_PLUGIN_KIND="+m.Kind,
		"FLYNN_PLUGIN_ROOT="+root,
	)
	in.logf("running hook %s", rel)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hooks.install: %w", err)
	}
	return nil
}

func (in *Installer) http() *http.Client {
	if in.HTTP != nil {
		return in.HTTP
	}
	return http.DefaultClient
}

func ensureProvider(client controller.Client, name, url string) error {
	providers, err := client.ProviderList()
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}
	for _, p := range providers {
		if p.Name == name {
			return nil
		}
	}
	if err := client.CreateProvider(&ct.Provider{Name: name, URL: url}); err != nil {
		return fmt.Errorf("create provider %s: %w", name, err)
	}
	return nil
}

func putFile(httpClient *http.Client, url, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, url, f)
	if err != nil {
		return err
	}
	req.ContentLength = st.Size()
	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("PUT %s: %s", url, res.Status)
	}
	return nil
}

func putBytes(httpClient *http.Client, url string, body []byte) error {
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("PUT %s: %s", url, res.Status)
	}
	return nil
}

func waitHTTP(httpClient *http.Client, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		res, err := httpClient.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode >= 200 && res.StatusCode < 300 {
				return nil
			}
			last = fmt.Errorf("status %s", res.Status)
		} else {
			last = err
		}
		time.Sleep(2 * time.Second)
	}
	if last == nil {
		last = fmt.Errorf("timeout")
	}
	return last
}

func displaySource(r *Resolved, root string) string {
	if r != nil && r.GitHub != nil {
		return r.GitHub.String()
	}
	return root
}
