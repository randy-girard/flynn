package plugin

import (
	"strings"
	"testing"
)

func TestOfficialCatalogMapsShortNames(t *testing.T) {
	t.Setenv(EnvGitHubOrg, "")
	t.Setenv(EnvFlynnRepo, "")
	t.Setenv(EnvPluginRepoRoot, t.TempDir())
	t.Setenv(EnvInstalledFile, t.TempDir()+"/none.json")

	plugins := KnownPlugins()
	if len(plugins) == 0 {
		t.Fatal("official catalog is empty")
	}
	got := map[string]KnownPlugin{}
	for _, p := range plugins {
		got[p.Name] = p
		for _, a := range p.Aliases {
			got[a] = p
		}
	}
	for _, name := range []string{"redis", "mariadb", "mysql", "mongodb", "kafka", "clickhouse", "dashboard", "www", "discovery", "otel", "opentelemetry", "scheduler"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("official catalog missing %s", name)
		}
	}
	if got["mysql"].Name != "mariadb" || got["mysql"].Repo != "flynn-plugin-mariadb" {
		t.Fatalf("mysql must resolve to mariadb: %+v", got["mysql"])
	}
	if _, ok := got["example"]; ok {
		t.Fatal("template plugin must not be in the official catalog")
	}

	cfg, err := LoadConfig(t.TempDir() + "/missing.json")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubURL("mysql") != "https://github.com/randy-girard/flynn-plugin-mariadb.git" {
		t.Fatalf("mysql: %s", cfg.GitHubURL("mysql"))
	}
	if cfg.GitHubURL("otel") != "https://github.com/randy-girard/flynn-plugin-otel.git" {
		t.Fatalf("otel: %s", cfg.GitHubURL("otel"))
	}
	if cfg.GitHubURL("scheduler") != "https://github.com/randy-girard/flynn-plugin-scheduler.git" {
		t.Fatalf("scheduler: %s", cfg.GitHubURL("scheduler"))
	}

	r, err := Resolve(InstallOptions{Source: "mysql"})
	if err != nil {
		t.Fatal(err)
	}
	if r.GitHub == nil || r.GitHub.Owner != "randy-girard" || r.GitHub.Repo != "flynn-plugin-mariadb" {
		t.Fatalf("resolve mysql: %+v", r.GitHub)
	}

	over, err := Resolve(InstallOptions{Source: "mysql", GitHubOrg: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if over.GitHub == nil || over.GitHub.Owner != "acme" || over.GitHub.Repo != "flynn-plugin-mariadb" {
		t.Fatalf("--github-org must override catalog owner: %+v", over.GitHub)
	}
}

func TestOfficialCatalogOverrideAndInstalledRepo(t *testing.T) {
	t.Setenv(EnvGitHubOrg, "")
	t.Setenv(EnvFlynnRepo, "")
	t.Setenv(EnvPluginRepoRoot, t.TempDir())
	inst := t.TempDir() + "/installed.json"
	t.Setenv(EnvInstalledFile, inst)
	if err := WriteInstalled(inst, []Installed{{
		Name:       "redis",
		GitHubRepo: "acme/custom-redis",
	}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(t.TempDir() + "/missing.json")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubURL("redis") != "https://github.com/acme/custom-redis.git" {
		t.Fatalf("installed github_repo must win over catalog: %s", cfg.GitHubURL("redis"))
	}

	path := t.TempDir() + "/plugins.json"
	writeJSON(t, path, map[string]interface{}{
		"mysql": map[string]string{"url": "https://github.com/other/fork-mariadb.git"},
	})
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubURL("mysql") != "https://github.com/other/fork-mariadb.git" {
		t.Fatalf("plugins.json must win: %s", cfg.GitHubURL("mysql"))
	}
}

func TestParseOfficialRejectsBadCatalog(t *testing.T) {
	if _, err := parseOfficial([]byte(`{`)); err == nil {
		t.Fatal("invalid json")
	}
	if _, err := parseOfficial([]byte(`{"plugins":[{"name":"x","kind":"app","repo":"r"}]}`)); err == nil {
		t.Fatal("missing description")
	}
	if _, err := parseOfficial([]byte(`{"plugins":[
		{"name":"a","kind":"app","repo":"r","description":"one","aliases":["x"]},
		{"name":"b","kind":"app","repo":"r2","description":"two","aliases":["x"]}
	]}`)); err == nil {
		t.Fatal("duplicate alias")
	}
}

func TestWriteKnownPlugins(t *testing.T) {
	var b strings.Builder
	if err := WriteKnownPlugins(&b, "randy-girard", KnownPlugins()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, needle := range []string{"NAME", "mariadb", "mysql", "randy-girard/flynn-plugin-mariadb", "OpenTelemetry", "scheduler", "flynn-plugin-scheduler"} {
		if !strings.Contains(out, needle) {
			t.Fatalf("missing %q in:\n%s", needle, out)
		}
	}
}

func TestKnownPluginRepoSlug(t *testing.T) {
	p := KnownPlugin{Repo: "flynn-plugin-redis"}
	if p.RepoSlug("acme") != "acme/flynn-plugin-redis" {
		t.Fatalf("%s", p.RepoSlug("acme"))
	}
	p.Repo = "other/custom.git"
	if p.RepoSlug("acme") != "other/custom" {
		t.Fatalf("owner/name must keep catalog owner: %s", p.RepoSlug("acme"))
	}
}
