package deployment

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestOmniRollPlanThreeHosts(t *testing.T) {
	got := omniRollPlan([]string{"c", "a", "b"}, nil)
	want := []omniRollStep{
		{OldHostIDs: []string{"b", "c"}, NewHostIDs: []string{"a"}},
		{OldHostIDs: []string{"c"}, NewHostIDs: []string{"a", "b"}},
		{OldZero: true, ClearNewTags: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %#v, want %#v", got, want)
	}
}

func TestOmniRollPlanResume(t *testing.T) {
	got := omniRollPlan([]string{"b", "c"}, []string{"a"})
	want := []omniRollStep{
		{OldHostIDs: []string{"c"}, NewHostIDs: []string{"a", "b"}},
		{OldZero: true, ClearNewTags: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resume plan = %#v, want %#v", got, want)
	}
}

func TestOmniRollPlanSingleHost(t *testing.T) {
	got := omniRollPlan([]string{"a"}, nil)
	if len(got) != 1 || !got[0].OldZero || !got[0].ClearNewTags {
		t.Fatalf("single host must stop-then-start in one step, got %#v", got)
	}
}

func TestOmniRollPlanEmpty(t *testing.T) {
	if plan := omniRollPlan(nil, []string{"a"}); plan != nil {
		t.Fatalf("no old hosts: %v", plan)
	}
}

func TestJobHoldsHostPort(t *testing.T) {
	if !jobHoldsHostPort(&ct.Job{State: ct.JobStateStopping}) {
		t.Fatal("stopping jobs still hold :80/:443")
	}
	if jobHoldsHostPort(&ct.Job{State: ct.JobStateDown}) {
		t.Fatal("down jobs have released the port")
	}
}

func TestProcessIsOmni(t *testing.T) {
	d := &DeployJob{
		newRelease: &ct.Release{Processes: map[string]ct.ProcessType{"app": {Omni: true}}},
		oldRelease: &ct.Release{Processes: map[string]ct.ProcessType{"app": {Omni: true}}},
	}
	if !d.processIsOmni("app") {
		t.Fatal("router app process is omni")
	}
	if d.processIsOmni("web") {
		t.Fatal("missing type is not omni")
	}
}

func TestOldReleaseStillActive(t *testing.T) {
	d := &DeployJob{oldFormation: &ct.Formation{Processes: map[string]int{"app": 1}}}
	if !d.oldReleaseStillActive() {
		t.Fatal("expected old release active")
	}
	d.oldFormation.Processes["app"] = 0
	if d.oldReleaseStillActive() {
		t.Fatal("expected old release inactive")
	}
}

func TestHostIDProcessTags(t *testing.T) {
	tags := hostIDProcessTags("app", []string{"b", "a"}, false)
	if got := tags["app"][ct.FormationHostIDsTag]; got != "b,a" {
		t.Fatalf("tags = %q, want b,a", got)
	}
	cleared := hostIDProcessTags("app", []string{"a"}, true)
	if _, ok := cleared["app"][ct.FormationHostIDsTag]; ok {
		t.Fatal("clear must drop flynn-host-ids so new hosts get omni jobs")
	}
}

func TestLeftoverOmniJobsSkipsAllowedAndDown(t *testing.T) {
	allowed := map[string]struct{}{"keep": {}}
	jobs := []*ct.Job{
		{ReleaseID: "old", Type: "scheduler", HostID: "drop", State: ct.JobStateUp, UUID: "u1"},
		{ReleaseID: "old", Type: "scheduler", HostID: "keep", State: ct.JobStateUp, UUID: "u2"},
		{ReleaseID: "old", Type: "scheduler", HostID: "gone", State: ct.JobStateDown, UUID: "u3"},
		{ReleaseID: "new", Type: "scheduler", HostID: "drop", State: ct.JobStateUp, UUID: "u4"},
		{ReleaseID: "old", Type: "web", HostID: "drop", State: ct.JobStateStopping, UUID: "u5"},
		{ReleaseID: "old", Type: "scheduler", HostID: "stuck", State: ct.JobStateStopping, UUID: "u6"},
	}
	got := leftoverOmniJobs(jobs, "old", "scheduler", allowed)
	if len(got) != 2 {
		t.Fatalf("leftover = %#v", got)
	}
	if got[0].UUID != "u1" || got[1].UUID != "u6" {
		t.Fatalf("leftover ids = %s %s", got[0].UUID, got[1].UUID)
	}
}

func TestJobStopIDPrefersUUID(t *testing.T) {
	if jobStopID(&ct.Job{UUID: "uuid", ID: "host-uuid"}) != "uuid" {
		t.Fatal("DeleteJob uses the controller UUID")
	}
	if jobStopID(&ct.Job{ID: "host-only"}) != "host-only" {
		t.Fatal("fall back to cluster id when uuid is empty")
	}
}

func TestOmniStopWaitIsNotADeployTimeout(t *testing.T) {
	if omniStopWaitLimit > 45*time.Second {
		t.Fatalf("omni leftover wait %s is a timeout band-aid; force-stop should finish in seconds", omniStopWaitLimit)
	}
	if omniForceStopAfter > 15*time.Second {
		t.Fatalf("graceful omni stop wait %s too long for local smoke", omniForceStopAfter)
	}
	if omniPortReleaseSettle > 3*time.Second {
		t.Fatalf("port settle %s too long", omniPortReleaseSettle)
	}
}

func TestProcessHostNetwork(t *testing.T) {
	d := &DeployJob{
		newRelease: &ct.Release{Processes: map[string]ct.ProcessType{
			"app":       {Omni: true, HostNetwork: true},
			"scheduler": {Omni: true},
		}},
	}
	if !d.processHostNetwork("app") {
		t.Fatal("router app process uses the host network")
	}
	if d.processHostNetwork("scheduler") {
		t.Fatal("controller scheduler is overlay omni")
	}
}

func TestOmniLastHostStartsNewBeforeOldZero(t *testing.T) {
	src, err := os.ReadFile("omni_rolling.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "scaleNewOmni") || !strings.Contains(body, "OldZero") {
		t.Fatal("last omni host must start the new scheduler before scaling old to zero")
	}
	if !strings.Contains(body, "processHostNetwork") {
		t.Fatal("host-network omni (router) must keep stop-then-start on the last host")
	}
	if !strings.Contains(body, "waitNewOmniJobsUp") {
		t.Fatal("omni must wait on JobList for new jobs, not ScaleRequest complete")
	}
}

func TestLeftoverJobReleasedOnHost(t *testing.T) {
	if leftoverJobReleasedOnHost(nil) {
		t.Fatal("nil error is still in flight")
	}
	if leftoverJobReleasedOnHost(fmt.Errorf("connection refused")) {
		t.Fatal("transient errors must not look like a released job")
	}
	if !leftoverJobReleasedOnHost(fmt.Errorf("host: job is already stopped")) {
		t.Fatal("already-stopped host jobs have released the port")
	}
	if !leftoverJobReleasedOnHost(fmt.Errorf("host: unknown job")) {
		t.Fatal("missing host jobs have released the port")
	}
	if !leftoverJobReleasedOnHost(fmt.Errorf("controller: not found")) {
		t.Fatal("not-found jobs have released the port")
	}
}
