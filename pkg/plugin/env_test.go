package plugin

import (
	"errors"
	"path/filepath"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

type releaseMap map[string]*ct.Release

func (m releaseMap) GetAppRelease(appID string) (*ct.Release, error) {
	r, ok := m[appID]
	if !ok {
		return nil, errors.New("not found")
	}
	return r, nil
}

func TestClusterEnvRequiresControllerKey(t *testing.T) {
	_, err := ClusterEnv(releaseMap{})
	if err == nil {
		t.Fatal("missing controller/postgres releases must fail")
	}

	_, err = ClusterEnv(releaseMap{
		"controller": {Env: map[string]string{"SINGLETON": "true"}},
		"postgres":   {Env: map[string]string{}},
	})
	if err == nil {
		t.Fatal("empty keys must fail closed")
	}
}

func TestClusterEnvPrefersControllerKeyOverAuthKey(t *testing.T) {
	env, err := ClusterEnv(releaseMap{
		"controller": {Env: map[string]string{
			"CONTROLLER_KEY":       "from-controller",
			"AUTH_KEY":             "legacy-auth",
			"CLUSTER_DOMAIN":       "example.local",
			"DEFAULT_ROUTE_DOMAIN": "routes.example.local",
			"SINGLETON":            "true",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["CONTROLLER_KEY"] != "from-controller" {
		t.Fatalf("CONTROLLER_KEY=%q", env["CONTROLLER_KEY"])
	}
	if env["CLUSTER_DOMAIN"] != "example.local" {
		t.Fatalf("CLUSTER_DOMAIN=%q", env["CLUSTER_DOMAIN"])
	}
	if env["DEFAULT_ROUTE_DOMAIN"] != "routes.example.local" {
		t.Fatalf("DEFAULT_ROUTE_DOMAIN=%q", env["DEFAULT_ROUTE_DOMAIN"])
	}
	if env["SINGLETON"] != "true" {
		t.Fatalf("SINGLETON=%q", env["SINGLETON"])
	}
}

func TestClusterEnvFallsBackToAuthKeyAndRouteDomain(t *testing.T) {
	env, err := ClusterEnv(releaseMap{
		"postgres": {Env: map[string]string{
			"AUTH_KEY":             "legacy-auth",
			"DEFAULT_ROUTE_DOMAIN": "routes.example.local",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["CONTROLLER_KEY"] != "legacy-auth" {
		t.Fatalf("AUTH_KEY fallback: %q", env["CONTROLLER_KEY"])
	}
	if env["CLUSTER_DOMAIN"] != "routes.example.local" {
		t.Fatalf("CLUSTER_DOMAIN should copy DEFAULT_ROUTE_DOMAIN, got %q", env["CLUSTER_DOMAIN"])
	}
	if env["SINGLETON"] != "false" {
		t.Fatalf("default SINGLETON=%q", env["SINGLETON"])
	}
}

func TestClusterEnvCopiesDiscoverdAuthKey(t *testing.T) {
	env, err := ClusterEnv(releaseMap{
		"controller": {Env: map[string]string{"CONTROLLER_KEY": "ck"}},
		"discoverd":  {Env: map[string]string{"DISCOVERD_AUTH_KEY": "disc-secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["DISCOVERD_AUTH_KEY"] != "disc-secret" {
		t.Fatalf("DISCOVERD_AUTH_KEY=%q", env["DISCOVERD_AUTH_KEY"])
	}
}

func TestClusterEnvCopiesGitreceiveAccessTokens(t *testing.T) {
	env, err := ClusterEnv(releaseMap{
		"controller": {Env: map[string]string{
			"CONTROLLER_KEY": "ck",
			"CLUSTER_DOMAIN": "ex.local",
		}},
		"gitreceive": {Env: map[string]string{
			"ACCESS_TOKEN_KEY":         "pub",
			"ACCESS_TOKEN_SIGNING_KEY": "priv",
			"GIT_URL":                  "https://git.ex.local",
			"IMAGE_URL":                "https://images.ex.local",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["ACCESS_TOKEN_KEY"] != "pub" {
		t.Fatalf("ACCESS_TOKEN_KEY=%q", env["ACCESS_TOKEN_KEY"])
	}
	if env["ACCESS_TOKEN_SIGNING_KEY"] != "priv" {
		t.Fatalf("ACCESS_TOKEN_SIGNING_KEY=%q", env["ACCESS_TOKEN_SIGNING_KEY"])
	}
	if env["ACCESS_TOKEN_PRIVATE_KEY"] != "priv" {
		t.Fatalf("ACCESS_TOKEN_PRIVATE_KEY=%q", env["ACCESS_TOKEN_PRIVATE_KEY"])
	}
	if env["GIT_URL"] != "https://git.ex.local" || env["IMAGE_URL"] != "https://images.ex.local" {
		t.Fatalf("urls %+v", env)
	}
}

func TestEnsureClusterAuthEnvFillsMissingKeys(t *testing.T) {
	env := map[string]string{"CONTROLLER_KEY": ""}
	cluster := map[string]string{
		"AUTH_KEY":           "legacy",
		"DISCOVERD_AUTH_KEY": "disc",
		"ACCESS_TOKEN_KEY":   "pub",
	}
	if !EnsureClusterAuthEnv(env, cluster) {
		t.Fatal("expected change")
	}
	if env["CONTROLLER_KEY"] != "legacy" || env["DISCOVERD_AUTH_KEY"] != "disc" || env["ACCESS_TOKEN_KEY"] != "pub" {
		t.Fatalf("%v", env)
	}
	if EnsureClusterAuthEnv(env, map[string]string{"CONTROLLER_KEY": "other", "DISCOVERD_AUTH_KEY": "nope"}) {
		t.Fatal("must not overwrite")
	}
	if env["CONTROLLER_KEY"] != "legacy" {
		t.Fatalf("overwrote CONTROLLER_KEY: %v", env)
	}
}

func TestEnsureClusterAuthEnvNilSafe(t *testing.T) {
	if EnsureClusterAuthEnv(nil, map[string]string{"CONTROLLER_KEY": "k"}) {
		t.Fatal("nil env")
	}
	if EnsureClusterAuthEnv(map[string]string{}, nil) {
		t.Fatal("nil cluster")
	}
}

func TestFormationScaleAndGeneratedEnv(t *testing.T) {
	m := &Manifest{
		GenerateEnv: []string{"MYSQL_PWD"},
		Env:         map[string]string{"FLYNN_MYSQL": "mariadb"},
		App: AppSpec{
			Processes: map[string]ct.ProcessType{
				"mariadb": {},
				"web":     {},
			},
			Scale: map[string]int{"mariadb": 0},
		},
	}
	scale := FormationScale(m, map[string]string{"SINGLETON": "true"})
	if scale["mariadb"] != 0 || scale["web"] != 1 {
		t.Fatalf("%v", scale)
	}
	scale = FormationScale(m, nil)
	if scale["web"] != 2 {
		t.Fatalf("ha web=%v", scale)
	}

	zeros := previousReleaseScaleDown(
		&ct.Release{Processes: map[string]ct.ProcessType{"web": {}, "worker": {}}},
		&ct.Formation{Processes: map[string]int{"web": 1}},
	)
	if zeros["web"] != 0 || zeros["worker"] != 0 || len(zeros) != 2 {
		t.Fatalf("scale-down: %v", zeros)
	}
	if got := previousReleaseScaleDown(nil, nil); len(got) != 0 {
		t.Fatalf("nil previous must not invent processes: %v", got)
	}

	env := ReleaseEnv(m, "art", map[string]string{})
	if env["FLYNN_MYSQL"] != "mariadb" || len(env["MYSQL_PWD"]) != 32 {
		t.Fatalf("%v", env)
	}
	first := env["MYSQL_PWD"]
	PreserveGeneratedEnv(m, env, map[string]string{"MYSQL_PWD": "keep-me"})
	if env["MYSQL_PWD"] != "keep-me" {
		t.Fatalf("preserve: %q (generated %q)", env["MYSQL_PWD"], first)
	}
}

func TestInjectGitHubTokenFromCredentialsFile(t *testing.T) {
	t.Setenv(EnvGitHubToken, "")
	t.Setenv(EnvGitHubTokenAlt, "")
	path := filepath.Join(t.TempDir(), "plugin-credentials.json")
	if err := SetGitHubCredentials(path, "github.com", "ghp_from_file", ""); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{}
	injectGitHubToken(env, path)
	if env[EnvGitHubToken] != "ghp_from_file" {
		t.Fatalf("%s=%q", EnvGitHubToken, env[EnvGitHubToken])
	}
	if env[EnvGitHubTokenAlt] != "ghp_from_file" {
		t.Fatalf("%s=%q", EnvGitHubTokenAlt, env[EnvGitHubTokenAlt])
	}

	env = map[string]string{EnvGitHubToken: "keep-plugin", EnvGitHubTokenAlt: "keep-github"}
	injectGitHubToken(env, path)
	if env[EnvGitHubToken] != "ghp_from_file" {
		t.Fatalf("FLYNN_PLUGIN_GITHUB_TOKEN should follow host credentials, got %q", env[EnvGitHubToken])
	}
	if env[EnvGitHubTokenAlt] != "keep-github" {
		t.Fatalf("GITHUB_TOKEN must stay when already set, got %q", env[EnvGitHubTokenAlt])
	}

	injectGitHubToken(nil, path)
	empty := map[string]string{}
	injectGitHubToken(empty, filepath.Join(t.TempDir(), "missing.json"))
	if empty[EnvGitHubToken] != "" || empty[EnvGitHubTokenAlt] != "" {
		t.Fatalf("missing file must not set tokens: %v", empty)
	}
}

func TestReleaseEnvInjectsGitHubTokenFromHostEnv(t *testing.T) {
	t.Setenv(EnvGitHubToken, "from-host-env")
	t.Setenv(EnvGitHubTokenAlt, "")
	env := ReleaseEnv(&Manifest{Env: map[string]string{"FLYNN_WIDGET": "widget"}}, "art", map[string]string{})
	if env[EnvGitHubToken] != "from-host-env" {
		t.Fatalf("%s=%q", EnvGitHubToken, env[EnvGitHubToken])
	}
	if env[EnvGitHubTokenAlt] != "from-host-env" {
		t.Fatalf("%s=%q", EnvGitHubTokenAlt, env[EnvGitHubTokenAlt])
	}
	if env["FLYNN_WIDGET"] != "widget" {
		t.Fatalf("static env lost: %v", env)
	}
}
