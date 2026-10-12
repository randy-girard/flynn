package updater

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestSystemAppsUpgradeOrder(t *testing.T) {
	// discoverd and blobstore must land before controller/databases so
	// post-upgrade deploys have discovery and image storage available.
	wantPrefix := []string{"discoverd", "blobstore", "taffy", "router", "gitreceive", "tarreceive", "controller"}
	if len(SystemApps) < len(wantPrefix) {
		t.Fatalf("SystemApps too short: %d", len(SystemApps))
	}
	for i, name := range wantPrefix {
		if SystemApps[i].Name != name {
			t.Fatalf("SystemApps[%d]=%q want %q", i, SystemApps[i].Name, name)
		}
	}

	byName := make(map[string]SystemApp, len(SystemApps))
	for _, app := range SystemApps {
		if _, dup := byName[app.Name]; dup {
			t.Fatalf("duplicate system app %q", app.Name)
		}
		byName[app.Name] = app
	}
	for _, required := range []string{"postgres", "status", "logaggregator"} {
		if _, ok := byName[required]; !ok {
			t.Fatalf("missing required system app %q", required)
		}
	}
	for _, extracted := range []string{"mariadb", "mongodb", "kafka", "clickhouse", "redis"} {
		if _, ok := byName[extracted]; ok {
			t.Fatalf("%s is a plugin and must not be a system app", extracted)
		}
	}
	for _, imageOnly := range []string{"slugbuilder", "slugrunner", "dockerbuilder"} {
		if !byName[imageOnly].ImageOnly {
			t.Fatalf("%s should be ImageOnly", imageOnly)
		}
	}
}

func TestPostgresUpdateReleaseSetsSireniaProcess(t *testing.T) {
	var fn UpdateReleaseFn
	for _, app := range SystemApps {
		if app.Name == "postgres" {
			fn = app.UpdateRelease
			break
		}
	}
	if fn == nil {
		t.Fatal("postgres UpdateRelease missing")
	}
	r := &ct.Release{Env: map[string]string{}}
	fn(r)
	if r.Env["SIRENIA_PROCESS"] != "postgres" {
		t.Fatalf("SIRENIA_PROCESS=%q", r.Env["SIRENIA_PROCESS"])
	}
}

func TestPostgresUpdateReleaseAddsMetricsPortToPeersOnly(t *testing.T) {
	var fn UpdateReleaseFn
	for _, app := range SystemApps {
		if app.Name == "postgres" {
			fn = app.UpdateRelease
		}
	}
	oldEnv := map[string]string{"KEEP": "1"}
	r := &ct.Release{
		Env: map[string]string{},
		Processes: map[string]ct.ProcessType{
			"postgres": {Env: oldEnv},
			"web":      {},
		},
	}
	fn(r)
	if got := r.Processes["postgres"].Env["FLYNN_METRICS_PORT"]; got != PostgresMetricsPort {
		t.Fatalf("postgres FLYNN_METRICS_PORT=%q", got)
	}
	if r.Processes["postgres"].Env["KEEP"] != "1" {
		t.Fatalf("lost existing env: %v", r.Processes["postgres"].Env)
	}
	if _, ok := oldEnv["FLYNN_METRICS_PORT"]; ok {
		t.Fatal("must not mutate the previous release's process env")
	}
	if _, ok := r.Processes["web"].Env["FLYNN_METRICS_PORT"]; ok {
		t.Fatal("web does not serve metrics")
	}
	if _, ok := r.Env["FLYNN_METRICS_PORT"]; ok {
		t.Fatal("metrics port must be process env, not release env")
	}
}
