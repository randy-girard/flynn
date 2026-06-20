package cli

import (
	"testing"

	ct "github.com/flynn/flynn/controller/types"
	updater "github.com/flynn/flynn/updater/types"
)

func TestReleaseConfigChanged(t *testing.T) {
	before := &ct.Release{
		Env: map[string]string{"SIRENIA_PROCESS": "postgres", "PGHOST": "leader.postgres.discoverd"},
	}
	afterSame := cloneReleaseForUpdate(before)
	afterSame.Env["SIRENIA_PROCESS"] = "postgres"
	if releaseConfigChanged(before, afterSame) {
		t.Fatal("expected no config change when env values match")
	}

	afterChanged := cloneReleaseForUpdate(before)
	afterChanged.Env["SIRENIA_PROCESS"] = "postgres"
	afterChanged.Env["NEW_VAR"] = "1"
	if !releaseConfigChanged(before, afterChanged) {
		t.Fatal("expected config change when env differs")
	}
}

func TestShouldSkipUnchangedDeploy(t *testing.T) {
	release := &ct.Release{
		Env: map[string]string{"SIRENIA_PROCESS": "postgres"},
	}
	postgresUpdate := func(r *ct.Release) {
		r.Env["SIRENIA_PROCESS"] = "postgres"
	}
	for _, app := range updater.SystemApps {
		if app.Name == "postgres" {
			postgresUpdate = app.UpdateRelease
			break
		}
	}
	if postgresUpdate == nil {
		t.Fatal("postgres system app not found")
	}

	skip, migration := shouldSkipUnchangedDeploy(true, true, release, postgresUpdate)
	if !skip || migration {
		t.Fatalf("force postgres redeploy with unchanged config should skip, got skip=%v migration=%v", skip, migration)
	}

	skip, migration = shouldSkipUnchangedDeploy(true, false, release, postgresUpdate)
	if !skip || migration {
		t.Fatalf("non-force unchanged deploy should skip, got skip=%v migration=%v", skip, migration)
	}

	releaseMissing := &ct.Release{Env: map[string]string{}}
	skip, migration = shouldSkipUnchangedDeploy(true, true, releaseMissing, postgresUpdate)
	if skip || !migration {
		t.Fatalf("force deploy with pending release migration should proceed, got skip=%v migration=%v", skip, migration)
	}

	skip, migration = shouldSkipUnchangedDeploy(true, true, release, nil)
	if skip || migration {
		t.Fatalf("force redeploy without updateFn should proceed, got skip=%v migration=%v", skip, migration)
	}
}
