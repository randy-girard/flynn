package deployment

import (
	"testing"

	ct "github.com/flynn/flynn/controller/types"
)

func TestSireniaOldReleaseActive(t *testing.T) {
	d := &DeployJob{
		Deployment: &ct.Deployment{
			Strategy:  "sirenia",
			Processes: map[string]int{"mariadb": 3},
		},
		oldFormation: &ct.Formation{Processes: map[string]int{"mariadb": 3}},
		newFormation: &ct.Formation{Processes: map[string]int{"mariadb": 3}},
		newRelease:   &ct.Release{Env: map[string]string{"SIRENIA_PROCESS": "mariadb"}},
	}
	if !d.sireniaOldReleaseActive() {
		t.Fatal("expected old release to be active when formation still scaled")
	}

	d.oldFormation.Processes["mariadb"] = 0
	if d.sireniaOldReleaseActive() {
		t.Fatal("expected old release inactive after scale down")
	}
}

func TestSireniaDeployNotSkippedWhenOldReleaseActive(t *testing.T) {
	target := map[string]int{"mariadb": 3, "web": 2}
	newForm := map[string]int{"mariadb": 3, "web": 2}
	oldForm := map[string]int{"mariadb": 3, "web": 2}

	if !processesEqual(newForm, target) {
		t.Fatal("test precondition: formations should match target")
	}

	d := &DeployJob{
		Deployment:   &ct.Deployment{Strategy: "sirenia", Processes: target},
		oldFormation: &ct.Formation{Processes: oldForm},
		newFormation: &ct.Formation{Processes: newForm},
		newRelease:   &ct.Release{Env: map[string]string{"SIRENIA_PROCESS": "mariadb"}},
	}

	shouldSkip := processesEqual(d.newFormation.Processes, d.Processes) &&
		(d.Strategy != "sirenia" || !d.sireniaOldReleaseActive())
	if shouldSkip {
		t.Fatal("sirenia deploy must not be skipped while old release formation is still active")
	}
}
