package plugin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestManifestValidateKinds(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name": "dashboard",
		"kind": "app",
		"app": map[string]interface{}{
			"name": "dashboard",
			"processes": map[string]interface{}{
				"web": map[string]interface{}{
					"args":  []string{"/bin/dashboard"},
					"ports": []map[string]interface{}{{"port": 80, "proto": "tcp"}},
				},
			},
		},
	})
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != KindApp || m.App.Name != "dashboard" {
		t.Fatalf("got kind=%s name=%s", m.Kind, m.App.Name)
	}
	if m.PingURL() != "" {
		t.Fatalf("app plugin should not default a ping URL: %s", m.PingURL())
	}

	dir = t.TempDir()
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name": "cache",
		"kind": "resource-provider",
		"provider": map[string]string{
			"name": "cache",
			"url":  "http://cache-api.discoverd/clusters",
		},
		"app": map[string]interface{}{
			"processes": map[string]interface{}{
				"web": map[string]interface{}{"args": []string{"/bin/api"}},
			},
		},
		"image_env":  map[string]string{"CACHE_IMAGE_ID": "self"},
		"inject_env": []string{"CONTROLLER_KEY", "SINGLETON"},
		"cli": map[string]interface{}{
			"command":     "cache",
			"usage":       "manage cache",
			"subcommands": []string{"cli"},
		},
	})
	m, err = LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.App.Name != "cache" {
		t.Fatalf("app.name should default to plugin name, got %q", m.App.Name)
	}
	if got := m.PingURL(); got != "http://cache-api.discoverd/ping" {
		t.Fatalf("PingURL=%q", got)
	}
	meta := m.AppMeta()
	if meta[MetaPlugin] != "true" || meta[MetaPluginKind] != KindResourceProvider {
		t.Fatalf("meta=%v", meta)
	}
	if meta[MetaPluginCLI] == "" {
		t.Fatal("expected flynn-plugin-cli meta")
	}

	dir = t.TempDir()
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name": "bad",
		"kind": "resource-provider",
		"app": map[string]interface{}{
			"processes": map[string]interface{}{"web": map[string]interface{}{"args": []string{"/bin/x"}}},
		},
	})
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("resource-provider without provider must fail")
	}

	dir = t.TempDir()
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name": "scheduler",
		"kind": "scheduler",
		"app": map[string]interface{}{
			"name": "scheduler",
			"processes": map[string]interface{}{
				"web": map[string]interface{}{
					"args":  []string{"/bin/scheduler"},
					"ports": []map[string]interface{}{{"port": 80, "proto": "tcp"}},
				},
			},
		},
		"cli": map[string]interface{}{
			"command": "scheduler",
			"usage":   "manage scheduled jobs",
		},
	})
	m, err = LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != KindScheduler {
		t.Fatalf("got kind=%s", m.Kind)
	}
	if !m.CLI.UserVisible(m.Kind) {
		t.Fatal("scheduler CLI must be user-visible")
	}
}

func TestManifestDashboardContract(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name": "cache",
		"kind": "resource-provider",
		"provider": map[string]string{
			"name": "cache",
			"url":  "http://cache-api.discoverd/clusters",
		},
		"app": map[string]interface{}{
			"processes": map[string]interface{}{
				"web": map[string]interface{}{"args": []string{"/bin/api"}},
			},
		},
		"dashboard": map[string]interface{}{
			"base_url": "http://cache.discoverd/dashboard",
			"surfaces": []string{"app.resources"},
			"card": map[string]string{
				"title":       "Cache",
				"description": "In-memory cache",
				"icon":        "redis",
			},
			"routes": []map[string]string{
				{"path": "/", "title": "Overview"},
				{"path": "/metrics", "title": "Metrics"},
			},
		},
	})
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Dashboard == nil || m.Dashboard.BaseURL != "http://cache.discoverd/dashboard" {
		t.Fatalf("dashboard: %+v", m.Dashboard)
	}
	meta := m.AppMeta()
	if meta[MetaPluginDashboard] == "" {
		t.Fatal("expected flynn-plugin-dashboard meta")
	}
	if rec := m.Record(); rec.Dashboard == nil || rec.Dashboard.Card == nil || rec.Dashboard.Card.Title != "Cache" {
		t.Fatalf("record dashboard: %+v", rec.Dashboard)
	}
	got := DashboardFromApp(&ct.App{Meta: meta})
	if got == nil || got.BaseURL != "http://cache.discoverd/dashboard" || len(got.Routes) != 2 {
		t.Fatalf("DashboardFromApp: %+v", got)
	}

	dir = t.TempDir()
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name": "cache",
		"kind": "app",
		"app": map[string]interface{}{
			"processes": map[string]interface{}{"web": map[string]interface{}{"args": []string{"/bin/x"}}},
		},
		"dashboard": map[string]interface{}{"base_url": "not-a-url"},
	})
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("invalid dashboard.base_url must fail")
	}

	dir = t.TempDir()
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name": "cache",
		"kind": "app",
		"app": map[string]interface{}{
			"processes": map[string]interface{}{"web": map[string]interface{}{"args": []string{"/bin/x"}}},
		},
		"dashboard": map[string]interface{}{
			"base_url": "http://cache.discoverd/dashboard",
			"surfaces": []string{"not.a.surface"},
		},
	})
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("unknown dashboard surface must fail")
	}

	if DashboardFromApp(nil) != nil || DashboardFromApp(&ct.App{}) != nil {
		t.Fatal("empty dashboard meta")
	}
	if DashboardFromApp(&ct.App{Meta: map[string]string{MetaPluginDashboard: "{"}}) != nil {
		t.Fatal("invalid dashboard json")
	}
	fromRec := DashboardFromApp(&ct.App{Meta: map[string]string{
		MetaPluginRecord: `{"dashboard":{"base_url":"http://pg.discoverd/dashboard"}}`,
	}})
	if fromRec == nil || fromRec.BaseURL != "http://pg.discoverd/dashboard" {
		t.Fatalf("record fallback: %+v", fromRec)
	}
}

func TestDashboardWebScalePlacementRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name": "autoscale",
		"kind": "app",
		"app": map[string]interface{}{
			"processes": map[string]interface{}{"web": map[string]interface{}{"args": []string{"/bin/autoscale"}}},
		},
		"dashboard": map[string]interface{}{
			"base_url":  "http://autoscale.discoverd/dashboard",
			"surfaces":  []string{"app.resources", "app.scale"},
			"placement": "web-scale",
			"card": map[string]interface{}{
				"title":  "Autoscale",
				"hidden": true,
			},
		},
	})
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Dashboard == nil || m.Dashboard.Placement != "web-scale" || m.Dashboard.Card == nil || !m.Dashboard.Card.Hidden {
		t.Fatalf("manifest dashboard: %+v", m.Dashboard)
	}
	got := DashboardFromApp(&ct.App{Meta: m.AppMeta()})
	if got == nil || got.Placement != "web-scale" || got.Card == nil || !got.Card.Hidden {
		t.Fatalf("stamped dashboard dropped placement: %+v", got)
	}
	if len(got.Surfaces) != 2 || got.Surfaces[1] != DashboardSurfaceAppScale {
		t.Fatalf("surfaces: %+v", got.Surfaces)
	}
}

func TestLoadManifestWebhooks(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name": "widget",
		"kind": "app",
		"app": map[string]interface{}{
			"processes": map[string]interface{}{
				"web": map[string]interface{}{"args": []string{"/bin/widget"}},
			},
		},
		"webhooks": []map[string]interface{}{
			{
				"url":        "http://widget.discoverd/webhooks/flynn",
				"secret_env": "WEBHOOK_INGEST_SECRET",
			},
		},
	})
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Webhooks) != 1 || m.Webhooks[0].URL != "http://widget.discoverd/webhooks/flynn" || m.Webhooks[0].SecretEnv != "WEBHOOK_INGEST_SECRET" {
		t.Fatalf("%+v", m.Webhooks)
	}

	dir = t.TempDir()
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name": "widget",
		"kind": "app",
		"app": map[string]interface{}{
			"processes": map[string]interface{}{
				"web": map[string]interface{}{"args": []string{"/bin/widget"}},
			},
		},
		"webhooks": []map[string]interface{}{{"url": "not-a-url"}},
	})
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("non-http webhook url must fail")
	}
}

func TestReleaseEnvSelf(t *testing.T) {
	m := &Manifest{
		InjectEnv: []string{"CONTROLLER_KEY", "SINGLETON", "MISSING"},
		ImageEnv:  map[string]string{"WIDGET_IMAGE_ID": ImageSelf, "OTHER": "literal"},
		Env:       map[string]string{"FLYNN_WIDGET": "widget"},
	}
	env := ReleaseEnv(m, "artifact-uuid", map[string]string{
		"CONTROLLER_KEY": "secret",
		"SINGLETON":      "true",
	})
	if env["CONTROLLER_KEY"] != "secret" || env["SINGLETON"] != "true" {
		t.Fatalf("inject: %v", env)
	}
	if _, ok := env["MISSING"]; ok {
		t.Fatal("missing cluster keys must not be injected as empty")
	}
	if env["WIDGET_IMAGE_ID"] != "artifact-uuid" {
		t.Fatalf("self image env: %v", env)
	}
	if env["OTHER"] != "literal" || env["FLYNN_WIDGET"] != "widget" {
		t.Fatalf("static env: %v", env)
	}
}

func TestResolveSourceAliasAndPath(t *testing.T) {
	cwd := t.TempDir()
	pluginDir := filepath.Join(cwd, "flynn-plugin-widget")
	if err := os.Mkdir(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(pluginDir, ManifestName), map[string]interface{}{
		"name": "widget",
		"kind": "app",
		"app": map[string]interface{}{
			"processes": map[string]interface{}{"web": map[string]interface{}{"args": []string{"/bin/x"}}},
		},
	})

	got, err := ResolveSource(pluginDir, cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != pluginDir {
		t.Fatalf("abs path: got %s", got)
	}

	rel, err := ResolveSource("./flynn-plugin-widget", cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if rel != pluginDir {
		t.Fatalf("rel path: got %s want %s", rel, pluginDir)
	}

	cfg := filepath.Join(t.TempDir(), "plugins.json")
	writeJSON(t, cfg, map[string]string{"widget": pluginDir})
	aliased, err := ResolveSource("widget", cwd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if aliased != pluginDir {
		t.Fatalf("alias: got %s", aliased)
	}
}

func TestResolveSourceEmptyAndGitHubOnly(t *testing.T) {
	if _, err := ResolveSource("", t.TempDir(), ""); err == nil {
		t.Fatal("empty source must fail")
	}
	_, err := ResolveSource("https://github.com/acme/flynn-plugin-x.git", t.TempDir(), "")
	if err == nil {
		t.Fatal("GitHub-only source must not return a local dir")
	}
	if _, ok := err.(*NotFoundError); !ok {
		t.Fatalf("want NotFoundError, got %T %v", err, err)
	}
}

func TestDiscoverLocalPluginsSkipsInvalidManifest(t *testing.T) {
	root := t.TempDir()
	bad := filepath.Join(root, "flynn-plugin-bad")
	if err := os.Mkdir(bad, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, ManifestName), []byte(`{"name":"x","kind":"nope"}`), 0644); err != nil {
		t.Fatal(err)
	}
	got := DiscoverLocalPlugins(root)
	if len(got) != 0 {
		t.Fatalf("invalid kind must be skipped: %v", got)
	}
	if len(DiscoverLocalPlugins("")) != 0 {
		t.Fatal("empty root")
	}
}

func TestListInstalled(t *testing.T) {
	apps := []*ct.App{
		{Name: "postgres", Meta: map[string]string{"flynn-system-app": "true"}},
		{
			ID:   "app-1",
			Name: "widget",
			Meta: map[string]string{
				"flynn-plugin":        "true",
				"flynn-plugin-kind":   KindApp,
				"flynn-plugin-source": "../flynn-plugin-widget",
				"flynn-plugin-wait":   "http://widget.discoverd/ping",
				"flynn-plugin-cli":    `{"command":"widget"}`,
			},
		},
	}
	got := ListInstalled(apps)
	if len(got) != 1 || got[0].Name != "widget" || got[0].Source == "" || got[0].CLI == nil || got[0].CLI.Command != "widget" {
		t.Fatalf("ListInstalled=%+v", got)
	}
	if empty := ListInstalled(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("empty inventory should be [] not nil: %#v", empty)
	}
}

func TestCatalogFromPluginApps(t *testing.T) {
	cli := CLI{Command: "cache", Usage: "manage cache", Subcommands: []string{"cli"}}
	raw, err := json.Marshal(cli)
	if err != nil {
		t.Fatal(err)
	}
	dupRaw, err := json.Marshal(CLI{Command: "cache", Usage: "duplicate"})
	if err != nil {
		t.Fatal(err)
	}
	apps := []*ct.App{
		nil,
		{Name: "postgres", Meta: map[string]string{"flynn-system-app": "true"}},
		{Name: "silent", Meta: map[string]string{MetaPlugin: "true"}},
		{Name: "bad-cli", Meta: map[string]string{MetaPlugin: "true", MetaPluginCLI: "{"}},
		{Name: "empty-cmd", Meta: map[string]string{MetaPlugin: "true", MetaPluginCLI: `{"command":""}`}},
		{Name: "cache", Meta: map[string]string{MetaPlugin: "true", MetaPluginKind: KindResourceProvider, MetaPluginCLI: string(raw)}},
		{Name: "cache-dup", Meta: map[string]string{MetaPlugin: "true", MetaPluginKind: KindResourceProvider, MetaPluginCLI: string(dupRaw)}},
		{Name: "control-ui", Meta: map[string]string{
			MetaPlugin: "true", MetaPluginKind: KindApp,
			MetaPluginCLI: `{"command":"control-ui","usage":"cluster UI","doc":"usage: flynn control-ui","actions":[{"name":"route","flynn":"route"}]}`,
		}},
		{Name: "opt-in-app", Meta: map[string]string{
			MetaPlugin: "true", MetaPluginKind: KindApp,
			MetaPluginCLI: `{"command":"opt-in","usage":"opt-in app","user":true,"doc":"usage: flynn opt-in","actions":[{"name":"ping","args":["/bin/ping"]}]}`,
		}},
	}
	providers := []*ct.Provider{
		nil,
		{Name: ""},
		{Name: "cache"},
		{Name: "redis"},
	}
	cat := catalogFrom(apps, providers)
	if !cat.HasCommand("cache") || !cat.HasProvider("cache") {
		t.Fatalf("plugin CLI must be listed: %+v", cat.Commands)
	}
	if got := cat.Lookup("cache"); got == nil || got.App != "cache" {
		t.Fatalf("catalog must record plugin app name: %+v", got)
	}
	if !cat.HasCommand("redis") || !cat.HasProvider("redis") {
		t.Fatalf("provider without CLI must unlock flynn redis: %+v", cat.Commands)
	}
	if cat.HasCommand("silent") || cat.HasCommand("postgres") {
		t.Fatalf("non-CLI apps must not leak into catalog: %+v", cat.Commands)
	}
	if cat.HasCommand("control-ui") {
		t.Fatalf("kind: app system plugins must not appear on the user flynn CLI: %+v", cat.Commands)
	}
	if !cat.HasCommand("opt-in") {
		t.Fatalf("kind: app with cli.user must appear: %+v", cat.Commands)
	}
	n := 0
	for _, cmd := range cat.Commands {
		if cmd.Command == "cache" {
			n++
			if cmd.Usage != "manage cache" {
				t.Fatalf("plugin meta must win over provider/duplicate: %+v", cmd)
			}
		}
	}
	if n != 1 {
		t.Fatalf("cache listed %d times: %+v", n, cat.Commands)
	}
}

func TestCLIFromAppAndCoreCommands(t *testing.T) {
	if CLIFromApp(nil) != nil {
		t.Fatal("nil app")
	}
	if CLIFromApp(&ct.App{}) != nil {
		t.Fatal("missing meta")
	}
	if CLIFromApp(&ct.App{Meta: map[string]string{MetaPluginCLI: "{"}}) != nil {
		t.Fatal("invalid json")
	}
	got := CLIFromApp(&ct.App{Meta: map[string]string{MetaPluginCLI: `{"command":"redis","usage":"manage redis"}`}})
	if got == nil || got.Command != "redis" || got.Usage != "manage redis" {
		t.Fatalf("%+v", got)
	}
	if IsCorePluginCommand("redis") || IsCorePluginCommand("mysql") || IsCorePluginCommand("mongodb") || IsCorePluginCommand("kafka") || IsCorePluginCommand("clickhouse") || IsCorePluginCommand("ps") {
		t.Fatal("core plugin command set")
	}
	old := CorePluginCommands
	t.Cleanup(func() { CorePluginCommands = old })
	CorePluginCommands = []string{"legacy"}
	if !IsCorePluginCommand("legacy") || IsCorePluginCommand("other") {
		t.Fatal("temporary core plugin command")
	}
}

type stubCatalogClient struct {
	apps      []*ct.App
	providers []*ct.Provider
	appsErr   error
	provErr   error
}

func (s stubCatalogClient) AppList() ([]*ct.App, error) { return s.apps, s.appsErr }
func (s stubCatalogClient) ProviderList() ([]*ct.Provider, error) {
	return s.providers, s.provErr
}

func TestLoadCatalog(t *testing.T) {
	if _, err := LoadCatalog(stubCatalogClient{appsErr: errors.New("offline")}); err == nil {
		t.Fatal("AppList error must fail")
	}

	raw := `{"command":"redis","usage":"manage redis databases"}`
	apps := []*ct.App{
		{
			Name: "redis",
			Meta: map[string]string{MetaPlugin: "true", MetaPluginKind: KindResourceProvider, MetaPluginCLI: raw},
		},
		{
			Name: "legacy-cache",
			Meta: map[string]string{MetaPlugin: "true", MetaPluginCLI: `{"command":"legacy-cache","usage":"old plugin"}`},
		},
		{
			Name: "control-ui",
			Meta: map[string]string{MetaPlugin: "true", MetaPluginKind: KindApp, MetaPluginCLI: `{"command":"control-ui","usage":"cluster UI"}`},
		},
	}
	cat, err := LoadCatalog(stubCatalogClient{apps: apps, provErr: errors.New("no providers")})
	if err != nil || !cat.HasCommand("redis") || !cat.HasCommand("legacy-cache") {
		t.Fatalf("ProviderList error must still return app CLI: %+v %v", cat, err)
	}
	if cat.HasCommand("control-ui") {
		t.Fatalf("kind: app must stay off the user CLI: %+v", cat.Commands)
	}

	cat, err = LoadCatalog(stubCatalogClient{
		apps:      apps,
		providers: []*ct.Provider{{Name: "postgres"}},
	})
	if err != nil || !cat.HasCommand("redis") || !cat.HasProvider("postgres") || !cat.HasCommand("legacy-cache") {
		t.Fatalf("full catalog: %+v %v", cat, err)
	}
	if (*Catalog)(nil).HasProvider("redis") || (*Catalog)(nil).Lookup("redis") != nil {
		t.Fatal("nil catalog")
	}
}

func TestManifestValidateErrorsAndWait(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("missing manifest must fail")
	}
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{"kind": "app"})
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("missing name must fail")
	}

	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("invalid json must fail")
	}

	m := &Manifest{Name: "x", Kind: "service", App: AppSpec{Processes: map[string]ct.ProcessType{"web": {}}}}
	if err := m.Validate(); err == nil {
		t.Fatal("unknown kind must fail")
	}
	m.Kind = KindApp
	m.App.Processes = nil
	if err := m.Validate(); err == nil {
		t.Fatal("missing processes must fail")
	}

	m = &Manifest{
		Name: "cache",
		Kind: KindResourceProvider,
		Wait: "http://cache.discoverd/health",
		Provider: &Provider{
			Name: "cache",
			URL:  "http://cache-api.discoverd/clusters?x=1#frag",
		},
		App: AppSpec{Processes: map[string]ct.ProcessType{"web": {}}},
	}
	if got := m.PingURL(); got != "http://cache.discoverd/health" {
		t.Fatalf("wait must win: %s", got)
	}
	m.Wait = ""
	if got := m.PingURL(); got != "http://cache-api.discoverd/ping" {
		t.Fatalf("provider ping: %s", got)
	}
	m.Provider.URL = "://bad"
	if m.PingURL() != "" {
		t.Fatal("invalid provider URL must not invent a ping")
	}
	m.Wait = "file:///etc/passwd"
	if m.PingURL() != "" {
		t.Fatal("file wait URLs must not be used as install ping")
	}
	m.Wait = "javascript:alert(1)"
	if m.PingURL() != "" {
		t.Fatal("non-http wait URLs must not be used as install ping")
	}
	m.Wait = ""
	m.Provider.URL = "ftp://cache-api.discoverd/clusters"
	if m.PingURL() != "" {
		t.Fatal("non-http provider URLs must not invent a ping")
	}
	if (*Manifest)(nil).PingURL() != "" {
		t.Fatal("nil manifest ping")
	}

	if (&Manifest{GitHubRepo: " acme/plug "}).githubRepo() != "acme/plug" {
		t.Fatal("explicit github_repo")
	}
	if (&Manifest{Name: "redis"}).githubRepo() != "flynn-plugin-redis" {
		t.Fatal("default github repo")
	}
	if (&Manifest{}).githubRepo() != "" {
		t.Fatal("empty github repo")
	}
	if (&Manifest{Name: "x", Kind: KindApp, App: AppSpec{Processes: map[string]ct.ProcessType{"web": {}}}}).sireniaOptional() {
		t.Fatal("non-sirenia is not optional")
	}

	meta := m.AnnotateInstall(nil, "../flynn-plugin-cache", "v1")
	if meta[MetaPluginSource] != "../flynn-plugin-cache" || meta[MetaPluginRef] != "v1" {
		t.Fatalf("annotate: %v", meta)
	}
}

func TestSingletonWebCount(t *testing.T) {
	if SingletonWebCount(map[string]string{"SINGLETON": "true"}) != 1 {
		t.Fatal("singleton cluster")
	}
	if SingletonWebCount(map[string]string{"SINGLETON": "TRUE"}) != 1 {
		t.Fatal("case insensitive")
	}
	if SingletonWebCount(nil) != 2 || SingletonWebCount(map[string]string{"SINGLETON": "false"}) != 2 {
		t.Fatal("ha cluster")
	}
}

func TestListInstalledSkipsNilAndNonPlugins(t *testing.T) {
	got := ListInstalled([]*ct.App{
		nil,
		{Name: "postgres", Meta: map[string]string{"flynn-system-app": "true"}},
		{
			ID:   "app-2",
			Name: "redis",
			Meta: map[string]string{
				MetaPlugin:       "true",
				MetaPluginKind:   KindResourceProvider,
				MetaPluginSource: "redis",
				MetaPluginRef:    "v1",
				MetaPluginWait:   "http://redis-api.discoverd/ping",
			},
		},
	})
	if len(got) != 1 || got[0].Name != "redis" || got[0].Kind != KindResourceProvider || got[0].Ref != "v1" {
		t.Fatalf("%+v", got)
	}
}

func TestAnnotateInstallRefreshesCLI(t *testing.T) {
	m := &Manifest{
		Name: "kafka",
		Kind: KindResourceProvider,
		App: AppSpec{
			Name: "kafka",
			Processes: map[string]ct.ProcessType{
				"web": {Args: []string{"/bin/x"}},
			},
		},
		Provider: &Provider{Name: "kafka", URL: "http://kafka-api.discoverd/clusters"},
		CLI: &CLI{
			Command: "kafka",
			Actions: []CLIAction{{
				Name: "topics",
				Env:  map[string]string{"KAFKA_BOOTSTRAP_SERVERS": "${app.KAFKA_BOOTSTRAP_SERVERS|leader.${resource}.discoverd:9092}"},
			}},
		},
	}
	stale := map[string]string{
		MetaPlugin:    "true",
		MetaPluginCLI: `{"command":"kafka","actions":[{"name":"topics"}]}`,
	}
	got := m.AnnotateInstall(stale, "/opt/flynn-plugins/flynn-plugin-kafka", "")
	cli := CLIFromApp(&ct.App{Meta: got})
	if cli == nil || len(cli.Actions) != 1 || cli.Actions[0].Env["KAFKA_BOOTSTRAP_SERVERS"] == "" {
		t.Fatalf("reinstall must refresh CLI catalog, got %+v", cli)
	}
}

func TestCatalogNilSafe(t *testing.T) {
	var cat *Catalog
	if cat.HasCommand("redis") || cat.HasProvider("redis") || cat.Lookup("redis") != nil {
		t.Fatal("nil catalog must not panic or report plugins")
	}
	empty := &Catalog{}
	if empty.HasCommand("redis") || empty.HasProvider("redis") {
		t.Fatal("empty catalog")
	}
}

func writeJSON(t *testing.T, path string, v interface{}) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}
