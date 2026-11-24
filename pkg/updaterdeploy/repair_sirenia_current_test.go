package updaterdeploy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
)

// sireniaPrimaryOn fakes the discoverd service meta of a sirenia cluster
// whose primary runs from release.
func sireniaPrimaryOn(t *testing.T, release string) func() {
	t.Helper()
	meta, err := json.Marshal(struct {
		Primary *discoverd.Instance   `json:"Primary"`
		Sync    *discoverd.Instance   `json:"Sync"`
		Async   []*discoverd.Instance `json:"Async"`
	}{
		Primary: &discoverd.Instance{Meta: map[string]string{"FLYNN_RELEASE_ID": release, "POSTGRES_ID": "primary"}},
		Sync:    &discoverd.Instance{Meta: map[string]string{"FLYNN_RELEASE_ID": release, "POSTGRES_ID": "sync"}},
		Async:   []*discoverd.Instance{{Meta: map[string]string{"FLYNN_RELEASE_ID": release, "POSTGRES_ID": "async"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	orig := discoverdNewService
	discoverdNewService = func(name string) discoverdService {
		return &fakeDiscoverdService{meta: &discoverd.ServiceMeta{Data: meta}}
	}
	return func() { discoverdNewService = orig }
}

// The EDGE-68 shape after the host fixer scaled the current release up
// (20:23:59): the engines run from release A, the primary's release, and the
// app's current release B also asks for three. The repair used to zero B
// (21:03:14), which left the current release at postgres=0 for good.
func TestRepairOrphanSireniaKeepsTheCurrentRelease(t *testing.T) {
	const (
		appID = "postgres-app"
		relA  = "0a8a5e46"
		relB  = "e0165d85"
	)
	defer sireniaPrimaryOn(t, relA)()
	ctrl := &fakeRepairController{
		apps:        []*ct.App{{ID: appID, Name: "postgres"}},
		appReleases: map[string]string{appID: relB},
		releases: map[string]*ct.Release{
			relA: {ID: relA, Env: map[string]string{"SIRENIA_PROCESS": "postgres"}},
			relB: {ID: relB, Env: map[string]string{"SIRENIA_PROCESS": "postgres"}},
		},
		formations: map[string][]*ct.Formation{
			appID: {
				{AppID: appID, ReleaseID: relA, Processes: map[string]int{"postgres": 3}},
				{AppID: appID, ReleaseID: relB, Processes: map[string]int{"postgres": 3, "web": 2}},
			},
		},
	}
	if err := RepairOrphanSireniaFormations(ctrl, log15.New()); err != nil {
		t.Fatal(err)
	}
	for _, f := range ctrl.puts {
		t.Errorf("formation %s was scaled to %v; the primary runs from %s and %s is current, so neither may be reduced", f.ReleaseID, f.Processes, relA, relB)
	}
}

// The stuck EDGE-68 shape: the current release runs no engines and an older
// release runs all of them. Nothing is zeroed and the mismatch is reported.
func TestRepairOrphanSireniaCurrentReleaseAtZero(t *testing.T) {
	const (
		appID = "postgres-app"
		relA  = "0a8a5e46"
		relB  = "e0165d85"
	)
	defer sireniaPrimaryOn(t, relA)()
	ctrl := &fakeRepairController{
		apps:        []*ct.App{{ID: appID, Name: "postgres"}},
		appReleases: map[string]string{appID: relB},
		releases: map[string]*ct.Release{
			relA: {ID: relA, Env: map[string]string{"SIRENIA_PROCESS": "postgres"}},
			relB: {ID: relB, Env: map[string]string{"SIRENIA_PROCESS": "postgres"}},
		},
		formations: map[string][]*ct.Formation{
			appID: {
				{AppID: appID, ReleaseID: relA, Processes: map[string]int{"postgres": 3}},
				{AppID: appID, ReleaseID: relB, Processes: map[string]int{"postgres": 0, "web": 2}},
			},
		},
	}
	var buf bytes.Buffer
	log := log15.New()
	log.SetHandler(log15.StreamHandler(&buf, log15.LogfmtFormat()))
	if err := RepairOrphanSireniaFormations(ctrl, log); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.puts) != 0 {
		t.Fatalf("expected no formation updates, got %d (%s %v)", len(ctrl.puts), ctrl.puts[0].ReleaseID, ctrl.puts[0].Processes)
	}
	if !strings.Contains(buf.String(), "sirenia engines run from a release that is not the app's current release") {
		t.Fatalf("the mismatch was not logged:\n%s", buf.String())
	}

	states, err := SireniaFormationStates(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Fatalf("expected one sirenia app, got %d", len(states))
	}
	s := states[0]
	if !s.Mismatch() || !s.Stuck() {
		t.Fatalf("state %+v: want a mismatch with the current release at zero", s)
	}
	if s.CurrentRelease != relB || s.CurrentCount != 0 || s.PrimaryRelease != relA || s.PrimaryCount != 3 || s.ProcessType != "postgres" {
		t.Fatalf("state %+v", s)
	}
}

// The ordinary repair still runs when the primary is on the current release:
// a stray formation from an aborted rolling deploy is zeroed.
func TestRepairOrphanSireniaZeroesAStrayWhenThePrimaryIsCurrent(t *testing.T) {
	const (
		appID = "postgres-app"
		relB  = "current"
		relX  = "stray"
	)
	defer sireniaPrimaryOn(t, relB)()
	ctrl := &fakeRepairController{
		apps:        []*ct.App{{ID: appID, Name: "postgres"}},
		appReleases: map[string]string{appID: relB},
		releases: map[string]*ct.Release{
			relB: {ID: relB, Env: map[string]string{"SIRENIA_PROCESS": "postgres"}},
		},
		formations: map[string][]*ct.Formation{
			appID: {
				{AppID: appID, ReleaseID: relB, Processes: map[string]int{"postgres": 3, "web": 2}},
				{AppID: appID, ReleaseID: relX, Processes: map[string]int{"postgres": 1}},
			},
		},
	}
	if err := RepairOrphanSireniaFormations(ctrl, log15.New()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.puts) != 1 || ctrl.puts[0].ReleaseID != relX || ctrl.puts[0].Processes["postgres"] != 0 {
		t.Fatalf("puts = %v, want only %s scaled to postgres=0", ctrl.puts, relX)
	}
	states, err := SireniaFormationStates(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].Mismatch() || states[0].Stuck() {
		t.Fatalf("states = %+v, want one healthy app", states)
	}
}

// With the primary on the current release but that formation at zero (it is
// being scaled down, or something else zeroed it), the other formations are
// what runs the engines; they are left alone.
func TestRepairOrphanSireniaKeepsOthersWhenTheCurrentFormationIsZero(t *testing.T) {
	const (
		appID = "postgres-app"
		relB  = "current"
		relX  = "other"
	)
	defer sireniaPrimaryOn(t, relB)()
	ctrl := &fakeRepairController{
		apps:        []*ct.App{{ID: appID, Name: "postgres"}},
		appReleases: map[string]string{appID: relB},
		releases: map[string]*ct.Release{
			relB: {ID: relB, Env: map[string]string{"SIRENIA_PROCESS": "postgres"}},
		},
		formations: map[string][]*ct.Formation{
			appID: {
				{AppID: appID, ReleaseID: relB, Processes: map[string]int{"postgres": 0, "web": 2}},
				{AppID: appID, ReleaseID: relX, Processes: map[string]int{"postgres": 3}},
			},
		},
	}
	if err := RepairOrphanSireniaFormations(ctrl, log15.New()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.puts) != 0 {
		t.Fatalf("puts = %v, want none: zeroing %s would stop every engine", ctrl.puts, relX)
	}
}

// An app with no current release (never deployed) is not repaired.
func TestRepairOrphanSireniaSkipsAnAppWithoutACurrentRelease(t *testing.T) {
	const appID = "postgres-app"
	defer sireniaPrimaryOn(t, "a")()
	ctrl := &fakeRepairController{
		apps: []*ct.App{{ID: appID, Name: "postgres"}},
		releases: map[string]*ct.Release{
			"a": {ID: "a", Env: map[string]string{"SIRENIA_PROCESS": "postgres"}},
		},
		formations: map[string][]*ct.Formation{
			appID: {
				{AppID: appID, ReleaseID: "a", Processes: map[string]int{"postgres": 3}},
				{AppID: appID, ReleaseID: "b", Processes: map[string]int{"postgres": 3}},
			},
		},
	}
	if err := RepairOrphanSireniaFormations(ctrl, log15.New()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.puts) != 0 {
		t.Fatalf("puts = %v, want none", ctrl.puts)
	}
}

func TestPlanOrphanSireniaRepairNeverZeroesCurrentOrPrimary(t *testing.T) {
	forms := []*ct.Formation{
		{ReleaseID: "a", Processes: map[string]int{"postgres": 3}},
		{ReleaseID: "b", Processes: map[string]int{"postgres": 3, "web": 2}},
		{ReleaseID: "c", Processes: map[string]int{"postgres": 1}},
	}
	for _, tc := range []struct{ current, primary string }{
		{"a", "a"}, {"b", "b"}, {"a", "b"}, {"b", "a"}, {"c", "a"}, {"", "a"}, {"b", ""},
	} {
		zero := PlanOrphanSireniaRepair(tc.current, tc.primary, "postgres", forms)
		for _, z := range zero {
			if z.ReleaseID == tc.current || z.ReleaseID == tc.primary {
				t.Errorf("current=%q primary=%q: plan zeroes %s", tc.current, tc.primary, z.ReleaseID)
			}
		}
		if tc.current != tc.primary && len(zero) != 0 {
			t.Errorf("current=%q primary=%q: plan zeroes %d formations, want none on a mismatch", tc.current, tc.primary, len(zero))
		}
	}
	// the input formations are not modified
	if forms[2].Processes["postgres"] != 1 {
		t.Fatal("PlanOrphanSireniaRepair changed its input")
	}
}

// EDGE-76: a sirenia rollback that keeps a release's engines (mid-roll peers
// that did not hand back, or a primary on the failed release) leaves more
// than one formation running engines. The state lists the formations other
// than the current and the primary's release that still ask for engines.
func TestSireniaFormationStatesListsOtherEngineFormations(t *testing.T) {
	const (
		appID = "postgres-app"
		relB  = "current"
		relX  = "kept"
		relY  = "zeroed"
	)
	defer sireniaPrimaryOn(t, relB)()
	ctrl := &fakeRepairController{
		apps:        []*ct.App{{ID: appID, Name: "postgres"}},
		appReleases: map[string]string{appID: relB},
		releases: map[string]*ct.Release{
			relB: {ID: relB, Env: map[string]string{"SIRENIA_PROCESS": "postgres"}},
		},
		formations: map[string][]*ct.Formation{
			appID: {
				{AppID: appID, ReleaseID: relB, Processes: map[string]int{"postgres": 3, "web": 2}},
				{AppID: appID, ReleaseID: relX, Processes: map[string]int{"postgres": 2}},
				{AppID: appID, ReleaseID: relY, Processes: map[string]int{"postgres": 0, "web": 1}},
			},
		},
	}
	states, err := SireniaFormationStates(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Fatalf("states = %+v", states)
	}
	s := states[0]
	if s.Mismatch() {
		t.Fatalf("state %+v: the primary is on the current release", s)
	}
	if len(s.Others) != 1 || s.Others[relX] != 2 {
		t.Fatalf("Others = %v, want only %s=2", s.Others, relX)
	}
	if !s.Split() {
		t.Fatalf("state %+v: two formations run engines, want Split", s)
	}
}
