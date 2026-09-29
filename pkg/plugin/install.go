package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/attempt"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/httphelper"
	router "github.com/randy-girard/flynn/router/types"
)

const (
	blobstorePrefix = "http://blobstore.discoverd/plugins"
	deployTimeout   = 5 * time.Minute
	pingTimeout     = 2 * time.Minute
)

// waitHTTPProbeTimeout bounds one GET inside waitHTTP so a hung discoverd
// instance cannot consume the whole ping/deploy deadline. Do not set
// Client.Timeout on Installer.HTTP; blobstore PUTs share that client.
var waitHTTPProbeTimeout = 5 * time.Second
var waitHTTPRetryDelay = 2 * time.Second
var waitHTTPLogInterval = 15 * time.Second

// provisionResourceAttempts retries postgres-api (and other provider) 500s.
// A long plugin image upload can race a brief postgres/controller blip, and
// httphelper used to collapse those to "unknown_error: Something went wrong".
var provisionResourceAttempts = attempt.Strategy{
	Min:   5,
	Total: 90 * time.Second,
	Delay: 2 * time.Second,
}

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
	Stdin      io.Reader
	Stderr     io.Writer
	// Interactive, if set, overrides TTY detection for setup prompts.
	Interactive func() bool
	// Build, if set, is invoked as (pluginRoot) when dist/ is missing.
	// Tests replace this; the default runs script/plugin-build.
	Build func(pluginRoot string) error
	// Hosts, if set, lists flynn-host members for webhook registration.
	// Tests replace this; the default is cluster.NewClient().Hosts.
	Hosts func() ([]WebhookHost, error)
	// RouteClient, if set, is used for HTTP routes and ACME. Tests
	// replace this; the default is Client.
	RouteClient RouteClient
	// UninstallClient is the controller subset used by Uninstall. Tests
	// replace this; the default is Client.
	UninstallClient uninstallAPI
	// FlynnVersion overrides the running Flynn release for tests.
	// Empty uses ClusterFlynnVersion (version.Release / FLYNN_VERSION).
	FlynnVersion string
	// LayerCacheDir overrides /var/lib/flynn/layer-cache for tests.
	LayerCacheDir string
	// BlobstorePrefix overrides http://blobstore.discoverd/plugins for tests.
	BlobstorePrefix string
	// AllowExternalLayers fetches image.json LayerURL hosts that are not
	// GitHub. The GitHub token is never sent to those hosts (SEC-018).
	AllowExternalLayers bool
}

// WebhookHost is the subset of pkg/cluster.Host used to register webhooks
// (the same API as `flynn-host webhooks:add`).
type WebhookHost interface {
	ID() string
	ListWebhooks() ([]*host.WebhookConfig, error)
	AddWebhook(id, url string, headers map[string]string) (*host.WebhookConfig, error)
	RemoveWebhook(id string) error
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
	// AutoTLS enables Let's Encrypt on this plugin's HTTP routes (the
	// same as `flynn route add http --auto-tls`). Requires cluster ACME.
	AutoTLS bool
	// Update is flynn-host plugin:update: the plugin app must already exist.
	// Install still upgrades an existing app, but update refuses a missing one.
	Update bool
	// AllowExternalLayers is --allow-external-layers: fetch non-GitHub
	// layer URLs from image.json without GitHub credentials.
	AllowExternalLayers bool
	// Yes is --yes: accept cluster-secret injection without a prompt.
	// Required for third-party plugins when stdin is not a TTY.
	Yes bool
}

// RouteClient is the controller subset used to create HTTP/TCP routes and
// attach ACME. Tests replace Installer.RouteClient / PluginRouter.Client.
type RouteClient interface {
	AppRouteList(appID string) ([]*router.Route, error)
	CreateRoute(appID string, route *router.Route) error
	UpdateRoute(appID, routeID string, route *router.Route) error
	DeleteRoute(appID, routeID string) error
	GetACMEConfig() (*ct.ACMEConfig, error)
}

type providerClient interface {
	ProviderList() ([]*ct.Provider, error)
	CreateProvider(*ct.Provider) error
}

func (in *Installer) logf(format string, args ...interface{}) {
	if in.Stdout == nil {
		return
	}
	fmt.Fprintf(in.Stdout, format+"\n", args...)
}

func (in *Installer) Install(opts InstallOptions) error {
	opts.Update = false
	return in.apply(opts)
}

// Update deploys a new release of an already-installed plugin. A local
// checkout runs script/plugin-build unless --no-build so source changes
// actually ship (reusing dist/ would redeploy the previous squashfs).
func (in *Installer) Update(opts InstallOptions) error {
	opts.Update = true
	return in.apply(opts)
}

// pluginImageBuildNeeded reports whether a local checkout should run
// script/plugin-build. GitHub releases ship dist/. --no-build never compiles.
// plugin:update of a local path compiles even when dist/ already exists so
// "layer already present; skip upload" is not a silent no-op of old squashfs.
func pluginImageBuildNeeded(opts InstallOptions, distReady bool) bool {
	if opts.Rebuild {
		return true
	}
	if opts.NoBuild {
		return false
	}
	if !distReady {
		return true
	}
	return opts.Update
}

func (in *Installer) flynnVersion() string {
	if in != nil && strings.TrimSpace(in.FlynnVersion) != "" {
		return strings.TrimSpace(in.FlynnVersion)
	}
	return ClusterFlynnVersion()
}

func (in *Installer) refuseIncompatiblePlugin(tag string) error {
	flynn, ok := ParseFlynnCalVer(in.flynnVersion())
	if !ok {
		return nil
	}
	tag = strings.TrimSpace(tag)
	if tag == "" || tag == "latest" {
		return nil
	}
	if PluginMatchesFlynn(tag, flynn.FlynnLine()) {
		return nil
	}
	return incompatiblePluginError(tag, flynn)
}

func (in *Installer) apply(opts InstallOptions) error {
	in.AllowExternalLayers = opts.AllowExternalLayers
	resolved, err := Resolve(opts)
	if err != nil {
		return err
	}

	root := resolved.Dir
	if resolved.GitHub != nil {
		if opts.Rebuild {
			return fmt.Errorf("GitHub plugin fetch cannot --rebuild; use a local checkout to build")
		}
		if err := in.refuseIncompatiblePlugin(resolved.Ref); err != nil {
			return err
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
		if err := in.refuseIncompatiblePlugin(resolved.Ref); err != nil {
			return err
		}
	}

	m, err := LoadManifest(root)
	if err != nil {
		return err
	}
	in.logf("%s plugin %s (kind %s) from %s", applyVerb(opts.Update), m.Name, m.Kind, displaySource(resolved, root))

	if opts.AutoTLS && !hasHTTPRoute(m) {
		return fmt.Errorf("--auto-tls requires an HTTP route; plugin %s has none", m.Name)
	}

	alreadyInstalled := false
	if in.Client != nil {
		if app, err := in.getInstalledPluginApp(m); err == nil && app != nil {
			alreadyInstalled = true
		} else if err != nil && err != controller.ErrNotFound {
			return err
		}
	}
	if opts.Update && in.Client != nil && !alreadyInstalled {
		return fmt.Errorf("plugin %s is not installed; flynn-host plugin:install %s", m.Name, m.Name)
	}
	if err := in.confirmClusterSecrets(m, resolved, opts, alreadyInstalled); err != nil {
		return err
	}

	if resolved.GitHub != nil {
		if !DistReady(root) {
			return fmt.Errorf("plugin %s GitHub release is missing image.json / layers; publish Build and Release or install from a local checkout", m.Name)
		}
	} else {
		distReady := DistReady(root)
		if opts.Rebuild && opts.NoBuild {
			in.logf("ignoring --no-build because --rebuild was set")
		}
		if pluginImageBuildNeeded(opts, distReady) {
			in.logf("building plugin image (dist/ missing, --rebuild, or local plugin:update)")
			if err := in.runBuild(root); err != nil {
				return fmt.Errorf("plugin-build: %w", err)
			}
		} else if !distReady {
			return fmt.Errorf("plugin %s has no built image under dist/; drop --no-build or run script/plugin-build", m.Name)
		} else {
			in.logf("using existing dist/; pass --rebuild to compile this checkout")
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

	app, err := in.getInstalledPluginApp(m)
	updating := opts.Update
	switch {
	case err == nil:
		if !opts.Update {
			in.logf("plugin %s already installed; updating in place", m.Name)
			updating = true
		}
		in.logf("app %s already exists; deploying a new release from uploaded layers", app.Name)
		if app.Meta == nil {
			app.Meta = map[string]string{}
		}
		m.AnnotateInstall(app.Meta, resolved.StampSource(), resolved.Ref)
		if err := in.Client.UpdateAppMeta(app); err != nil {
			return fmt.Errorf("update plugin meta on %s: %w", app.Name, err)
		}
		if prev, err := in.Client.GetAppRelease(app.ID); err == nil && prev != nil {
			PreservePreviousEnv(cluster, prev.Env)
		}
	case err == controller.ErrNotFound:
		if opts.Update {
			return fmt.Errorf("plugin %s is not installed; flynn-host plugin:install %s", m.Name, m.Name)
		}
		app, err = in.createApp(m, resolved)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("get app %s: %w", m.App.Name, err)
	}

	if !updating {
		if err := in.applySetup(m, cluster); err != nil {
			return err
		}
	}

	if err := in.provisionResources(app, m, cluster); err != nil {
		return err
	}

	in.logf("deploying %s", m.App.Name)
	dep, err := in.deployRelease(app, m, artifact, cluster)
	if err != nil {
		return err
	}

	if m.Kind == KindResourceProvider {
		if err := ensureProvider(in.Client, m.Provider.Name, m.Provider.URL, m.Provider.TenantSafe); err != nil {
			return err
		}
	}

	if err := in.ensureRoutes(app, m, cluster, opts.AutoTLS); err != nil {
		return err
	}

	if ping := m.PingURL(); ping != "" {
		waitFor := pingTimeout
		if dep != nil && dep.timeout > waitFor {
			waitFor = dep.timeout
		}
		in.logf("waiting for %s (timeout %s)", ping, waitFor)
		if err := waitHTTPLog(in.http(), ping, waitFor, in.logf); err != nil {
			in.restorePreviousRelease(app, dep)
			return fmt.Errorf("plugin %s did not become ready: %w", m.Name, err)
		}
	}

	// hooks.install (or upgrade) after the app is up so operator logs are not
	// the last line of a hung ScaleAppRelease.
	if err := in.runHook(root, m, m.deployHook(updating), cluster); err != nil {
		return err
	}

	if err := in.runHook(root, m, m.readyHook(), cluster); err != nil {
		return err
	}

	in.ensureManifestDBRuntimes(m)

	if err := in.ensureWebhooks(m, cluster); err != nil {
		return err
	}

	in.persistInventory()
	if updating {
		in.logf("plugin %s updated", m.Name)
	} else {
		in.logf("plugin %s installed", m.Name)
	}
	return nil
}

func (in *Installer) persistInventory() {
	if in.Client == nil {
		return
	}
	apps, err := in.Client.AppList()
	if err != nil {
		in.logf("warning: could not list apps for plugin inventory: %s", err)
		return
	}
	if err := WriteInstalled("", ListInstalled(apps)); err != nil {
		in.logf("warning: could not write %s: %s", InstalledFile(), err)
	}
}

func applyVerb(update bool) string {
	if update {
		return "updating"
	}
	return "installing"
}

func (m *Manifest) installHook() string {
	if m.Hooks != nil {
		return m.Hooks.Install
	}
	return ""
}

func (m *Manifest) upgradeHook() string {
	if m.Hooks != nil {
		return strings.TrimSpace(m.Hooks.Upgrade)
	}
	return ""
}

// deployHook is hooks.install on first install. On update it is hooks.upgrade
// when declared; the install hook is not re-run (it may not be idempotent).
// apply() runs this after deploy and the wait URL.
func (m *Manifest) deployHook(updating bool) string {
	if updating {
		return m.upgradeHook()
	}
	return m.installHook()
}

func (m *Manifest) uninstallHook() string {
	if m.Hooks != nil {
		return strings.TrimSpace(m.Hooks.Uninstall)
	}
	return ""
}

func (m *Manifest) readyHook() string {
	if m.Hooks != nil {
		return strings.TrimSpace(m.Hooks.Ready)
	}
	return ""
}

func (in *Installer) runBuild(root string) error {
	if in.Build != nil {
		return in.Build(root)
	}
	if flynnRoot := FlynnSourceRoot(); flynnRoot != "" {
		restore, err := replacePluginFlynnModule(root, flynnRoot)
		if err != nil {
			return fmt.Errorf("plugin-build: compile against Flynn: %w", err)
		}
		if restore != nil {
			defer restore()
		}
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
	cmd.Env = mergeGoBinPath(append(os.Environ(), in.localFlynnImageEnv()...), "/usr/local/go/bin")
	if flynnRoot := FlynnSourceRoot(); flynnRoot != "" {
		cmd.Env = append(cmd.Env, "FLYNN_ROOT="+flynnRoot)
	}
	return cmd.Run()
}

func pathListContains(path, dir string) bool {
	for _, p := range strings.Split(path, string(os.PathListSeparator)) {
		if p == dir {
			return true
		}
	}
	return false
}

// mergeGoBinPath prepends goBin to PATH when goBin/go exists. sudo flynn-host
// plugin:update uses secure_path and drops /usr/local/go/bin on vagrant-dev.
func mergeGoBinPath(env []string, goBin string) []string {
	goBin = strings.TrimRight(goBin, string(os.PathSeparator))
	if goBin == "" {
		return env
	}
	if st, err := os.Stat(filepath.Join(goBin, "go")); err != nil || st.IsDir() {
		return env
	}
	out := make([]string, 0, len(env)+1)
	foundPATH := false
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k != "PATH" {
			out = append(out, e)
			continue
		}
		foundPATH = true
		if pathListContains(v, goBin) {
			out = append(out, e)
			continue
		}
		out = append(out, "PATH="+goBin+string(os.PathListSeparator)+v)
	}
	if !foundPATH {
		cur := os.Getenv("PATH")
		if !pathListContains(cur, goBin) {
			cur = goBin + string(os.PathListSeparator) + cur
		}
		out = append(out, "PATH="+cur)
	}
	return out
}

func (in *Installer) localFlynnImageEnv() []string {
	env := LocalFlynnImageEnv()
	if in != nil && in.LayerCacheDir != "" {
		replaced := false
		prefix := EnvLayersDir + "="
		for i, e := range env {
			if strings.HasPrefix(e, prefix) {
				env[i] = prefix + in.LayerCacheDir
				replaced = true
			}
		}
		if !replaced {
			env = append(env, prefix+in.LayerCacheDir)
		}
	}
	return env
}

func (in *Installer) pluginBlobstorePrefix() string {
	if in != nil && strings.TrimSpace(in.BlobstorePrefix) != "" {
		return strings.TrimRight(strings.TrimSpace(in.BlobstorePrefix), "/")
	}
	return blobstorePrefix
}

func (in *Installer) pluginObjectPrefix(pluginName string) string {
	return fmt.Sprintf("%s/%s", in.pluginBlobstorePrefix(), pluginName)
}

func (in *Installer) uploadDistLayers(pluginName string, dist *DistArtifact) (layerTmpl string, extraMeta map[string]string, err error) {
	if dist == nil || dist.Artifact == nil {
		return "", nil, fmt.Errorf("missing plugin image")
	}
	httpClient := in.http()
	prefix := in.pluginObjectPrefix(pluginName)
	layerTmpl = prefix + "/layers/{id}.squashfs"
	extraMeta = map[string]string{}
	ls := layers(dist.Artifact)
	in.logf("uploading %d layer(s) for %s", len(ls), pluginName)
	for i, layer := range ls {
		if layer == nil || layer.ID == "" {
			return "", nil, fmt.Errorf("image layer %d missing id", i)
		}
		src := dist.LayerPath(layer.ID)
		url := fmt.Sprintf("%s/layers/%s.squashfs", prefix, layer.ID)
		size := layerSourceSize(src, layer)
		overlay := i == len(ls)-1
		skip, flynnURL := in.skipLayerUpload(httpClient, url, layer.ID, size, overlay)
		if skip {
			if flynnURL != "" && flynnURL != url {
				extraMeta["layer_url."+layer.ID] = flynnURL
			}
			in.logf("layer %s already present (%d bytes); skip upload", layer.ID, size)
			continue
		}
		in.logf("uploading layer %s (%d bytes)", layer.ID, layer.Length)
		if err := putFile(httpClient, url, src); err != nil {
			return "", nil, fmt.Errorf("upload layer %s: %w", layer.ID, err)
		}
	}
	return layerTmpl, extraMeta, nil
}

// skipLayerUpload reports whether a layer PUT can be omitted.
//
// Overlay/delta (the last plugin layer) is skipped only when HEAD/GET on the
// plugin-prefix blobstore object already matches size.
//
// Flynn OS layers (not the last layer) skip a duplicate plugin-prefix PUT when
// jobs can still fetch the squashfs: either the Flynn blobstore already has
// the object, or this host's layer-cache has it and Flynn images.json /
// artifacts already publish a LayerURL (GitHub or blobstore) — the same
// source flynn-host update used. LayerURLTemplate is the plugin prefix, so
// skipping PUT without a Flynn LayerURL 404s on other hosts.
func (in *Installer) skipLayerUpload(httpClient *http.Client, pluginURL, id string, size int64, overlay bool) (skip bool, flynnURL string) {
	if blobstoreHasObject(httpClient, pluginURL, size) {
		return true, ""
	}
	if overlay {
		return false, ""
	}
	if in.localFlynnLayer(id) == "" && !in.clusterHasFlynnLayer(id) {
		return false, ""
	}
	flynnURL = in.flynnLayerURL(id)
	if flynnURL == "" || flynnURL == pluginURL {
		return false, ""
	}
	if in.reusableLayerURL(flynnURL, pluginURL) && blobstoreHasObject(httpClient, flynnURL, size) {
		return true, flynnURL
	}
	if in.localFlynnLayer(id) != "" {
		return true, flynnURL
	}
	return false, ""
}

func layerSourceSize(src string, layer *ct.ImageLayer) int64 {
	if src != "" {
		if st, err := os.Stat(src); err == nil {
			return st.Size()
		}
	}
	if layer != nil {
		return layer.Length
	}
	return 0
}

func (in *Installer) reusableLayerURL(raw, pluginURL string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	if blobstoreauth.IsBlobstoreURL(u) {
		return true
	}
	p, err := url.Parse(pluginURL)
	if err != nil || p.Host == "" || u.Host == "" {
		return false
	}
	// Tests point BlobstorePrefix at httptest; reuse Flynn URLs on that host only.
	return canonicalHTTPHost(u.Hostname()) == canonicalHTTPHost(p.Hostname())
}

func (in *Installer) uploadArtifact(pluginName string, dist *DistArtifact) (*ct.Artifact, error) {
	httpClient := in.http()
	layerTmpl, extraMeta, err := in.uploadDistLayers(pluginName, dist)
	if err != nil {
		return nil, err
	}

	raw := dist.Artifact.RawManifest
	manifestID := dist.Artifact.Manifest().ID()
	if manifestID == "" && dist.Artifact.Hashes != nil {
		manifestID = dist.Artifact.Hashes["sha512_256"]
	}
	manifestURL := fmt.Sprintf("%s/images/%s.json", in.pluginObjectPrefix(pluginName), manifestID)
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
	for k, v := range extraMeta {
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

func (in *Installer) createApp(m *Manifest, resolved *Resolved) (*ct.App, error) {
	source, ref := "", ""
	if resolved != nil {
		source = resolved.StampSource()
		ref = resolved.Ref
	}
	app := &ct.App{
		Name:          m.App.Name,
		Strategy:      m.App.Strategy,
		DeployTimeout: m.App.DeployTimeout,
		Meta:          m.AnnotateInstall(m.AppMeta(), source, ref),
	}
	if err := in.Client.CreateApp(app); err != nil {
		return nil, fmt.Errorf("create app %s: %w", m.App.Name, err)
	}
	return app, nil
}

// pluginAppLookupNames prefers the current manifest app name so a renamed
// install (dashboard-plugin) wins over a leftover app still named dashboard.
func pluginAppLookupNames(m *Manifest) []string {
	if m == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	add(m.App.Name)
	for _, name := range m.aliasNames() {
		add(name)
	}
	return out
}

// acceptPluginApp is true for apps installed by plugin:install. GetApp("postgres")
// hits the platform appliance on a new cluster; that must not count as the
// tenant postgres plugin (postgres-plugin).
func acceptPluginApp(app *ct.App) *ct.App {
	if app == nil || !app.Plugin() {
		return nil
	}
	return app
}

func (in *Installer) getInstalledPluginApp(m *Manifest) (*ct.App, error) {
	if in == nil || in.Client == nil || m == nil {
		return nil, controller.ErrNotFound
	}
	var lastErr error
	for _, name := range pluginAppLookupNames(m) {
		app, err := in.Client.GetApp(name)
		if err == nil && acceptPluginApp(app) != nil {
			return app, nil
		}
		if err != nil && err != controller.ErrNotFound {
			return nil, err
		}
		lastErr = err
	}
	apps, err := in.Client.AppList()
	if err != nil {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, err
	}
	for _, name := range pluginAppLookupNames(m) {
		app, err := LookupPluginApp(apps, name)
		if err == nil && app != nil {
			return app, nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, controller.ErrNotFound
}

type pluginDeploy struct {
	prev     *ct.Release
	prevForm *ct.Formation
	timeout  time.Duration
}

// pluginScaleNoWait is true when install already polls PingURL. Waiting on
// ScaleAppRelease's event stream then burns deployTimeout (5m) with no extra
// logs if the job never becomes healthy.
func pluginScaleNoWait(m *Manifest) bool {
	return m != nil && strings.TrimSpace(m.PingURL()) != ""
}

func (in *Installer) restorePreviousRelease(app *ct.App, dep *pluginDeploy) {
	if in == nil || in.Client == nil || app == nil || dep == nil || dep.prev == nil || dep.prevForm == nil || len(dep.prevForm.Processes) == 0 {
		return
	}
	in.logf("restoring previous %s release %s after failed readiness", app.Name, dep.prev.ID)
	timeout := dep.timeout
	_ = in.Client.ScaleAppRelease(app.ID, dep.prev.ID, ct.ScaleOptions{
		Processes: dep.prevForm.Processes,
		Timeout:   &timeout,
		NoWait:    true,
	})
	_ = in.Client.SetAppRelease(app.ID, dep.prev.ID)
}

func (in *Installer) deployRelease(app *ct.App, m *Manifest, image *ct.Artifact, cluster map[string]string) (*pluginDeploy, error) {
	env := ReleaseEnv(m, image.ID, cluster)
	var prev *ct.Release
	if p, err := in.Client.GetAppRelease(app.ID); err == nil && p != nil {
		prev = p
		PreserveGeneratedEnv(m, env, prev.Env)
		PreservePreviousEnv(env, prev.Env)
	}
	EnsureClusterAuthEnv(env, cluster)
	release := &ct.Release{
		ArtifactIDs: []string{image.ID},
		Env:         env,
		Processes:   m.App.Processes,
	}
	in.logf("creating %s release", app.Name)
	if err := in.Client.CreateRelease(app.ID, release); err != nil {
		return nil, fmt.Errorf("create release: %w", err)
	}

	procs := FormationScale(m, cluster)
	timeout := deployTimeout
	if m.App.DeployTimeout > 0 {
		timeout = time.Duration(m.App.DeployTimeout) * time.Second
	}
	dep := &pluginDeploy{prev: prev, timeout: timeout}

	if prev != nil && prev.ID != release.ID {
		if f, err := in.Client.GetFormation(app.ID, prev.ID); err == nil {
			dep.prevForm = f
		}
		if zeros := previousReleaseScaleDown(prev, dep.prevForm); len(zeros) > 0 {
			in.logf("stopping previous %s release %s so the new job can be placed", app.Name, prev.ID)
			if err := in.Client.ScaleAppRelease(app.ID, prev.ID, ct.ScaleOptions{
				Processes: zeros,
				Timeout:   &timeout,
			}); err != nil {
				return dep, fmt.Errorf("scale down previous %s release: %w", app.Name, err)
			}
		}
	}

	scaleOpts := ct.ScaleOptions{
		Processes: procs,
		Timeout:   &timeout,
		NoWait:    pluginScaleNoWait(m),
	}
	if scaleOpts.NoWait {
		in.logf("scaling %s %v (readiness via wait URL)", app.Name, procs)
	} else {
		in.logf("waiting for %s formation %v (timeout %s)", app.Name, procs, timeout)
	}
	if err := in.Client.ScaleAppRelease(app.ID, release.ID, scaleOpts); err != nil {
		in.restorePreviousRelease(app, dep)
		return dep, fmt.Errorf("scale %s: %w", app.Name, err)
	}
	if err := in.Client.SetAppRelease(app.ID, release.ID); err != nil {
		return dep, fmt.Errorf("set release: %w", err)
	}
	for k, v := range env {
		if v != "" {
			cluster[k] = v
		}
	}
	return dep, nil
}

func (in *Installer) runHook(root string, m *Manifest, rel string, cluster map[string]string) error {
	if rel == "" {
		return nil
	}
	script := filepath.Join(root, rel)
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("hook %s: %w", rel, err)
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
		"FLYNN_PLUGIN_APP="+m.App.Name,
		"FLYNN_PLUGIN_ROOT="+root,
	)
	for k, v := range cluster {
		if k == "" || v == "" {
			continue
		}
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	in.logf("running hook %s", rel)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hook %s: %w", rel, err)
	}
	return nil
}

func (in *Installer) provisionResources(app *ct.App, m *Manifest, cluster map[string]string) error {
	if m == nil || len(m.Resources) == 0 || in.Client == nil || app == nil {
		return nil
	}
	existing, err := in.Client.AppResourceList(app.ID)
	if err != nil && err != controller.ErrNotFound {
		return fmt.Errorf("list resources for %s: %w", app.Name, err)
	}
	aliases := providerAliases(in.Client)
	return applyProvisionedResources(existing, aliases, m.Resources, cluster, func(name string) (*ct.Resource, error) {
		return in.provisionResourceWithRetry(app, name)
	}, func(name string) {
		in.logf("resource %s already attached to %s", name, app.Name)
	})
}

// pluginResourceProvider maps a manifest resource name to the controller
// provider. Core Flynn plugins attach the built-in appliance
// (platform-postgres / postgres-api.discoverd), not the tenant postgres plugin.
// "postgres" is accepted as an alias so older manifests still hit the appliance.
func pluginResourceProvider(name string) string {
	switch strings.TrimSpace(name) {
	case "postgres", "platform-postgres":
		return "platform-postgres"
	default:
		return name
	}
}

func (in *Installer) provisionResourceWithRetry(app *ct.App, name string) (*ct.Resource, error) {
	var res *ct.Resource
	err := provisionResourceAttempts.RunWithValidator(func() error {
		provider := pluginResourceProvider(name)
		in.logf("provisioning %s resource for %s", provider, app.Name)
		r, err := in.Client.ProvisionResource(&ct.ResourceReq{
			ProviderID: provider,
			Apps:       []string{app.ID},
		})
		if err != nil {
			if retryableResourceProvision(err) {
				in.logf("provision %s: %v; retrying", name, err)
			}
			return err
		}
		res = r
		return nil
	}, retryableResourceProvision)
	return res, err
}

// retryableResourceProvision retries collapsed controller/postgres-api 500s
// ("unknown_error: Something went wrong") and explicit retry JSON errors.
func retryableResourceProvision(err error) bool {
	if err == nil {
		return false
	}
	if httphelper.IsRetryableError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "not found") || strings.Contains(msg, "validation") {
		return false
	}
	return strings.Contains(msg, "unknown_error") ||
		strings.Contains(msg, "something went wrong") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "no such host")
}

// applyProvisionedResources attaches missing providers. A leftover DATABASE_URL
// in cluster env is not proof a resource is still attached: uninstall can drop
// the Postgres role while PreservePreviousEnv keeps the dead URL.
func applyProvisionedResources(
	existing []*ct.Resource,
	aliases map[string]string,
	wanted []string,
	cluster map[string]string,
	provision func(name string) (*ct.Resource, error),
	already func(name string),
) error {
	have := map[string]bool{}
	for _, res := range existing {
		if res == nil {
			continue
		}
		for _, key := range resourceProviderKeys(res) {
			have[key] = true
		}
		mergeResourceEnv(cluster, res)
	}
	for _, name := range wanted {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if providerResourceAttached(have, name, aliases) {
			if already != nil {
				already(name)
			}
			continue
		}
		if provision == nil {
			return fmt.Errorf("provision %s: missing provisioner", name)
		}
		res, err := provision(name)
		if err != nil {
			return fmt.Errorf("provision %s: %w", name, err)
		}
		for _, key := range resourceProviderKeys(res) {
			have[key] = true
		}
		have[name] = true
		mergeResourceEnv(cluster, res)
	}
	return nil
}

func mergeResourceEnv(cluster map[string]string, res *ct.Resource) {
	if cluster == nil || res == nil {
		return
	}
	for k, v := range res.Env {
		if v != "" {
			cluster[k] = v
		}
	}
}

func providerAliases(client providerClient) map[string]string {
	out := map[string]string{}
	if client == nil {
		return out
	}
	list, err := client.ProviderList()
	if err != nil {
		return out
	}
	for _, p := range list {
		if p == nil || strings.TrimSpace(p.Name) == "" {
			continue
		}
		out[p.Name] = p.ID
		if p.ID != "" {
			out[p.ID] = p.ID
		}
	}
	return out
}

func resourceProviderKeys(res *ct.Resource) []string {
	if res == nil {
		return nil
	}
	var keys []string
	if res.ProviderID != "" {
		keys = append(keys, res.ProviderID)
	}
	if v := strings.TrimSpace(res.Env["FLYNN_POSTGRES"]); v != "" {
		keys = append(keys, v)
	}
	return keys
}

func providerResourceAttached(have map[string]bool, name string, aliases map[string]string) bool {
	want := pluginResourceProvider(name)
	if have[want] {
		return true
	}
	if id := aliases[want]; id != "" && have[id] {
		return true
	}
	if want == "platform-postgres" {
		// Appliance databases set FLYNN_POSTGRES=postgres (the postgres app).
		// The tenant plugin provider is also named postgres; do not treat
		// aliases["postgres"] as the appliance.
		return have["postgres"]
	}
	if have[name] {
		return true
	}
	if id := aliases[name]; id != "" && have[id] {
		return true
	}
	return false
}

func (in *Installer) routeAPI() RouteClient {
	if in.RouteClient != nil {
		return in.RouteClient
	}
	if in.Client != nil {
		return in.Client
	}
	return nil
}

func hasHTTPRoute(m *Manifest) bool {
	if m == nil {
		return false
	}
	for _, spec := range m.Routes {
		typ := spec.Type
		if typ == "" {
			typ = "http"
		}
		if typ == "http" {
			return true
		}
	}
	return false
}

func routeKey(typ, domain, service string) string {
	return typ + "/" + domain + "/" + service
}

func routeDomainKey(typ, domain string) string {
	return typ + "/" + domain
}

func existingHTTPRoute(have map[string]*router.Route, haveDomain map[string]*router.Route, typ, domain, service string) *router.Route {
	if prev := have[routeKey(typ, domain, service)]; prev != nil {
		return prev
	}
	return haveDomain[routeDomainKey(typ, domain)]
}

func routeHasAutoTLS(r *router.Route) bool {
	return r != nil && r.ManagedCertificateDomain != nil && strings.TrimSpace(*r.ManagedCertificateDomain) != ""
}

func (in *Installer) acmeEnabled() (bool, error) {
	api := in.routeAPI()
	if api == nil {
		return false, fmt.Errorf("missing controller client")
	}
	cfg, err := api.GetACMEConfig()
	if err != nil {
		return false, err
	}
	return cfg != nil && cfg.Enabled, nil
}

func (in *Installer) attachAutoTLS(route *router.Route, require bool) error {
	if route == nil || route.Domain == "" {
		return nil
	}
	if route.Type != "http" && route.Type != "tcp" {
		return nil
	}
	if route.Type == "tcp" && router.NormalizeTLSMode(route.TLSMode) == router.TLSModePassthrough {
		return nil
	}
	if routeHasAutoTLS(route) {
		return nil
	}
	enabled, err := in.acmeEnabled()
	if err != nil {
		return fmt.Errorf("check ACME: %w", err)
	}
	if !enabled {
		if require {
			return fmt.Errorf("ACME/Let's Encrypt is not enabled for this cluster.\nRun 'flynn-host acme:configure --email=<email> --agree-tos' first")
		}
		in.logf("skipping auto TLS for %s (ACME is not enabled; run flynn-host acme:configure --email=<email> --agree-tos)", route.Domain)
		return nil
	}
	domain := route.Domain
	route.ManagedCertificateDomain = &domain
	route.Certificate = nil
	route.LegacyTLSCert = ""
	route.LegacyTLSKey = ""
	if route.Type == "tcp" {
		route.TLSMode = router.TLSModeTerminate
	}
	in.logf("enabling auto TLS for %s", domain)
	return nil
}

func (in *Installer) ensureRoutes(app *ct.App, m *Manifest, cluster map[string]string, installAutoTLS bool) error {
	if m == nil || len(m.Routes) == 0 || app == nil {
		return nil
	}
	api := in.routeAPI()
	if api == nil {
		return nil
	}
	acmeOn := false
	if hasHTTPRoute(m) || installAutoTLS {
		on, err := in.acmeEnabled()
		if installAutoTLS {
			if err != nil {
				return fmt.Errorf("check ACME: %w", err)
			}
			if !on {
				return fmt.Errorf("ACME/Let's Encrypt is not enabled for this cluster.\nRun 'flynn-host acme:configure --email=<email> --agree-tos' first")
			}
		}
		if err == nil {
			acmeOn = on
		}
	}
	existing, err := api.AppRouteList(app.ID)
	if err != nil && err != controller.ErrNotFound {
		return fmt.Errorf("list routes for %s: %w", app.Name, err)
	}
	have := map[string]*router.Route{}
	haveDomain := map[string]*router.Route{}
	for _, r := range existing {
		if r == nil {
			continue
		}
		have[routeKey(r.Type, r.Domain, r.Service)] = r
		haveDomain[routeDomainKey(r.Type, r.Domain)] = r
	}
	for _, spec := range m.Routes {
		typ := spec.Type
		if typ == "" {
			typ = "http"
		}
		domain := ExpandClusterVars(spec.Domain, cluster)
		wantTLS := typ == "http" && (spec.AutoTLS || installAutoTLS || acmeOn)
		if prev := existingHTTPRoute(have, haveDomain, typ, domain, spec.Service); prev != nil {
			if !wantTLS {
				in.logf("route %s %s already exists", typ, domain)
				continue
			}
			if err := in.attachAutoTLS(prev, installAutoTLS); err != nil {
				return err
			}
			if !routeHasAutoTLS(prev) {
				in.logf("route %s %s already exists", typ, domain)
				continue
			}
			// Re-touch so a failed or stuck-pending ACME cert is queued again.
			in.logf("updating %s route %s with auto TLS", typ, domain)
			if err := api.UpdateRoute(app.ID, prev.FormattedID(), prev); err != nil {
				return fmt.Errorf("update route %s: %w", domain, err)
			}
			continue
		}
		in.logf("adding %s route %s -> %s", typ, domain, spec.Service)
		route := &router.Route{
			Type:          typ,
			Domain:        domain,
			Service:       spec.Service,
			Leader:        spec.Leader,
			DrainBackends: true,
		}
		if wantTLS {
			if err := in.attachAutoTLS(route, installAutoTLS); err != nil {
				return err
			}
		}
		if err := api.CreateRoute(app.ID, route); err != nil {
			return fmt.Errorf("create route %s: %w", domain, err)
		}
	}
	return nil
}

func (in *Installer) ensureWebhooks(m *Manifest, cluster map[string]string) error {
	if m == nil || len(m.Webhooks) == 0 {
		return nil
	}
	hosts, err := in.webhookHosts()
	if err != nil {
		return fmt.Errorf("list hosts for plugin webhooks: %w", err)
	}
	if len(hosts) == 0 {
		return fmt.Errorf("no flynn-host members to register plugin webhooks")
	}
	for i, spec := range m.Webhooks {
		url, headers, err := expandWebhookSpec(spec, cluster)
		if err != nil {
			return fmt.Errorf("webhooks[%d]: %w", i, err)
		}
		id := pluginWebhookID(m.Name, url)
		in.logf("registering host webhook %s", url)
		for _, h := range hosts {
			if skip, err := webhookAlreadyRegistered(h, id, url); err != nil {
				return fmt.Errorf("list webhooks on %s: %w", h.ID(), err)
			} else if skip {
				in.logf("webhook %s already on %s", url, h.ID())
				continue
			}
			if _, err := h.AddWebhook(id, url, headers); err != nil {
				return fmt.Errorf("webhook on %s: %w", h.ID(), err)
			}
		}
	}
	return nil
}

func expandWebhookSpec(spec WebhookSpec, cluster map[string]string) (string, map[string]string, error) {
	url := ExpandClusterVars(strings.TrimSpace(spec.URL), cluster)
	if url == "" {
		return "", nil, fmt.Errorf("url expanded empty")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", nil, fmt.Errorf("url %q must be http(s)", url)
	}
	headers := map[string]string{}
	for k, v := range spec.Headers {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		headers[k] = ExpandClusterVars(v, cluster)
	}
	if key := strings.TrimSpace(spec.SecretEnv); key != "" {
		secret := cluster[key]
		if secret == "" {
			return "", nil, fmt.Errorf("secret_env %s is empty", key)
		}
		if _, ok := headers["X-Flynn-Webhook-Secret"]; !ok {
			headers["X-Flynn-Webhook-Secret"] = secret
		}
	}
	if len(headers) == 0 {
		headers = nil
	}
	return url, headers, nil
}

func pluginWebhookName(pluginName string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		default:
			return '-'
		}
	}, pluginName)
}

func pluginWebhookID(pluginName, url string) string {
	name := pluginWebhookName(pluginName)
	sum := sha256.Sum256([]byte(pluginName + "\n" + url))
	return fmt.Sprintf("plugin-%s-%x", name, sum[:8])
}

func pluginWebhookIDPrefix(pluginName string) string {
	return "plugin-" + pluginWebhookName(pluginName) + "-"
}

func webhookAlreadyRegistered(h WebhookHost, id, url string) (bool, error) {
	existing, err := h.ListWebhooks()
	if err != nil {
		return false, err
	}
	for _, wh := range existing {
		if wh == nil {
			continue
		}
		if wh.URL == url && wh.ID != id {
			return true, nil
		}
	}
	return false, nil
}

func (in *Installer) webhookHosts() ([]WebhookHost, error) {
	if in.Hosts != nil {
		return in.Hosts()
	}
	hs, err := cluster.NewClient().Hosts()
	if err != nil {
		return nil, err
	}
	out := make([]WebhookHost, len(hs))
	for i, h := range hs {
		out[i] = h
	}
	return out, nil
}

func (in *Installer) http() *http.Client {
	if in.HTTP != nil {
		return in.HTTP
	}
	return http.DefaultClient
}

func ensureProvider(client providerClient, name, url string, tenantSafe bool) error {
	providers, err := client.ProviderList()
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}
	for _, p := range providers {
		if p.Name == name {
			return nil
		}
	}
	if err := client.CreateProvider(&ct.Provider{Name: name, URL: url, TenantSafe: tenantSafe}); err != nil {
		return fmt.Errorf("create provider %s: %w", name, err)
	}
	return nil
}

func blobstoreHasObject(httpClient *http.Client, rawURL string, size int64) bool {
	if rawURL == "" {
		return false
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	code, ok := blobstoreHeadMatches(httpClient, rawURL, size)
	if ok {
		return true
	}
	switch code {
	case 0, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return blobstoreGetMatches(httpClient, rawURL, size)
	default:
		return false
	}
}

func blobstoreHeadMatches(httpClient *http.Client, rawURL string, size int64) (int, bool) {
	req, err := http.NewRequest(http.MethodHead, rawURL, nil)
	if err != nil {
		return 0, false
	}
	blobstoreauth.ApplyIfBlobstore(req)
	res, err := httpClient.Do(req)
	if err != nil {
		return 0, false
	}
	defer res.Body.Close()
	return res.StatusCode, blobstoreStatusMatches(res, size)
}

func blobstoreGetMatches(httpClient *http.Client, rawURL string, size int64) bool {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Range", "bytes=0-0")
	blobstoreauth.ApplyIfBlobstore(req)
	res, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	io.Copy(io.Discard, io.LimitReader(res.Body, 64))
	res.Body.Close()
	if res.StatusCode == http.StatusPartialContent {
		if n := contentRangeTotal(res.Header.Get("Content-Range")); n >= 0 {
			return size <= 0 || n == size
		}
	}
	return blobstoreStatusMatches(res, size)
}

func blobstoreStatusMatches(res *http.Response, size int64) bool {
	if res == nil || res.StatusCode != http.StatusOK {
		return false
	}
	n := res.ContentLength
	if n < 0 {
		if s := res.Header.Get("Content-Length"); s != "" {
			if parsed, err := strconv.ParseInt(s, 10, 64); err == nil {
				n = parsed
			}
		}
	}
	if size > 0 && n >= 0 && n != size {
		return false
	}
	if size > 0 && n < 0 {
		return false
	}
	return true
}

func contentRangeTotal(v string) int64 {
	// bytes 0-0/211136512
	i := strings.LastIndex(v, "/")
	if i < 0 || i == len(v)-1 {
		return -1
	}
	n, err := strconv.ParseInt(v[i+1:], 10, 64)
	if err != nil {
		return -1
	}
	return n
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
	blobstoreauth.ApplyIfBlobstore(req)
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
	blobstoreauth.ApplyIfBlobstore(req)
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

func waitHTTP(httpClient *http.Client, rawURL string, timeout time.Duration) error {
	return waitHTTPLog(httpClient, rawURL, timeout, nil)
}

func waitHTTPLog(httpClient *http.Client, rawURL string, timeout time.Duration, logf func(string, ...interface{})) error {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	deadline := time.Now().Add(timeout)
	var last error
	var lastLog time.Time
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		probe := waitHTTPProbeTimeout
		if remaining := time.Until(deadline); remaining < probe {
			probe = remaining
		}
		if probe <= 0 {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), probe)
		// Close each probe so keep-alive cannot pin a draining discoverd IP.
		req = req.WithContext(ctx)
		req.Close = true
		res, err := httpClient.Do(req)
		cancel()
		if err == nil {
			res.Body.Close()
			if res.StatusCode >= 200 && res.StatusCode < 300 {
				return nil
			}
			last = fmt.Errorf("status %s", res.Status)
		} else {
			last = err
		}
		if logf != nil && (lastLog.IsZero() || time.Since(lastLog) >= waitHTTPLogInterval) {
			lastLog = time.Now()
			logf("still waiting for %s: %v", rawURL, last)
		}
		time.Sleep(waitHTTPRetryDelay)
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
