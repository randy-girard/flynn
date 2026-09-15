package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
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
		{Name: "cache", Meta: map[string]string{MetaPlugin: "true", MetaPluginCLI: string(raw)}},
		{Name: "cache-dup", Meta: map[string]string{MetaPlugin: "true", MetaPluginCLI: string(dupRaw)}},
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
	if IsCorePluginCommand("redis") || IsCorePluginCommand("mysql") || IsCorePluginCommand("mongodb") || !IsCorePluginCommand("kafka") || IsCorePluginCommand("ps") {
		t.Fatal("core plugin command set")
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
