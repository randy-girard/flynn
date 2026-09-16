package deployment

import (
	"os"
	"strings"
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

func TestOneDownOneUpIsAKnownStrategy(t *testing.T) {
	src, err := os.ReadFile("job.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `case "one-down-one-up":`) {
		t.Fatal("Perform must dispatch one-down-one-up (redis appliance strategy)")
	}
}

func TestScaleOneDownOneUpStopsOldBeforeStartingNew(t *testing.T) {
	src, err := os.ReadFile("job.go")
	if err != nil {
		t.Fatal(err)
	}
	const marker = "func (d *DeployJob) scaleOneDownOneUp"
	start := strings.Index(string(src), marker)
	if start < 0 {
		t.Fatal("scaleOneDownOneUp missing")
	}
	fn := string(src[start:])
	if end := strings.Index(fn, "\nfunc "); end > 0 {
		fn = fn[:end]
	}
	down := strings.Index(fn, "scaleOldFormationDownByOne")
	up := strings.Index(fn, "scaleNewFormationUpByOne")
	if down < 0 || up < 0 || down > up {
		t.Fatal("one-down-one-up must scale the old formation down before starting the replacement (else redis findVolume allocates an empty /data)")
	}
}

func TestSireniaSingletonScalesNewBeforeOld(t *testing.T) {
	src, err := os.ReadFile("sirenia.go")
	if err != nil {
		t.Fatal(err)
	}
	const marker = "func (d *DeployJob) deploySireniaSingleton"
	start := strings.Index(string(src), marker)
	if start < 0 {
		t.Fatal("deploySireniaSingleton missing")
	}
	fn := string(src[start:])
	if end := strings.Index(fn, "\nfunc "); end > 0 {
		fn = fn[:end]
	}
	up := strings.Index(fn, "scaling new formation up")
	down := strings.Index(fn, "scaling old formation down")
	if up < 0 || down < 0 || up > down {
		t.Fatal("singleton sirenia deploy must PutFormation the new release before scaling the old peer to zero")
	}
}

func TestAllAtOnceStartsNewBeforeStoppingOld(t *testing.T) {
	src, err := os.ReadFile("all_at_once.go")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Index(string(src), "scaleNewRelease()")
	down := strings.Index(string(src), "scaleOldRelease(false)")
	if up < 0 || down < 0 || up > down {
		t.Fatal("all-at-once must start new jobs while old jobs still run")
	}
}
