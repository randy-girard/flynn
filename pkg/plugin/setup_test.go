package plugin

import (
	"io"
	"strings"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
)

func TestExpandClusterVars(t *testing.T) {
	got := ExpandClusterVars("https://dashboard.${CLUSTER_DOMAIN}", map[string]string{
		"CLUSTER_DOMAIN": "example.local",
	})
	if got != "https://dashboard.example.local" {
		t.Fatalf("got %q", got)
	}
}

func TestApplySetupNonInteractiveDefaultAndGenerate(t *testing.T) {
	t.Setenv(nonInteractiveEnv, "1")
	in := &Installer{Interactive: func() bool { return false }}
	cluster := map[string]string{"CLUSTER_DOMAIN": "smoke.local"}
	m := &Manifest{
		Setup: []SetupPrompt{
			{Env: "ADMIN_EMAIL", Prompt: "Admin email", Default: "admin@${CLUSTER_DOMAIN}"},
			{Env: "BOOTSTRAP_ADMIN_PASSWORD", Prompt: "Password", Generate: true, Secret: true},
			{Env: "OPTIONAL_NOTE", Prompt: "Note", Optional: true},
		},
	}
	if err := in.applySetup(m, cluster); err != nil {
		t.Fatal(err)
	}
	if cluster["ADMIN_EMAIL"] != "admin@smoke.local" {
		t.Fatalf("ADMIN_EMAIL=%q", cluster["ADMIN_EMAIL"])
	}
	if len(cluster["BOOTSTRAP_ADMIN_PASSWORD"]) != 32 {
		t.Fatalf("password %q", cluster["BOOTSTRAP_ADMIN_PASSWORD"])
	}
	if cluster["OPTIONAL_NOTE"] != "" {
		t.Fatalf("optional should stay empty, got %q", cluster["OPTIONAL_NOTE"])
	}
}

func TestApplySetupEnvOverride(t *testing.T) {
	t.Setenv(nonInteractiveEnv, "1")
	t.Setenv("FLYNN_PLUGIN_SETUP_ADMIN_EMAIL", "ops@example.com")
	in := &Installer{Interactive: func() bool { return false }}
	cluster := map[string]string{}
	m := &Manifest{Setup: []SetupPrompt{{Env: "ADMIN_EMAIL", Prompt: "Admin"}}}
	if err := in.applySetup(m, cluster); err != nil {
		t.Fatal(err)
	}
	if cluster["ADMIN_EMAIL"] != "ops@example.com" {
		t.Fatalf("got %q", cluster["ADMIN_EMAIL"])
	}
}

func TestApplySetupRequiredMissing(t *testing.T) {
	t.Setenv(nonInteractiveEnv, "1")
	in := &Installer{Interactive: func() bool { return false }}
	cluster := map[string]string{}
	m := &Manifest{Setup: []SetupPrompt{{Env: "MUST_SET", Prompt: "Need this"}}}
	err := in.applySetup(m, cluster)
	if err == nil || !strings.Contains(err.Error(), "MUST_SET") {
		t.Fatalf("got %v", err)
	}
}

func TestManifestValidateSetupAndRoutes(t *testing.T) {
	ok := &Manifest{
		Name: "dash",
		Kind: KindApp,
		App: AppSpec{
			Processes: map[string]ct.ProcessType{"web": {}},
		},
		Setup: []SetupPrompt{
			{Env: "ADMIN_EMAIL", Prompt: "Admin email", Default: "admin@${CLUSTER_DOMAIN}"},
		},
		Routes: []RouteSpec{
			{Type: "http", Domain: "dashboard.${CLUSTER_DOMAIN}", Service: "dashboard"},
		},
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := &Manifest{
		Name: "dash",
		Kind: KindApp,
		App:  AppSpec{Processes: map[string]ct.ProcessType{"web": {}}},
		Setup: []SetupPrompt{
			{Env: "", Prompt: "missing env"},
		},
	}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected setup.env required")
	}
	badRoute := &Manifest{
		Name: "dash",
		Kind: KindApp,
		App:  AppSpec{Processes: map[string]ct.ProcessType{"web": {}}},
		Routes: []RouteSpec{
			{Type: "http", Service: "dashboard"},
		},
	}
	if err := badRoute.Validate(); err == nil {
		t.Fatal("expected http route domain")
	}
}

func TestReleaseEnvExpandsClusterAndSetup(t *testing.T) {
	m := &Manifest{
		Env:         map[string]string{"URL": "https://dashboard.${CLUSTER_DOMAIN}"},
		GenerateEnv: []string{"SESSION_SECRET"},
		Setup:       []SetupPrompt{{Env: "ADMIN_EMAIL", Prompt: "Admin"}},
	}
	cluster := map[string]string{
		"CLUSTER_DOMAIN": "ex.local",
		"ADMIN_EMAIL":    "admin@ex.local",
		"DATABASE_URL":   "postgres://db",
	}
	env := ReleaseEnv(m, "art", cluster)
	if env["URL"] != "https://dashboard.ex.local" {
		t.Fatalf("URL=%q", env["URL"])
	}
	if env["ADMIN_EMAIL"] != "admin@ex.local" {
		t.Fatalf("ADMIN_EMAIL=%q", env["ADMIN_EMAIL"])
	}
	if env["DATABASE_URL"] != "postgres://db" {
		t.Fatalf("DATABASE_URL=%q", env["DATABASE_URL"])
	}
	if len(env["SESSION_SECRET"]) != 32 {
		t.Fatalf("SESSION_SECRET=%q", env["SESSION_SECRET"])
	}
}

func TestPreservePreviousEnvKeepsDatabaseURL(t *testing.T) {
	env := map[string]string{"URL": "https://dash"}
	PreservePreviousEnv(env, map[string]string{"DATABASE_URL": "postgres://old", "URL": "https://stale"})
	if env["DATABASE_URL"] != "postgres://old" {
		t.Fatalf("DATABASE_URL=%q", env["DATABASE_URL"])
	}
	if env["URL"] != "https://dash" {
		t.Fatalf("URL must not be overwritten, got %q", env["URL"])
	}
}

func TestApplySetupInteractiveAnswers(t *testing.T) {
	in := &Installer{
		Stdin:       strings.NewReader("ops@example.com\nsecret-pass\n"),
		Stdout:      io.Discard,
		Interactive: func() bool { return true },
	}
	cluster := map[string]string{"CLUSTER_DOMAIN": "ex.local"}
	m := &Manifest{
		Setup: []SetupPrompt{
			{Env: "ADMIN_EMAIL", Prompt: "Email", Default: "admin@${CLUSTER_DOMAIN}"},
			{Env: "BOOTSTRAP_ADMIN_PASSWORD", Prompt: "Password", Secret: true, Generate: true},
		},
	}
	if err := in.applySetup(m, cluster); err != nil {
		t.Fatal(err)
	}
	if cluster["ADMIN_EMAIL"] != "ops@example.com" {
		t.Fatalf("ADMIN_EMAIL=%q", cluster["ADMIN_EMAIL"])
	}
	if cluster["BOOTSTRAP_ADMIN_PASSWORD"] != "secret-pass" {
		t.Fatalf("password=%q", cluster["BOOTSTRAP_ADMIN_PASSWORD"])
	}
}

func TestPreserveGeneratedEnvKeepsSetupSecrets(t *testing.T) {
	m := &Manifest{
		GenerateEnv: []string{"SESSION_SECRET"},
		Setup:       []SetupPrompt{{Env: "BOOTSTRAP_ADMIN_PASSWORD", Prompt: "Password", Generate: true, Secret: true}},
	}
	env := map[string]string{"SESSION_SECRET": "new", "BOOTSTRAP_ADMIN_PASSWORD": "rotated"}
	PreserveGeneratedEnv(m, env, map[string]string{
		"SESSION_SECRET":           "keep-session",
		"BOOTSTRAP_ADMIN_PASSWORD": "keep-pass",
	})
	if env["SESSION_SECRET"] != "keep-session" {
		t.Fatalf("SESSION_SECRET=%q", env["SESSION_SECRET"])
	}
	if env["BOOTSTRAP_ADMIN_PASSWORD"] != "keep-pass" {
		t.Fatalf("BOOTSTRAP_ADMIN_PASSWORD=%q", env["BOOTSTRAP_ADMIN_PASSWORD"])
	}
}
