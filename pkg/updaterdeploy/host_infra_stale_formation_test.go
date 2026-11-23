package updaterdeploy

import (
	"errors"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
)

// swappedInfraController is the state EDGE-38 left on the dev cluster: the
// app already points at rel-new (pulled image) and runs there on every host,
// but the old release's formation is still at app=1 with every host avoided,
// so the full-strength probe expects twice the jobs that can ever run.
func swappedInfraController(t *testing.T, newCount int) *fakeInfraController {
	c := newFakeInfraController(t, "layer-old")
	c.artifacts["art-new"] = newInfraImage(t)
	next := &ct.Release{ID: "rel-new", ArtifactIDs: []string{"art-new"}}
	c.releases = append(c.releases, next)
	c.release = next
	c.formations["rel-old"].Tags = map[string]map[string]string{"app": {ct.FormationAvoidHostIDsTag: "n1,n2"}}
	c.formations["rel-new"] = &ct.Formation{AppID: "app-flannel", ReleaseID: "rel-new", Processes: map[string]int{"app": newCount}}
	return c
}

func TestHostInfraRolloutZeroesStaleFormationWhenAlreadySwapped(t *testing.T) {
	c := swappedInfraController(t, 1)
	h1 := &fakeInfraHost{id: "n1", jobs: map[string]host.ActiveJob{"a": infraJob("a", "rel-new", "layer-new")}}
	h2 := &fakeInfraHost{id: "n2", jobs: map[string]host.ActiveJob{"b": infraJob("b", "rel-new", "layer-new")}}
	c.hosts = []*fakeInfraHost{h1, h2}

	runHostInfraRollout(t, c, []hostInfraHost{h1, h2}, newInfraImage(t))

	if n := c.formations["rel-old"].Processes["app"]; n != 0 {
		t.Fatalf("stale formation app=%d, want 0", n)
	}
	if n := c.formations["rel-new"].Processes["app"]; n != 1 {
		t.Fatalf("current formation app=%d, want 1", n)
	}
	if c.release.ID != "rel-new" || len(c.created) != 0 {
		t.Fatalf("release=%s created=%d, want rel-new kept", c.release.ID, len(c.created))
	}
	if len(c.puts) != 1 || c.puts[0].ReleaseID != "rel-old" {
		t.Fatalf("puts=%+v, want only rel-old zeroed", c.puts)
	}
	for _, h := range []*fakeInfraHost{h1, h2} {
		if len(h.stopped) != 0 || len(h.added) != 0 {
			t.Fatalf("%s: stopped=%v added=%d, want the running jobs left alone", h.id, h.stopped, len(h.added))
		}
		assertHostRunsRelease(t, h, "rel-new")
	}
}

// Zeroing every other formation when the current one is empty would scale
// the app to nothing; leave it for an operator.
func TestHostInfraRolloutKeepsOtherFormationsWhenCurrentIsEmpty(t *testing.T) {
	c := swappedInfraController(t, 0)
	h := &fakeInfraHost{id: "n1", jobs: map[string]host.ActiveJob{"a": infraJob("a", "rel-new", "layer-new")}}

	runHostInfraRollout(t, c, []hostInfraHost{h}, newInfraImage(t))

	if n := c.formations["rel-old"].Processes["app"]; n != 1 {
		t.Fatalf("rel-old app=%d, want it kept while rel-new is empty", n)
	}
	if len(c.puts) != 0 {
		t.Fatalf("puts=%+v, want none", c.puts)
	}
}

// A job of the zeroed release still running beside the current one is a
// duplicate on the same host ports. The rerun stops it so the host is back at
// one job, whichever of the two the host lists first.
func TestHostInfraRolloutStopsStaleJobBesideCurrent(t *testing.T) {
	for i := 0; i < 30; i++ {
		c := swappedInfraController(t, 1)
		h := &fakeInfraHost{id: "n1", jobs: map[string]host.ActiveJob{
			"cur":   infraJob("cur", "rel-new", "layer-new"),
			"stale": infraJob("stale", "rel-old", "layer-old"),
		}}

		runHostInfraRollout(t, c, []hostInfraHost{h}, newInfraImage(t))

		if len(h.stopped) != 1 || h.stopped[0] != "stale" {
			t.Fatalf("run %d: stopped=%v, want only the stale job", i, h.stopped)
		}
		if len(h.added) != 0 {
			t.Fatalf("run %d: added %d jobs, want none", i, len(h.added))
		}
		assertHostRunsRelease(t, h, "rel-new")
	}
}

// failZeroController refuses to scale rel-old to zero, the way every write to
// the old discoverd formation failed for three minutes on the dev cluster.
type failZeroController struct {
	*fakeInfraController
	fail bool
}

func (c *failZeroController) PutFormation(f *ct.Formation) error {
	if c.fail && f.ReleaseID == "rel-old" && processCount(f.Processes) == 0 {
		return errors.New("httpclient: raw req: unexpected status 503")
	}
	return c.fakeInfraController.PutFormation(f)
}

// If the old formation cannot be zeroed, the app must stay on the old
// release, so the next update takes the switching path again and finishes
// the move instead of returning early with the old formation still active.
func TestHostInfraFailedFinishIsRetriedByNextRun(t *testing.T) {
	budget := hostInfraFormationBudget
	hostInfraFormationBudget = 0
	t.Cleanup(func() { hostInfraFormationBudget = budget })

	fake := newFakeInfraController(t, "layer-old")
	c := &failZeroController{fakeInfraController: fake, fail: true}
	h1 := &fakeInfraHost{id: "n1", jobs: map[string]host.ActiveJob{"a": infraJob("a", "rel-old", "layer-old")}}
	h2 := &fakeInfraHost{id: "n2", jobs: map[string]host.ActiveJob{"b": infraJob("b", "rel-old", "layer-old")}}
	fake.hosts = []*fakeInfraHost{h1, h2}
	img := newInfraImage(t)
	images := map[string]*ct.Artifact{"flannel": img}

	grace, delay := hostInfraSchedulerGrace, hostInfraFormationRetryDelay
	hostInfraSchedulerGrace, hostInfraFormationRetryDelay = 0, time.Millisecond
	t.Cleanup(func() { hostInfraSchedulerGrace, hostInfraFormationRetryDelay = grace, delay })

	r, err := prepareHostInfraRollout(c, "flannel", img, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceHostInfraJobs([]hostInfraHost{h1, h2}, images, map[string]*hostInfraRollout{"flannel": r}, false, nil); err == nil {
		t.Fatal("want the failed zero write to fail the rollout")
	}
	if fake.release.ID != "rel-old" {
		t.Fatalf("app release=%s after a failed finish, want rel-old so the next run resumes", fake.release.ID)
	}

	c.fail = false
	r, err = prepareHostInfraRollout(c, "flannel", img, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceHostInfraJobs([]hostInfraHost{h1, h2}, images, map[string]*hostInfraRollout{"flannel": r}, false, nil); err != nil {
		t.Fatal(err)
	}
	newID := fake.created[0].ID
	if len(fake.created) != 1 || fake.release.ID != newID {
		t.Fatalf("created=%d release=%s, want reuse of %s", len(fake.created), fake.release.ID, newID)
	}
	if n := fake.formations["rel-old"].Processes["app"]; n != 0 {
		t.Fatalf("rel-old app=%d after rerun, want 0", n)
	}
	assertHostRunsRelease(t, h1, newID)
	assertHostRunsRelease(t, h2, newID)
}
