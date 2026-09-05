package updater

import (
	"testing"

	ct "github.com/flynn/flynn/controller/types"
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
	for _, required := range []string{"postgres", "redis", "status", "logaggregator"} {
		if _, ok := byName[required]; !ok {
			t.Fatalf("missing required system app %q", required)
		}
	}
	for _, optional := range []string{"mariadb", "mongodb", "kafka", "clickhouse"} {
		app, ok := byName[optional]
		if !ok {
			t.Fatalf("missing optional system app %q", optional)
		}
		if !app.Optional {
			t.Fatalf("%s should be Optional", optional)
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
