package deployment

import (
	"strings"
	"testing"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/updaterdeploy"
)

// The dev-cluster release ids from EDGE-68/EDGE-72/EDGE-73.
const (
	relEngines = "0a8a5e46" // an older release; its formation runs the three engines
	relCurrent = "e0165d85" // the app's current release
	relNew     = "12384ef4" // the release the update deploys
)

func (f *fakeFormationCluster) PutFormation(form *ct.Formation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.putErr[form.ReleaseID]; err != nil {
		return err
	}
	if err := f.failCall("put_formation", form.ReleaseID); err != nil {
		return err
	}
	f.formations[form.ReleaseID] = copyFormation(form)
	if f.observe != nil {
		f.observe(f)
	}
	return nil
}

func (f *fakeFormationCluster) GetAppRelease(appID string) (*ct.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appRelease == "" {
		return nil, controller.ErrNotFound
	}
	return f.releases[f.appRelease], nil
}

// fakeSireniaCluster stands in for discoverd and the sirenia peers: the
// release each member runs from, the first being the primary.
type fakeSireniaCluster struct {
	members    []string
	lowMembers int
}

func (s *fakeSireniaCluster) primary() string {
	if len(s.members) == 0 {
		return ""
	}
	return s.members[0]
}

func (s *fakeSireniaCluster) note() {
	if len(s.members) < s.lowMembers {
		s.lowMembers = len(s.members)
	}
}

// install replaces the discoverd-backed hooks: the orphan repair runs the
// updater's real plan against the fake controller, and the HA roll starts
// one new peer per engine (PutFormation on the new release, as startInstance
// does) before it stops one old peer, then hands the primary to the new
// release. Scaling the replaced formations down is left to deploySirenia.
func (s *fakeSireniaCluster) install(t *testing.T, f *fakeFormationCluster) {
	t.Helper()
	s.lowMembers = len(s.members)
	origRepair, origRoll, origPrimary := repairOrphanSireniaFormations, sireniaRollHA, sireniaPrimaryRelease
	t.Cleanup(func() {
		repairOrphanSireniaFormations, sireniaRollHA, sireniaPrimaryRelease = origRepair, origRoll, origPrimary
	})
	sireniaPrimaryRelease = func(service string) (string, error) { return s.primary(), nil }
	repairOrphanSireniaFormations = func(_ controller.Client, log log15.Logger) error {
		forms, _ := f.FormationList("postgres")
		for _, z := range updaterdeploy.PlanOrphanSireniaRepair(f.appRelease, s.primary(), "postgres", forms) {
			if err := f.PutFormation(z); err != nil {
				return err
			}
		}
		return nil
	}
	sireniaRollHA = func(d *DeployJob, processType string, proc ct.ProcessType, stopped *[]string, log log15.Logger) error {
		old := append([]string(nil), s.members...)
		for i := range old {
			d.newFormation.Processes[processType]++
			if err := d.client.PutFormation(d.newFormation); err != nil {
				return err
			}
			s.members = append(s.members, d.NewReleaseID)
			s.note()
			// stop the old peer (asyncs first, the primary last)
			idx := len(old) - 1 - i
			if idx == 0 {
				s.members = s.members[1:]
			} else {
				s.members = append(s.members[:idx], s.members[idx+1:]...)
			}
			s.note()
		}
		// the new peers reorder so that the first new one is the primary
		return nil
	}
}

func postgresReleases(f *fakeFormationCluster, ids ...string) {
	for _, id := range ids {
		f.releases[id] = &ct.Release{ID: id, Env: map[string]string{"SIRENIA_PROCESS": "postgres"}, Processes: map[string]ct.ProcessType{
			"postgres": {Service: "postgres", Volumes: []ct.VolumeReq{{Path: "/data"}}},
			"web":      {Service: "postgres-api"},
		}}
	}
}

func postgresDeploy(processes map[string]int) *ct.Deployment {
	return &ct.Deployment{ID: "be6f21f3", AppID: "postgres", OldReleaseID: relCurrent, NewReleaseID: relNew,
		Strategy: "sirenia", Processes: processes, Status: "running"}
}

func assertEnginesOn(t *testing.T, f *fakeFormationCluster, s *fakeSireniaCluster, want string, low map[string]int) {
	t.Helper()
	for _, rid := range []string{relEngines, relCurrent, relNew} {
		n := 0
		if form := f.formations[rid]; form != nil {
			n = form.Processes["postgres"]
		}
		exp := 0
		if rid == want {
			exp = 3
		}
		if n != exp {
			t.Errorf("release %s has postgres=%d, want %d (formations: engines=%v current=%v new=%v)", rid, n, exp,
				f.formations[relEngines], f.formations[relCurrent], f.formations[relNew])
		}
	}
	for _, m := range s.members {
		if m != want {
			t.Errorf("sirenia member runs from %s, want %s (members %v)", m, want, s.members)
		}
	}
	if low["postgres"] < 3 {
		t.Errorf("running postgres engines dropped to %d, want never below 3", low["postgres"])
	}
	if s.lowMembers < 3 {
		t.Errorf("sirenia members dropped to %d, want never below 3", s.lowMembers)
	}
	if f.appRelease != relNew {
		t.Errorf("current release %s, want %s", f.appRelease, relNew)
	}
}

// The EDGE-68 state as the EDGE-72 deploy found it (23:19:27): the current
// release has postgres=0 web=2 and the older release runs all three engines,
// with the primary on it. The deploy copied postgres:0, finished in a second
// and left the engines on the older release, so every later deploy did the
// same. It must instead move the engines to the new release without ever
// running fewer than three.
func TestSireniaDeployMovesEnginesOffANonCurrentRelease(t *testing.T) {
	f := newFakeFormationCluster("devnode1", "devnode2", "devnode3")
	postgresReleases(f, relEngines, relCurrent, relNew)
	f.formations[relEngines] = &ct.Formation{AppID: "postgres", ReleaseID: relEngines, Processes: map[string]int{"postgres": 3}}
	f.formations[relCurrent] = &ct.Formation{AppID: "postgres", ReleaseID: relCurrent, Processes: map[string]int{"postgres": 0, "web": 2}}
	f.appRelease = relCurrent
	s := &fakeSireniaCluster{members: []string{relEngines, relEngines, relEngines}}
	s.install(t, f)
	low := watchCapacity(f)

	runSuccessfulDeploy(t, f, postgresDeploy(map[string]int{"postgres": 0, "web": 2}))

	assertEnginesOn(t, f, s, relNew, low)
	if n := f.formations[relNew].Processes["web"]; n != 2 {
		t.Errorf("new release web=%d, want 2", n)
	}
}

// The whole EDGE-68 sequence. The host fixer scaled the current release to
// postgres=3 (20:23:59), so six engines ran from two formations. The next
// update's orphan repair zeroed the current release because the primary ran
// from the older one (21:03:14), and the update's postgres deploy then copied
// postgres:0. The repair must keep the current release, and the deploy must
// end with only the new release running engines.
func TestSireniaUpdateReplayEDGE68(t *testing.T) {
	f := newFakeFormationCluster("devnode1", "devnode2", "devnode3")
	postgresReleases(f, relEngines, relCurrent, relNew)
	f.formations[relEngines] = &ct.Formation{AppID: "postgres", ReleaseID: relEngines, Processes: map[string]int{"postgres": 3}}
	f.formations[relCurrent] = &ct.Formation{AppID: "postgres", ReleaseID: relCurrent, Processes: map[string]int{"postgres": 3, "web": 2}}
	f.appRelease = relCurrent
	s := &fakeSireniaCluster{members: []string{relEngines, relEngines, relEngines}}
	s.install(t, f)
	low := watchCapacity(f)

	// flynn-host update: the orphan repair runs before any deploy
	if err := repairOrphanSireniaFormations(nil, log15.New()); err != nil {
		t.Fatal(err)
	}
	if n := f.formations[relCurrent].Processes["postgres"]; n != 3 {
		t.Fatalf("the orphan repair scaled the current release to postgres=%d, want it left at 3", n)
	}
	// the deployment copies the current formation (controller DeploymentRepo.Add)
	cur, _ := f.GetFormation("postgres", relCurrent)
	runSuccessfulDeploy(t, f, postgresDeploy(cur.Processes))

	assertEnginesOn(t, f, s, relNew, low)
}

// A sirenia deploy whose old formation runs no engines while another
// release's formation does, and whose primary cannot be placed on that
// release, fails instead of finishing as a no-op.
func TestSireniaDeployFailsWhenEnginesRunElsewhereAndThePrimaryIsUnknown(t *testing.T) {
	f := newFakeFormationCluster("devnode1")
	postgresReleases(f, relEngines, relCurrent, relNew)
	f.formations[relEngines] = &ct.Formation{AppID: "postgres", ReleaseID: relEngines, Processes: map[string]int{"postgres": 3}}
	f.formations[relCurrent] = &ct.Formation{AppID: "postgres", ReleaseID: relCurrent, Processes: map[string]int{"postgres": 0, "web": 2}}
	f.appRelease = relCurrent
	s := &fakeSireniaCluster{}
	s.install(t, f)

	j := &DeployJob{
		Deployment:   postgresDeploy(map[string]int{"postgres": 0, "web": 2}),
		client:       f,
		deployEvents: make(chan ct.DeploymentEvent, 64),
		logger:       log15.New(),
		stop:         make(chan struct{}),
	}
	err := j.Perform()
	if err == nil || !strings.Contains(err.Error(), relEngines) {
		t.Fatalf("Perform = %v, want an error naming %s", err, relEngines)
	}
	if n := f.formations[relEngines].Processes["postgres"]; n != 3 {
		t.Fatalf("engines formation postgres=%d, want 3", n)
	}
}

// A normal sirenia deploy (engines and primary on the current release) is
// unchanged: the old release ends at zero, the new one at the old count.
func TestSireniaDeployFromTheCurrentRelease(t *testing.T) {
	f := newFakeFormationCluster("devnode1", "devnode2", "devnode3")
	postgresReleases(f, relCurrent, relNew)
	f.formations[relCurrent] = &ct.Formation{AppID: "postgres", ReleaseID: relCurrent, Processes: map[string]int{"postgres": 3, "web": 2}}
	f.appRelease = relCurrent
	s := &fakeSireniaCluster{members: []string{relCurrent, relCurrent, relCurrent}}
	s.install(t, f)
	low := watchCapacity(f)

	runSuccessfulDeploy(t, f, postgresDeploy(map[string]int{"postgres": 3, "web": 2}))

	assertEnginesOn(t, f, s, relNew, low)
}

// A retry of the same release after the no-op (the new formation already
// equals the copied postgres:0 web:2) must not stop at "already completed".
func TestSireniaDeployRetryAfterTheNoOpStillMovesTheEngines(t *testing.T) {
	f := newFakeFormationCluster("devnode1", "devnode2", "devnode3")
	postgresReleases(f, relEngines, relCurrent, relNew)
	f.formations[relEngines] = &ct.Formation{AppID: "postgres", ReleaseID: relEngines, Processes: map[string]int{"postgres": 3}}
	f.formations[relCurrent] = &ct.Formation{AppID: "postgres", ReleaseID: relCurrent, Processes: map[string]int{"postgres": 0, "web": 2}}
	f.formations[relNew] = &ct.Formation{AppID: "postgres", ReleaseID: relNew, Processes: map[string]int{"postgres": 0, "web": 2}}
	f.appRelease = relCurrent
	s := &fakeSireniaCluster{members: []string{relEngines, relEngines, relEngines}}
	s.install(t, f)
	low := watchCapacity(f)

	runSuccessfulDeploy(t, f, postgresDeploy(map[string]int{"postgres": 0, "web": 2}))

	assertEnginesOn(t, f, s, relNew, low)
}
