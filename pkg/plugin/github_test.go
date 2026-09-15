package plugin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
)

func TestParseGitHubURL(t *testing.T) {
	cases := []struct {
		in          string
		owner, repo string
	}{
		{"https://github.com/randy-girard/flynn-plugin-redis.git", "randy-girard", "flynn-plugin-redis"},
		{"https://github.com/randy-girard/flynn-plugin-redis", "randy-girard", "flynn-plugin-redis"},
		{"git@github.com:randy-girard/flynn-plugin-redis.git", "randy-girard", "flynn-plugin-redis"},
		{"github.com/randy-girard/flynn-plugin-redis", "randy-girard", "flynn-plugin-redis"},
	}
	for _, c := range cases {
		got, err := ParseGitHubURL(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got.Owner != c.owner || got.Repo != c.repo || got.Host != "github.com" {
			t.Fatalf("%s: %+v", c.in, got)
		}
	}
}

func TestLoadConfigGitHubSettings(t *testing.T) {
	t.Setenv(EnvPluginRepoRoot, t.TempDir())
	t.Setenv(EnvInstalledFile, filepath.Join(t.TempDir(), "none.json"))
	path := filepath.Join(t.TempDir(), "plugins.json")
	writeJSON(t, path, map[string]interface{}{
		"github_org": "acme",
		"redis": map[string]string{
			"url": "https://github.com/acme/flynn-plugin-redis.git",
			"ref": "v20260914.0",
		},
		"widget": "/opt/plugins/widget",
	})
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubOrg != "acme" {
		t.Fatalf("org=%s", cfg.GitHubOrg)
	}
	if cfg.GitHubURL("redis") != "https://github.com/acme/flynn-plugin-redis.git" {
		t.Fatalf("url=%s", cfg.GitHubURL("redis"))
	}
	if cfg.alias("redis").Ref != "v20260914.0" {
		t.Fatalf("ref=%s", cfg.alias("redis").Ref)
	}
	if cfg.alias("widget").Path != "/opt/plugins/widget" {
		t.Fatalf("widget path=%s", cfg.alias("widget").Path)
	}
}

func TestResolvePrefersLocalThenGitHub(t *testing.T) {
	t.Setenv(EnvGitHubToken, "")
	t.Setenv(EnvGitHubTokenAlt, "")
	t.Setenv(EnvGitHubOrg, "randy-girard")
	root := t.TempDir()
	t.Setenv(EnvPluginRepoRoot, root)

	pluginDir := filepath.Join(root, "flynn-plugin-redis")
	if err := os.Mkdir(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(pluginDir, ManifestName), map[string]interface{}{
		"name": "redis",
		"kind": "app",
		"app": map[string]interface{}{
			"processes": map[string]interface{}{"web": map[string]interface{}{"args": []string{"/bin/x"}}},
		},
	})

	local, err := Resolve(InstallOptions{Source: "redis", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if local.Dir != pluginDir || local.GitHub != nil {
		t.Fatalf("local should win: %+v", local)
	}

	if err := os.RemoveAll(pluginDir); err != nil {
		t.Fatal(err)
	}
	remote, err := Resolve(InstallOptions{Source: "redis", Ref: "v20260914.0", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if remote.Dir != "" || remote.GitHub == nil || remote.GitHub.Repo != "flynn-plugin-redis" {
		t.Fatalf("expected GitHub fallback: %+v", remote)
	}
	if remote.GitHub.Owner != "randy-girard" || remote.Ref != "v20260914.0" {
		t.Fatalf("github %+v ref=%s", remote.GitHub, remote.Ref)
	}

	urlSrc, err := Resolve(InstallOptions{
		Source: "https://github.com/acme/flynn-plugin-redis.git",
		Ref:    "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if urlSrc.GitHub.Owner != "acme" || urlSrc.Ref != "v1" {
		t.Fatalf("%+v", urlSrc.GitHub)
	}
}

func TestFetchGitHubRelease(t *testing.T) {
	t.Setenv(EnvGitHubToken, "")
	t.Setenv(EnvGitHubTokenAlt, "")

	osID := "ubuntu-noble-os"
	deltaID := "plugin-delta"
	manifest := &ct.ImageManifest{
		Type: ct.ImageManifestTypeV1,
		Rootfs: []*ct.ImageRootfs{{
			Layers: []*ct.ImageLayer{
				{
					ID:     osID,
					Type:   ct.ImageLayerTypeSquashfs,
					Length: 199 << 20,
					Hashes: map[string]string{"sha512_256": osID},
				},
				{
					ID:     deltaID,
					Type:   ct.ImageLayerTypeSquashfs,
					Length: 34 << 20,
					Hashes: map[string]string{"sha512_256": deltaID},
				},
			},
		}},
	}
	raw := manifest.RawManifest()
	image := &ct.Artifact{
		Type:        ct.ArtifactTypeFlynn,
		RawManifest: raw,
		Hashes:      map[string]string{"sha512_256": "deadbeef"},
		Size:        int64(len(raw)),
	}
	imageJSON, _ := json.Marshal(image)
	pluginJSON := []byte(`{
  "name": "redis",
  "kind": "resource-provider",
  "provider": {"name": "redis", "url": "http://redis-api.discoverd/clusters"},
  "app": {"name": "redis", "processes": {"web": {"args": ["/bin/start-flynn-redis", "api"]}}},
  "artifacts": {"image": "https://example.invalid/image.json"}
}`)
	layerBytes := []byte("squashfs-bytes")

	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/randy-girard/flynn-plugin-redis/releases/tags/v20260914.0", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent")
		}
		_ = json.NewEncoder(w).Encode(githubRelease{
			TagName: "v20260914.0",
			Assets: []githubAsset{
				{Name: ManifestName, BrowserDownloadURL: srv.URL + "/files/flynn-plugin.json"},
				{Name: ImageJSON, BrowserDownloadURL: srv.URL + "/files/image.json"},
				{Name: osID + ".squashfs", BrowserDownloadURL: srv.URL + "/files/" + osID + ".squashfs"},
				{Name: deltaID + ".squashfs", BrowserDownloadURL: srv.URL + "/files/" + deltaID + ".squashfs"},
			},
		})
	})
	mux.HandleFunc("/files/flynn-plugin.json", func(w http.ResponseWriter, r *http.Request) { w.Write(pluginJSON) })
	mux.HandleFunc("/files/image.json", func(w http.ResponseWriter, r *http.Request) { w.Write(imageJSON) })
	mux.HandleFunc("/files/"+osID+".squashfs", func(w http.ResponseWriter, r *http.Request) { w.Write(layerBytes) })
	mux.HandleFunc("/files/"+deltaID+".squashfs", func(w http.ResponseWriter, r *http.Request) { w.Write(layerBytes) })
	srv = httptest.NewServer(mux)
	defer srv.Close()

	in := &Installer{GitHubHTTP: srv.Client()}
	dir, err := in.fetchGitHub(&GitHubSource{
		Host:  "github.com",
		Owner: "randy-girard",
		Repo:  "flynn-plugin-redis",
		Ref:   "v20260914.0",
		API:   srv.URL,
	}, filepath.Join(t.TempDir(), "missing-creds.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	if !DistReady(dir) {
		t.Fatal("expected dist/ from GitHub assets")
	}
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "redis" || m.Kind != KindResourceProvider {
		t.Fatalf("manifest %+v", m)
	}
}

func TestCredentialsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin-credentials.json")
	if err := SetGitHubCredentials(path, "github.com", "ghp_test", ""); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("perm=%s", st.Mode().Perm())
	}
	ok, err := CredentialsSet(path, "github.com")
	if err != nil || !ok {
		t.Fatalf("set=%v err=%v", ok, err)
	}
	t.Setenv(EnvGitHubToken, "")
	t.Setenv(EnvGitHubTokenAlt, "")
	tok, _, err := TokenForHost("github.com", path)
	if err != nil || tok != "ghp_test" {
		t.Fatalf("token=%q err=%v", tok, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "ghp_test") {
		t.Fatalf("file should store token: %s", data)
	}
}

func TestParseGitHubURLErrorsAndEnterprise(t *testing.T) {
	if _, err := ParseGitHubURL(""); err == nil {
		t.Fatal("empty")
	}
	if _, err := ParseGitHubURL("git@github.com-nocolon"); err == nil {
		t.Fatal("ssh without colon")
	}
	if _, err := ParseGitHubURL("https://github.com/onlyowner"); err == nil {
		t.Fatal("missing repo")
	}
	got, err := ParseGitHubURL("https://ghe.example.com/acme/plug.git")
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "ghe.example.com" || got.Owner != "acme" || got.Repo != "plug" || got.API != "https://ghe.example.com/api/v3" {
		t.Fatalf("%+v", got)
	}
	ssh, err := ParseGitHubURL("ssh://git@github.com/acme/plug.git")
	if err != nil || ssh.Owner != "acme" || ssh.Repo != "plug" {
		t.Fatalf("%+v %v", ssh, err)
	}
}

func TestGitHubSourceHelpers(t *testing.T) {
	if (*GitHubSource)(nil).ReleaseAssetURL("image.json") != "" {
		t.Fatal("nil source")
	}
	g := &GitHubSource{Host: "github.com", Owner: "acme", Repo: "plug", Ref: "latest"}
	if g.ReleaseAssetURL("image.json") != "" {
		t.Fatal("latest has no direct download URL")
	}
	g.Ref = "v1"
	want := "https://github.com/acme/plug/releases/download/v1/image.json"
	if got := g.ReleaseAssetURL("image.json"); got != want {
		t.Fatalf("got %s", got)
	}
	if g.String() != "acme/plug@v1" {
		t.Fatalf("string=%s", g.String())
	}
	g.Ref = ""
	if g.String() != "acme/plug" {
		t.Fatalf("string=%s", g.String())
	}
	if (*GitHubSource)(nil).String() != "" {
		t.Fatal("nil string")
	}
	r := &Resolved{Ref: "v1"}
	if r.RefOr("latest") != "v1" || (*Resolved)(nil).RefOr("latest") != "latest" {
		t.Fatal("RefOr")
	}
}

func TestLoadConfigMissingFileAndDefaults(t *testing.T) {
	t.Setenv(EnvGitHubOrg, "")
	t.Setenv(EnvFlynnRepo, "acme/flynn")
	if DefaultGitHubOrg() != "acme" {
		t.Fatalf("org from FLYNN_GITHUB_REPO: %s", DefaultGitHubOrg())
	}
	t.Setenv(EnvFlynnRepo, "")
	t.Setenv(EnvPluginRepoRoot, t.TempDir())
	t.Setenv(EnvInstalledFile, filepath.Join(t.TempDir(), "none.json"))
	if DefaultGitHubOrg() != "randy-girard" {
		t.Fatalf("builtin org: %s", DefaultGitHubOrg())
	}

	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubURL("mysql") != "https://github.com/randy-girard/flynn-plugin-mysql.git" {
		t.Fatalf("unknown alias uses flynn-plugin-<name>: %s", cfg.GitHubURL("mysql"))
	}
	if cfg.alias("redis").Path != "" {
		t.Fatalf("missing checkouts must not invent aliases: %+v", cfg.alias("redis"))
	}
}

func TestLoadConfigDottedKeysAndRepoOverride(t *testing.T) {
	t.Setenv(EnvPluginRepoRoot, t.TempDir())
	t.Setenv(EnvInstalledFile, filepath.Join(t.TempDir(), "none.json"))
	path := filepath.Join(t.TempDir(), "plugins.json")
	writeJSON(t, path, map[string]interface{}{
		"github.org": "dotted-org",
		"github.api": "https://ghe.example.com/api/v3",
		"widget":     "https://github.com/acme/custom-widget.git",
		"redis": map[string]string{
			"repo": "acme/flynn-plugin-redis",
			"ref":  "v9",
		},
		"cache": map[string]string{"repo": "flynn-plugin-cache"},
	})
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubOrg != "dotted-org" || cfg.GitHubAPI != "https://ghe.example.com/api/v3" {
		t.Fatalf("org/api %+v", cfg)
	}
	if cfg.alias("widget").URL != "https://github.com/acme/custom-widget.git" {
		t.Fatalf("string git URL: %+v", cfg.alias("widget"))
	}
	if cfg.GitHubURL("redis") != "https://github.com/acme/flynn-plugin-redis.git" {
		t.Fatalf("owner/name repo: %s", cfg.GitHubURL("redis"))
	}
	if cfg.GitHubURL("cache") != "https://github.com/dotted-org/flynn-plugin-cache.git" {
		t.Fatalf("short repo: %s", cfg.GitHubURL("cache"))
	}
	if cfg.alias("redis").Ref != "v9" || cfg.alias("redis").Repo != "acme/flynn-plugin-redis" {
		t.Fatalf("plugins.json must apply repo/ref: %+v", cfg.alias("redis"))
	}
}

func TestResolveEmptySourceAndOverrides(t *testing.T) {
	t.Setenv(EnvGitHubToken, "")
	t.Setenv(EnvGitHubTokenAlt, "")
	t.Setenv(EnvPluginRepoRoot, t.TempDir())
	inst := filepath.Join(t.TempDir(), "installed.json")
	t.Setenv(EnvInstalledFile, inst)
	if err := WriteInstalled(inst, []Installed{{
		Name:       "mariadb",
		Aliases:    []string{"mysql"},
		GitHubRepo: "flynn-plugin-mariadb",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(InstallOptions{}); err == nil {
		t.Fatal("empty source")
	}
	_, err := Resolve(InstallOptions{Source: "./missing", Cwd: t.TempDir()})
	nf, ok := err.(*NotFoundError)
	if !ok || nf == nil || !strings.Contains(nf.Error(), ManifestName) {
		t.Fatalf("path miss: %v", err)
	}

	got, err := Resolve(InstallOptions{Source: "mysql", GitHubOrg: "other-org", Ref: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.GitHub == nil || got.GitHub.Owner != "other-org" || got.GitHub.Repo != "flynn-plugin-mariadb" || got.Ref != "v2" {
		t.Fatalf("mysql github fallback: %+v", got)
	}

	cfgPath := filepath.Join(t.TempDir(), "plugins.json")
	writeJSON(t, cfgPath, map[string]string{"github_api": "https://ghe.example.com/api/v3"})
	ent, err := Resolve(InstallOptions{
		Source:      "https://ghe.example.com/acme/plug.git",
		PluginsFile: cfgPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ent.GitHub.API != "https://ghe.example.com/api/v3" {
		t.Fatalf("config API override: %+v", ent.GitHub)
	}
}

func TestCredentialsEnvOverridesFileAndUnset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin-credentials.json")
	if err := SetGitHubCredentials(path, "", "", ""); err == nil {
		t.Fatal("empty token must fail")
	}
	if err := SetGitHubCredentials(path, "github.com", "file-token", "https://api.github.com"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvGitHubToken, " env-token ")
	t.Setenv(EnvGitHubTokenAlt, "")
	tok, api, err := TokenForHost("github.com", path)
	if err != nil || tok != "env-token" || api != "" {
		t.Fatalf("env must win: tok=%q api=%q err=%v", tok, api, err)
	}
	ok, err := CredentialsSet(path, "github.com")
	if err != nil || !ok {
		t.Fatalf("env counts as set: %v %v", ok, err)
	}

	t.Setenv(EnvGitHubToken, "")
	if err := UnsetGitHubCredentials(path, "github.com"); err != nil {
		t.Fatal(err)
	}
	ok, err = CredentialsSet(path, "github.com")
	if err != nil || ok {
		t.Fatalf("unset: %v %v", ok, err)
	}
	if err := UnsetGitHubCredentials(path, "github.com"); err != nil {
		t.Fatal(err)
	}
	tok, _, err = TokenForHost("", filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || tok != "" {
		t.Fatalf("missing creds: %q %v", tok, err)
	}
	got, err := ReadToken(strings.NewReader("  ghp_from_stdin \n"))
	if err != nil || got != "ghp_from_stdin" {
		t.Fatalf("ReadToken=%q err=%v", got, err)
	}
}

func TestFetchGitHubMissingManifestAndAuth(t *testing.T) {
	t.Setenv(EnvGitHubToken, "")
	t.Setenv(EnvGitHubTokenAlt, "")
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/plug/releases/tags/v1", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(githubRelease{
			TagName: "v1",
			Assets:  []githubAsset{{Name: ImageJSON, BrowserDownloadURL: srv.URL + "/files/image.json"}},
		})
	})
	mux.HandleFunc("/repos/acme/plug/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization=%q", got)
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/repos/acme/missing/releases/tags/v1", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	in := &Installer{GitHubHTTP: srv.Client()}
	if _, err := in.fetchGitHub(nil, ""); err == nil {
		t.Fatal("nil source")
	}
	_, err := in.fetchGitHub(&GitHubSource{Host: "github.com", Owner: "acme", Repo: "plug", Ref: "v1", API: srv.URL}, "")
	if err == nil || !strings.Contains(err.Error(), ManifestName) {
		t.Fatalf("missing manifest: %v", err)
	}

	_, err = in.getRelease(&GitHubSource{Owner: "acme", Repo: "missing", Ref: "v1", API: srv.URL}, "")
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("404 without token should hint credentials: %v", err)
	}

	_, err = in.getRelease(&GitHubSource{Owner: "acme", Repo: "plug", Ref: "latest", API: srv.URL}, "secret")
	if err == nil || !strings.Contains(err.Error(), "latest") {
		t.Fatalf("latest 404: %v", err)
	}
}

func TestSanitizeURLAndRefOrLatest(t *testing.T) {
	if got := sanitizeURL("https://user:pass@github.com/acme/plug"); !strings.Contains(got, "://***") {
		t.Fatalf("sanitize=%s", got)
	}
	if got := sanitizeURL("https://github.com/acme/plug"); got != "https://github.com/acme/plug" {
		t.Fatalf("plain=%s", got)
	}
	if refOrLatest("") != "latest" || refOrLatest("v1") != "v1" {
		t.Fatal("refOrLatest")
	}

	req, _ := http.NewRequest("GET", "https://example.invalid", nil)
	(&Installer{}).githubHeaders(req, "tok", "application/octet-stream")
	if req.Header.Get("User-Agent") == "" || req.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("headers=%v", req.Header)
	}
	if req.Header.Get("Accept") != "application/octet-stream" {
		t.Fatal("accept")
	}
	rel := githubRelease{Assets: []githubAsset{{Name: "image.json"}}}
	if rel.asset("image.json") == nil || rel.asset("missing") != nil {
		t.Fatal("asset lookup")
	}
}
