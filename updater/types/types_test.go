package updater

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestSystemAppsUpgradeOrder(t *testing.T) {
	// discoverd and blobstore must land before the controller so
	// post-upgrade deploys have discovery and image storage available; the
	// controller lands before the apps that call its API (EDGE-30).
	wantPrefix := []string{"discoverd", "blobstore", "controller", "taffy", "router", "gitreceive", "tarreceive"}
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

// EDGE-30: the router used to update before the controller. The router API
// required the cluster key since EDGE-04, and the scheduler still running was
// the old one, which sent none: it got 401 and reconnected every 100 ms until
// the controller deploy replaced it, and jobs waited the backend-up and drain
// fallbacks meanwhile. A new router could likewise call a controller endpoint
// only the new controller serves. The controller runs every later deploy (the
// deployer and scheduler) and serves the API these apps call, so it updates
// first and they update after it; each still has to work with the previous
// release of the other because an update can stop between two apps.
func TestControllerUpdatesBeforeRouterAPIConsumers(t *testing.T) {
	pos := make(map[string]int, len(SystemApps))
	for i, app := range SystemApps {
		pos[app.Name] = i
	}
	before := func(first, then, why string) {
		t.Helper()
		f, ok1 := pos[first]
		s, ok2 := pos[then]
		if !ok1 || !ok2 {
			t.Fatalf("missing %s or %s in SystemApps", first, then)
		}
		if f >= s {
			t.Errorf("%s (at %d) must update before %s (at %d): %s", first, f, then, s, why)
		}
	}
	if SystemApps[0].Name != "discoverd" {
		t.Errorf("discoverd must update first, got %s", SystemApps[0].Name)
	}
	before("blobstore", "controller", "every deploy stores and reads image artifacts in the blobstore")
	for _, app := range []string{"router", "gitreceive", "tarreceive", "taffy", "logaggregator", "status"} {
		before("controller", app, app+" calls the controller API, and the new controller's scheduler and deployer roll it")
	}
	before("controller", "postgres", "the controller's sirenia deployer moves the postgres peers")
	for _, app := range SystemApps {
		if app.ImageOnly || app.Name == "status" {
			continue
		}
		before(app.Name, "status", "status checks every other app")
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
