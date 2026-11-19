package main

import (
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

func newSyncTestScheduler() *Scheduler {
	l := log15.New()
	l.SetHandler(log15.DiscardHandler())
	s := NewScheduler(newTestCluster(nil), testutils.NewFakeControllerClient(), newFakeDiscoverd(true), l)
	s.isLeader = typeconv.BoolPtr(true)
	s.hosts["h2"] = &Host{ID: "h2", Healthy: true}
	return s
}

func flannelFormation(processes map[string]int, updated time.Time) *ct.ExpandedFormation {
	return &ct.ExpandedFormation{
		App:       &ct.App{ID: "flannel", Name: "flannel"},
		Release:   &ct.Release{ID: "r-new", Processes: map[string]ct.ProcessType{"app": {Omni: true}}},
		Processes: processes,
		UpdatedAt: updated,
	}
}

// During `update` the updater PUTs the new flannel release's formation and
// starts the job. A periodic formation sync whose controller request began
// before the PUT can be applied after the formation event arrived. The
// formation is missing from that older snapshot, so the sync treated it as
// inactive and zeroed it ("expected=map[] actual=map[app:1]"), after which
// the scheduler stopped every fresh flannel job as scale-down.
func TestFormationSyncDoesNotZeroFormationNewerThanItsSnapshot(t *testing.T) {
	s := newSyncTestScheduler()

	fetch := s.loadFormationFetch() // controller has no active formation yet
	s.HandleFormationChange(flannelFormation(map[string]int{"app": 1}, time.Now()))

	s.applyFormationFetch(fetch, 0)

	f := s.formations.Get("flannel", "r-new")
	if f == nil {
		t.Fatal("formation missing")
	}
	if got := f.GetProcesses()["app"]; got != 1 {
		t.Fatalf("formation processes = %v after a sync older than the formation; want app:1", f.GetProcesses())
	}
}

// A formation that a sync did zero must come back on the next sync that lists
// it. handleFormation compared the controller's processes with the original
// (untouched) counts, saw no change and left the formation at zero until it
// was changed again.
func TestFormationSyncRestoresFormationItZeroed(t *testing.T) {
	s := newSyncTestScheduler()
	ef := flannelFormation(map[string]int{"app": 1}, time.Now())
	s.HandleFormationChange(ef)

	// a sync that really does not list the formation (it was inactive)
	s.applyFormationFetch(s.loadFormationFetch(), 0)
	if !s.formations.Get("flannel", "r-new").GetProcesses().IsEmpty() {
		t.Fatalf("an inactive formation should be scaled down")
	}

	// the controller lists it again, unchanged since the scheduler first saw it
	fetch := s.loadFormationFetch()
	fetch.formations = []*ct.ExpandedFormation{flannelFormation(map[string]int{"app": 1}, ef.UpdatedAt)}
	s.applyFormationFetch(fetch, 0)
	if got := s.formations.Get("flannel", "r-new").GetProcesses()["app"]; got != 1 {
		t.Fatalf("formation processes = %v after it became active again; want app:1", s.formations.Get("flannel", "r-new").GetProcesses())
	}
}
