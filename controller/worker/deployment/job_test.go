package deployment

import (
	"os"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestHandleDeploymentClearsBuildingMeta(t *testing.T) {
	src, err := os.ReadFile("context.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "c.clearAppBuilding(deployment.AppID)") {
		t.Fatal("finished deploys must clear git-push building meta so the dashboard header drops")
	}
	if !strings.Contains(body, "StartNextQueuedDeployment") {
		t.Fatal("finished deploys must start the next queued deploy")
	}
	if !strings.Contains(body, "if deployment.FinishedAt != nil") {
		t.Fatal("HandleDeployment must no-op when finished_at is already set")
	}
	doneAt := strings.Index(body, "defer func()")
	formAt := strings.Index(body, "getting old formation")
	if doneAt < 0 || formAt < 0 || doneAt > formAt {
		t.Fatal("finished_at must be deferred before GetFormation so a missing formation cannot hold isolate_deploys")
	}
}

func TestHandleDeploymentBuffersDeployEvents(t *testing.T) {
	src, err := os.ReadFile("context.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "make(chan ct.DeploymentEvent, 32)") {
		t.Fatal("deploy event channel must be buffered so postgres event_insert cannot deadlock Perform")
	}
}

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

func TestOneDownOneUpDeployNotSkippedWhenOldReleaseActive(t *testing.T) {
	target := map[string]int{"app": 1}
	d := &DeployJob{
		Deployment:   &ct.Deployment{Strategy: "one-down-one-up", Processes: target},
		oldFormation: &ct.Formation{Processes: map[string]int{"app": 1}},
		newFormation: &ct.Formation{Processes: map[string]int{"app": 1}},
	}
	shouldSkip := processesEqual(d.newFormation.Processes, d.Processes) && !d.oldReleaseStillActive()
	if shouldSkip {
		t.Fatal("omni one-down-one-up must not skip while old routers still run")
	}
}

func TestOneByOneDeployNotSkippedWhenOldReleaseActive(t *testing.T) {
	target := map[string]int{"web": 2, "scheduler": 1}
	d := &DeployJob{
		Deployment:   &ct.Deployment{Strategy: "one-by-one", Processes: target},
		oldFormation: &ct.Formation{Processes: map[string]int{"web": 2, "scheduler": 1}},
		newFormation: &ct.Formation{Processes: map[string]int{"web": 2, "scheduler": 1}},
	}
	if !processesEqual(d.newFormation.Processes, d.Processes) {
		t.Fatal("test precondition: new formation already at target")
	}
	if !d.oldReleaseStillActive() {
		t.Fatal("old controller jobs must still count as active")
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
	if !strings.Contains(string(src), "runReleasePhase") {
		t.Fatal("Perform must run the release process type before swapping traffic")
	}
	if !strings.Contains(string(src), "WithoutReleaseProcessCounts") {
		t.Fatal("Perform must not scale a Procfile release process")
	}
}

func TestScaleOneByOneRollsOmniOneHostAtATime(t *testing.T) {
	src, err := os.ReadFile("job.go")
	if err != nil {
		t.Fatal(err)
	}
	const marker = "func (d *DeployJob) scaleOneByOne"
	start := strings.Index(string(src), marker)
	if start < 0 {
		t.Fatal("scaleOneByOne missing")
	}
	fn := string(src[start:])
	if end := strings.Index(fn, "\nfunc "); end > 0 {
		fn = fn[:end]
	}
	if !strings.Contains(fn, "processIsOmni") || !strings.Contains(fn, "scaleOmniOneDownOneUp") {
		t.Fatal("one-by-one omni processes (controller scheduler) must roll one host at a time")
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
	if !strings.Contains(fn, "processIsOmni") || !strings.Contains(fn, "scaleOmniOneDownOneUp") {
		t.Fatal("omni processes (router) must roll one host at a time instead of scaling formation 1→0 cluster-wide")
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

func TestJobStartFailureSurfacesAppArmorApply(t *testing.T) {
	hostErr := `container_linux.go:346: starting container process caused "process_linux.go:480: container init caused \"apply apparmor profile: apparmor failed to apply profile: write /proc/self/attr/exec: permission denied\""`
	err := jobStartFailure(&ct.Job{Type: "worker", HostError: &hostErr})
	want := "worker job failed to start: " + hostErr
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
	if err := jobStartFailure(&ct.Job{Type: "web"}); err == nil || err.Error() != "web job failed to start: got down job event" {
		t.Fatalf("empty host error: %v", err)
	}
}

func TestJobStartFailureIncludesExitStatus(t *testing.T) {
	exit := int32(1)
	restarts := int32(5)
	err := jobStartFailure(&ct.Job{Type: "web", Name: "web.1", ExitStatus: &exit, Restarts: &restarts})
	got := ""
	if err != nil {
		got = err.Error()
	}
	if !strings.Contains(got, "web job failed to start: exit 1") || !strings.Contains(got, "restarts 5") || !strings.Contains(got, "web.1") {
		t.Fatalf("%q", got)
	}
}

func TestJobStartFailureIncludesCrashHint(t *testing.T) {
	exit := int32(1)
	err := jobStartFailure(&ct.Job{Type: "web", Name: "web.5511", ExitStatus: &exit}, `Unable to load application: RuntimeError: Missing service adapter for "WebDav"`)
	got := ""
	if err != nil {
		got = err.Error()
	}
	if !strings.Contains(got, "exit 1") || !strings.Contains(got, "web.5511") || !strings.Contains(got, `Missing service adapter for "WebDav"`) {
		t.Fatalf("%q", got)
	}
}

func TestCrashHintFromLogLinesPrefersUnableToLoad(t *testing.T) {
	got := crashHintFromLogLines([]string{
		"Puma starting in single mode...",
		`! Unable to load application: RuntimeError: Missing service adapter for "WebDav"`,
		`/app/vendor/bundle/ruby/3.3.0/gems/activestorage-8.1.3/lib/active_storage/service/configurator.rb:39:in ` + "`rescue in resolve'" + `: Missing service adapter for "WebDav" (RuntimeError)`,
		"\tfrom /app/vendor/bundle/ruby/3.3.0/gems/puma-8.0.2/lib/puma/cli.rb:73:in `run'",
		`cannot load such file -- rexml/document (LoadError)`,
		`t=2026-10-05T13:57:22+0000 lvl=info msg="job exited" component=containerinit status=1`,
		"web process crashed (exit 1)",
		"metrics cpu_percent=0.03 memory_bytes=194359296",
	})
	if got != `Unable to load application: RuntimeError: Missing service adapter for "WebDav"` {
		t.Fatalf("%q", got)
	}
}

func TestCrashHintFromLogLinesLoadError(t *testing.T) {
	got := crashHintFromLogLines([]string{
		`cannot load such file -- rexml/document (LoadError)`,
		"web process crashed (exit 1)",
	})
	if got != `cannot load such file -- rexml/document (LoadError)` {
		t.Fatalf("%q", got)
	}
}
