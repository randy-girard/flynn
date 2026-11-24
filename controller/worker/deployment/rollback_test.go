package deployment

import (
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

// fakeFormationCluster is a controller client whose "scheduler" acts at once:
// every formation with a non-zero count runs one job per allowed host for
// each omni type, and one job per count on the first host otherwise.
type fakeFormationCluster struct {
	controller.Client

	mu          sync.Mutex
	hosts       []string
	releases    map[string]*ct.Release
	formations  map[string]*ct.Formation
	deployments []*ct.Deployment

	// jobListErr, when set, decides whether JobList fails given the current
	// formations (the live failure: the controller API stopped answering
	// right after the last old scheduler was scaled to zero).
	jobListErr func(f *fakeFormationCluster) error
	// waitErr is returned by a waiting ScaleAppRelease for that release after
	// the scale request was written (the live rollback: the restore was
	// accepted but its scale request completed after the 10 s timeout).
	waitErr map[string]error
	// putErr makes every scale request for that release fail before it is
	// written.
	putErr map[string]error
	// appRelease is the app's current release (SetAppRelease).
	appRelease string
	// jobsOverride, when set, rewrites what JobList returns.
	jobsOverride func([]*ct.Job) []*ct.Job
	// observe runs after every formation change, with the lock held.
	observe func(f *fakeFormationCluster)
}

func newFakeFormationCluster(hosts ...string) *fakeFormationCluster {
	return &fakeFormationCluster{
		hosts:      hosts,
		releases:   map[string]*ct.Release{},
		formations: map[string]*ct.Formation{},
		waitErr:    map[string]error{},
		putErr:     map[string]error{},
	}
}

func copyFormation(f *ct.Formation) *ct.Formation {
	out := &ct.Formation{AppID: f.AppID, ReleaseID: f.ReleaseID, Processes: map[string]int{}, Tags: map[string]map[string]string{}}
	for k, v := range f.Processes {
		out.Processes[k] = v
	}
	for k, v := range f.Tags {
		inner := map[string]string{}
		for ik, iv := range v {
			inner[ik] = iv
		}
		out.Tags[k] = inner
	}
	return out
}

func (f *fakeFormationCluster) GetRelease(id string) (*ct.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.releases[id]; ok {
		return r, nil
	}
	return nil, controller.ErrNotFound
}

func (f *fakeFormationCluster) GetFormation(appID, releaseID string) (*ct.Formation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if form, ok := f.formations[releaseID]; ok {
		return copyFormation(form), nil
	}
	return nil, controller.ErrNotFound
}

func (f *fakeFormationCluster) FormationList(appID string) ([]*ct.Formation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*ct.Formation, 0, len(f.formations))
	for _, form := range f.formations {
		out = append(out, copyFormation(form))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReleaseID < out[j].ReleaseID })
	return out, nil
}

func (f *fakeFormationCluster) DeploymentList(appID string) ([]*ct.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deployments, nil
}

func (f *fakeFormationCluster) ScaleAppRelease(appID, releaseID string, opts ct.ScaleOptions) error {
	f.mu.Lock()
	if err := f.putErr[releaseID]; err != nil {
		f.mu.Unlock()
		return err
	}
	form, ok := f.formations[releaseID]
	if !ok {
		form = &ct.Formation{AppID: appID, ReleaseID: releaseID}
		f.formations[releaseID] = form
	}
	if form.Processes == nil {
		form.Processes = map[string]int{}
	}
	if form.Tags == nil {
		form.Tags = map[string]map[string]string{}
	}
	// merge like FormationRepo.AddScaleRequest
	for typ, n := range opts.Processes {
		form.Processes[typ] = n
	}
	for typ, tags := range opts.Tags {
		form.Tags[typ] = tags
	}
	waitErr := f.waitErr[releaseID]
	if f.observe != nil {
		f.observe(f)
	}
	f.mu.Unlock()
	if opts.ScaleRequestCallback != nil {
		opts.ScaleRequestCallback(&ct.ScaleRequest{AppID: appID, ReleaseID: releaseID})
	}
	if opts.NoWait {
		return nil
	}
	return waitErr
}

func (f *fakeFormationCluster) DeleteFormation(appID, releaseID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.putErr[releaseID]; err != nil {
		return err
	}
	delete(f.formations, releaseID)
	if f.observe != nil {
		f.observe(f)
	}
	return nil
}

func (f *fakeFormationCluster) SetAppRelease(appID, releaseID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appRelease = releaseID
	return nil
}

func (f *fakeFormationCluster) jobsLocked() []*ct.Job {
	var jobs []*ct.Job
	ids := make([]string, 0, len(f.formations))
	for id := range f.formations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, rid := range ids {
		form := f.formations[rid]
		rel := f.releases[rid]
		for typ, n := range form.Processes {
			if n <= 0 {
				continue
			}
			omni := rel != nil && rel.Processes[typ].Omni
			if !omni {
				for i := 0; i < n; i++ {
					jobs = append(jobs, &ct.Job{ReleaseID: rid, Type: typ, HostID: f.hosts[0], State: ct.JobStateUp})
				}
				continue
			}
			for _, h := range f.hosts {
				if ct.HostIDsTagMatches(form.Tags[typ], h) {
					jobs = append(jobs, &ct.Job{ReleaseID: rid, Type: typ, HostID: h, State: ct.JobStateUp, UUID: rid + "-" + typ + "-" + h})
				}
			}
		}
	}
	return jobs
}

func (f *fakeFormationCluster) JobList(appID string) ([]*ct.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.jobListErr != nil {
		if err := f.jobListErr(f); err != nil {
			return nil, err
		}
	}
	jobs := f.jobsLocked()
	if f.jobsOverride != nil {
		jobs = f.jobsOverride(jobs)
	}
	return jobs, nil
}

func (f *fakeFormationCluster) schedulerHosts() map[string][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]string{}
	for _, j := range f.jobsLocked() {
		if j.Type == "scheduler" {
			out[j.HostID] = append(out[j.HostID], j.ReleaseID)
		}
	}
	return out
}

var errJobListTimeout = errors.New(`Get "http://controller.discoverd/apps/controller/jobs": net/http: timeout awaiting response headers`)

func controllerReleases(f *fakeFormationCluster, ids ...string) {
	for _, id := range ids {
		f.releases[id] = &ct.Release{ID: id, Processes: map[string]ct.ProcessType{
			"scheduler": {Omni: true},
			"web":       {},
			"worker":    {},
		}}
	}
}

func controllerDeployment(oldID, newID string) *ct.Deployment {
	return &ct.Deployment{
		ID:           "dep-" + newID,
		AppID:        "controller",
		OldReleaseID: oldID,
		NewReleaseID: newID,
		Strategy:     "one-by-one",
		Processes:    map[string]int{"scheduler": 1, "web": 2, "worker": 2},
		Tags:         map[string]map[string]string{"scheduler": {}},
	}
}

// runFailedControllerDeploy drives Perform and then the rollback, as
// HandleDeployment does for a failed deploy.
func runFailedControllerDeploy(t *testing.T, f *fakeFormationCluster, dep *ct.Deployment) error {
	t.Helper()
	orig, err := f.GetFormation(dep.AppID, dep.OldReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	j := &DeployJob{
		Deployment:   dep,
		client:       f,
		deployEvents: make(chan ct.DeploymentEvent, 64),
		logger:       log15.New(),
		stop:         stop,
	}
	if err := j.Perform(); err == nil {
		t.Fatal("expected the deploy to fail")
	}
	c := &context{client: f, logger: log15.New()}
	return c.rollback(log15.New(), dep, orig, stop)
}

func assertOneSchedulerPerHost(t *testing.T, f *fakeFormationCluster, release string) {
	t.Helper()
	got := f.schedulerHosts()
	for _, h := range f.hosts {
		if len(got[h]) != 1 || got[h][0] != release {
			t.Fatalf("host %s schedulers = %v, want exactly one from %s (all: %v)", h, got[h], release, got)
		}
	}
}

// The live failure (deployment c7b6063a): the scheduler roll reached its last
// step, scaled the old scheduler to 0, then JobList timed out. The rollback
// restore was accepted but completed after its 10 s wait, so the rollback
// returned before deleting the new formation and the new release kept
// scheduler=1 on every host.
func TestFailedOneByOneRollbackZeroesNewOmniFormation(t *testing.T) {
	f := newFakeFormationCluster("devnode1", "devnode2", "devnode3")
	controllerReleases(f, "old", "new")
	f.formations["old"] = &ct.Formation{AppID: "controller", ReleaseID: "old",
		Processes: map[string]int{"scheduler": 1, "web": 2, "worker": 2},
		Tags:      map[string]map[string]string{"scheduler": {}}}
	failed := false
	f.jobListErr = func(f *fakeFormationCluster) error {
		if failed || f.formations["old"].Processes["scheduler"] == 0 {
			failed = true
			return errJobListTimeout
		}
		return nil
	}
	f.waitErr["old"] = errors.New("timed out waiting for scale to complete")

	err := runFailedControllerDeploy(t, f, controllerDeployment("old", "new"))
	failed = false
	f.jobListErr = nil
	if err != nil {
		t.Logf("rollback returned %v", err)
	}
	if form, ok := f.formations["new"]; ok {
		for typ, n := range form.Processes {
			if n != 0 {
				t.Fatalf("new release formation %s=%d after a failed deploy, want 0 (formation %v)", typ, n, form.Processes)
			}
		}
	}
	if f.formations["old"].Processes["scheduler"] != 1 {
		t.Fatalf("old scheduler = %d, want restored to 1", f.formations["old"].Processes["scheduler"])
	}
	assertOneSchedulerPerHost(t, f, "old")
}

// A failure in the middle of the roll leaves the old scheduler pinned to the
// hosts it had not left yet. The rollback must lift that pin, or the old
// release never comes back on the hosts the new one is leaving.
func TestFailedOneByOneRollbackClearsOldOmniHostPins(t *testing.T) {
	f := newFakeFormationCluster("devnode1", "devnode2", "devnode3")
	controllerReleases(f, "old", "new")
	f.formations["old"] = &ct.Formation{AppID: "controller", ReleaseID: "old",
		Processes: map[string]int{"scheduler": 1, "web": 2, "worker": 2},
		Tags:      map[string]map[string]string{"scheduler": {}}}
	failed := false
	f.jobListErr = func(f *fakeFormationCluster) error {
		if failed {
			return errJobListTimeout
		}
		if n := f.formations["new"]; n != nil && n.Tags["scheduler"][ct.FormationHostIDsTag] == "devnode1,devnode2" {
			failed = true
			return errJobListTimeout
		}
		return nil
	}

	err := runFailedControllerDeploy(t, f, controllerDeployment("old", "new"))
	failed = false
	f.jobListErr = nil
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, ok := f.formations["old"].Tags["scheduler"][ct.FormationHostIDsTag]; ok {
		t.Fatalf("old scheduler still pinned after rollback: %v", f.formations["old"].Tags)
	}
	assertOneSchedulerPerHost(t, f, "old")
}

// When the restore itself cannot be written, the rollback must not zero the
// new release (that could leave no scheduler at all); it reports the error
// and the next deploy cleans up.
func TestRollbackKeepsNewFormationWhenRestoreIsNotWritten(t *testing.T) {
	f := newFakeFormationCluster("devnode1")
	controllerReleases(f, "old", "new")
	f.formations["old"] = &ct.Formation{AppID: "controller", ReleaseID: "old", Processes: map[string]int{"scheduler": 0}}
	f.formations["new"] = &ct.Formation{AppID: "controller", ReleaseID: "new", Processes: map[string]int{"scheduler": 1}}
	f.putErr["old"] = errors.New("connection refused")
	c := &context{client: f, logger: log15.New()}
	orig := &ct.Formation{AppID: "controller", ReleaseID: "old", Processes: map[string]int{"scheduler": 1}}
	if err := c.rollback(log15.New(), controllerDeployment("old", "new"), orig, make(chan struct{})); err == nil {
		t.Fatal("rollback must report a restore it could not write")
	}
	if f.formations["new"].Processes["scheduler"] != 1 {
		t.Fatal("new formation must stay while the old one could not be restored")
	}
}

// Repair: a successful deploy zeroes formations that a failed deployment of
// the same app left behind (the 6e8553be formation that survived two later
// deploys), and leaves other non-current formations alone (`flynn scale -r`).
func TestSuccessfulDeployZeroesFailedDeploymentLeftovers(t *testing.T) {
	f := newFakeFormationCluster("devnode1", "devnode2", "devnode3")
	controllerReleases(f, "de7c1efb", "6e8553be", "97546bf0", "manual")
	f.formations["de7c1efb"] = &ct.Formation{AppID: "controller", ReleaseID: "de7c1efb", Processes: map[string]int{"scheduler": 0, "web": 0, "worker": 0}}
	f.formations["6e8553be"] = &ct.Formation{AppID: "controller", ReleaseID: "6e8553be", Processes: map[string]int{"scheduler": 1}}
	f.formations["97546bf0"] = &ct.Formation{AppID: "controller", ReleaseID: "97546bf0", Processes: map[string]int{"scheduler": 1, "web": 2, "worker": 2}}
	f.formations["manual"] = &ct.Formation{AppID: "controller", ReleaseID: "manual", Processes: map[string]int{"web": 1}}
	f.deployments = []*ct.Deployment{
		{ID: "d2", AppID: "controller", OldReleaseID: "de7c1efb", NewReleaseID: "97546bf0", Status: "running"},
		{ID: "c7b6063a", AppID: "controller", OldReleaseID: "de7c1efb", NewReleaseID: "6e8553be", Status: "failed"},
		{ID: "d0", AppID: "controller", OldReleaseID: "x", NewReleaseID: "de7c1efb", Status: "complete"},
	}
	c := &context{client: f, logger: log15.New()}
	c.zeroFailedDeploymentFormations(log15.New(), f.deployments[0])

	if n := f.formations["6e8553be"].Processes["scheduler"]; n != 0 {
		t.Fatalf("failed deployment's release still has scheduler=%d", n)
	}
	if f.formations["97546bf0"].Processes["scheduler"] != 1 {
		t.Fatal("the deployed release must keep its scheduler")
	}
	if f.formations["manual"].Processes["web"] != 1 {
		t.Fatal("a release scaled by hand is not a failed-deploy leftover")
	}
	assertOneSchedulerPerHost(t, f, "97546bf0")
}

func TestHandleDeploymentZeroesFailedLeftoversAfterSetAppRelease(t *testing.T) {
	src, err := os.ReadFile("context.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	set := strings.Index(body, "c.client.SetAppRelease(deployment.AppID, deployment.NewReleaseID)")
	zero := strings.Index(body, "c.zeroFailedDeploymentFormations(log, deployment)")
	if set < 0 || zero < 0 || zero < set {
		t.Fatal("a successful deploy must zero failed-deploy leftovers once the new release is current")
	}
}

func webReleases(f *fakeFormationCluster, ids ...string) {
	for _, id := range ids {
		f.releases[id] = &ct.Release{ID: id, Processes: map[string]ct.ProcessType{"web": {}, "worker": {}}}
	}
}

// runSuccessfulDeploy drives a deploy the way HandleDeployment does on
// success: Perform, SetAppRelease, then the leftover repair.
func runSuccessfulDeploy(t *testing.T, f *fakeFormationCluster, dep *ct.Deployment) {
	t.Helper()
	j := &DeployJob{
		Deployment:   dep,
		client:       f,
		deployEvents: make(chan ct.DeploymentEvent, 64),
		logger:       log15.New(),
		stop:         make(chan struct{}),
	}
	if err := j.Perform(); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if err := f.SetAppRelease(dep.AppID, dep.NewReleaseID); err != nil {
		t.Fatal(err)
	}
	c := &context{client: f, logger: log15.New()}
	c.zeroFailedDeploymentFormations(log15.New(), dep)
}

// runningTotalsLocked counts the up jobs of the app by process type.
func (f *fakeFormationCluster) runningTotalsLocked() map[string]int {
	out := map[string]int{}
	for _, j := range f.jobsLocked() {
		if j.State == ct.JobStateUp {
			out[j.Type]++
		}
	}
	return out
}

// watchCapacity records the lowest running count of each process type over
// every formation change from now on.
func watchCapacity(f *fakeFormationCluster) map[string]int {
	low := f.runningTotalsLocked()
	f.observe = func(f *fakeFormationCluster) {
		now := f.runningTotalsLocked()
		for typ, n := range low {
			if now[typ] < n {
				low[typ] = now[typ]
			}
		}
	}
	return low
}

// EDGE-72, the dev-cluster incident: the app's current release (e0165d85)
// has web=0 worker=1 (system Postgres: postgres:0 web:2) while an older
// release (0a8a5e46) runs web=3, and 0a8a5e46 was once the target of a
// failed deployment. A successful deploy copies the current formation to the
// new release (a deploy whose counts are all zero finishes in the controller
// and never reaches the worker, so the other type matters). The repair then
// zeroed 0a8a5e46, so no web job ran at all (for the system Postgres: every
// engine stopped).
func TestZeroFailedFormationsKeepsTheFormationThatRunsTheApp(t *testing.T) {
	f := newFakeFormationCluster("devnode1", "devnode2", "devnode3")
	webReleases(f, "0a8a5e46", "e0165d85", "12384ef4")
	f.formations["0a8a5e46"] = &ct.Formation{AppID: "app", ReleaseID: "0a8a5e46", Processes: map[string]int{"web": 3}}
	f.formations["e0165d85"] = &ct.Formation{AppID: "app", ReleaseID: "e0165d85", Processes: map[string]int{"web": 0, "worker": 1}}
	f.appRelease = "e0165d85"
	dep := &ct.Deployment{ID: "be6f21f3", AppID: "app", OldReleaseID: "e0165d85", NewReleaseID: "12384ef4",
		Strategy: "one-by-one", Processes: map[string]int{"web": 0, "worker": 1}, Status: "running"}
	f.deployments = []*ct.Deployment{
		dep,
		{ID: "d-failed", AppID: "app", OldReleaseID: "older", NewReleaseID: "0a8a5e46", Status: "failed"},
		{ID: "d-ok", AppID: "app", OldReleaseID: "older", NewReleaseID: "e0165d85", Status: "complete"},
	}
	low := watchCapacity(f)

	runSuccessfulDeploy(t, f, dep)

	if n := f.formations["0a8a5e46"].Processes["web"]; n != 3 {
		t.Fatalf("the formation that runs the app was scaled to web=%d, want 3 (new release formation %v)", n, f.formations["12384ef4"])
	}
	if low["web"] < 3 {
		t.Fatalf("running web jobs dropped to %d during the deploy, want never below 3", low["web"])
	}
	if n := f.formations["12384ef4"].Processes["worker"]; n != 1 {
		t.Fatalf("new release worker=%d, want 1 (the deploy must have run)", n)
	}
}

// The same shape, but the operator first scales the current release back
// up (`flynn scale web=3`, so both run) and then deploys: the leftover may go
// only once the new release runs every type it runs, and the app never has
// fewer running jobs than it started with.
func TestZeroFailedFormationsWaitsForTheNewReleaseAtCapacity(t *testing.T) {
	f := newFakeFormationCluster("devnode1")
	webReleases(f, "0a8a5e46", "e0165d85", "12384ef4")
	f.formations["0a8a5e46"] = &ct.Formation{AppID: "app", ReleaseID: "0a8a5e46", Processes: map[string]int{"web": 3}}
	f.formations["e0165d85"] = &ct.Formation{AppID: "app", ReleaseID: "e0165d85", Processes: map[string]int{"web": 0}}
	if err := f.ScaleAppRelease("app", "e0165d85", ct.ScaleOptions{Processes: map[string]int{"web": 3}}); err != nil {
		t.Fatal(err)
	}
	dep := &ct.Deployment{ID: "dep", AppID: "app", OldReleaseID: "e0165d85", NewReleaseID: "12384ef4",
		Strategy: "all-at-once", Processes: map[string]int{"web": 3}, Status: "running"}
	f.deployments = []*ct.Deployment{dep, {ID: "d-failed", AppID: "app", OldReleaseID: "x", NewReleaseID: "0a8a5e46", Status: "failed"}}
	low := watchCapacity(f)
	var zeroedEarly bool
	inner := f.observe
	f.observe = func(f *fakeFormationCluster) {
		inner(f)
		if f.formations["0a8a5e46"].Processes["web"] == 0 {
			n := f.formations["12384ef4"]
			if n == nil || n.Processes["web"] < 3 {
				zeroedEarly = true
			}
		}
	}

	runSuccessfulDeploy(t, f, dep)

	if zeroedEarly {
		t.Fatal("the leftover was zeroed before the new release was at capacity")
	}
	if low["web"] < 3 {
		t.Fatalf("running web jobs dropped to %d, want never below 3", low["web"])
	}
	if n := f.formations["0a8a5e46"].Processes["web"]; n != 0 {
		t.Fatalf("leftover web=%d once the new release runs web=3, want 0", n)
	}
}

// A leftover that runs a type the current release does not run (worker
// here) is the only formation running that type: it stays.
func TestZeroFailedFormationsKeepsALeftoverThatRunsAMissingType(t *testing.T) {
	f := newFakeFormationCluster("devnode1")
	webReleases(f, "left", "cur")
	f.formations["left"] = &ct.Formation{AppID: "app", ReleaseID: "left", Processes: map[string]int{"web": 1, "worker": 2}}
	f.formations["cur"] = &ct.Formation{AppID: "app", ReleaseID: "cur", Processes: map[string]int{"web": 2, "worker": 0}}
	dep := &ct.Deployment{ID: "dep", AppID: "app", OldReleaseID: "prev", NewReleaseID: "cur", Strategy: "all-at-once", Status: "running"}
	f.deployments = []*ct.Deployment{dep, {ID: "d-failed", AppID: "app", OldReleaseID: "x", NewReleaseID: "left", Status: "failed"}}
	c := &context{client: f, logger: log15.New()}
	c.zeroFailedDeploymentFormations(log15.New(), dep)
	if got := f.formations["left"].Processes; got["worker"] != 2 || got["web"] != 1 {
		t.Fatalf("leftover = %v, want unchanged: it is the only formation running worker", got)
	}
}

// A leftover whose new-release counterpart is set but not running yet (no
// up jobs) stays: the repair trusts running jobs, not the formation.
func TestZeroFailedFormationsKeepsALeftoverWhileTheNewJobsAreNotUp(t *testing.T) {
	f := newFakeFormationCluster("devnode1")
	webReleases(f, "left", "cur")
	f.formations["left"] = &ct.Formation{AppID: "app", ReleaseID: "left", Processes: map[string]int{"web": 2}}
	f.formations["cur"] = &ct.Formation{AppID: "app", ReleaseID: "cur", Processes: map[string]int{"web": 2}}
	dep := &ct.Deployment{ID: "dep", AppID: "app", OldReleaseID: "prev", NewReleaseID: "cur", Strategy: "all-at-once", Status: "running"}
	f.deployments = []*ct.Deployment{dep, {ID: "d-failed", AppID: "app", OldReleaseID: "x", NewReleaseID: "left", Status: "failed"}}
	f.jobListErr = nil
	f.jobsOverride = func(jobs []*ct.Job) []*ct.Job {
		var out []*ct.Job
		for _, j := range jobs {
			if j.ReleaseID == "cur" {
				j.State = ct.JobStateStarting
			}
			out = append(out, j)
		}
		return out
	}
	c := &context{client: f, logger: log15.New()}
	c.zeroFailedDeploymentFormations(log15.New(), dep)
	if n := f.formations["left"].Processes["web"]; n != 2 {
		t.Fatalf("leftover web=%d while the new release has no up job, want 2", n)
	}
}

// Sirenia (system Postgres, plugin datastores) and any release with volumes
// hold data: the repair never scales them, whatever the counts.
func TestZeroFailedFormationsSkipsSireniaAndVolumeApps(t *testing.T) {
	for _, tc := range []struct {
		name     string
		strategy string
		volumes  bool
	}{
		{"sirenia", "sirenia", false},
		{"volumes", "all-at-once", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeFormationCluster("devnode1")
			for _, id := range []string{"left", "cur"} {
				pt := ct.ProcessType{}
				if tc.volumes {
					pt.Volumes = []ct.VolumeReq{{Path: "/data"}}
				}
				f.releases[id] = &ct.Release{ID: id, Processes: map[string]ct.ProcessType{"postgres": pt}}
			}
			f.formations["left"] = &ct.Formation{AppID: "pg", ReleaseID: "left", Processes: map[string]int{"postgres": 3}}
			f.formations["cur"] = &ct.Formation{AppID: "pg", ReleaseID: "cur", Processes: map[string]int{"postgres": 3}}
			dep := &ct.Deployment{ID: "dep", AppID: "pg", OldReleaseID: "prev", NewReleaseID: "cur", Strategy: tc.strategy, Status: "running"}
			f.deployments = []*ct.Deployment{dep, {ID: "d-failed", AppID: "pg", OldReleaseID: "x", NewReleaseID: "left", Status: "failed"}}
			c := &context{client: f, logger: log15.New()}
			c.zeroFailedDeploymentFormations(log15.New(), dep)
			if n := f.formations["left"].Processes["postgres"]; n != 3 {
				t.Fatalf("datastore leftover postgres=%d, want 3: the repair must not touch data formations", n)
			}
		})
	}
}

// A release that was ever the app's current release (the old release of
// some deployment) may be what the app runs: it is not a leftover.
func TestZeroFailedFormationsKeepsAReleaseThatWasCurrent(t *testing.T) {
	f := newFakeFormationCluster("devnode1")
	webReleases(f, "left", "cur")
	f.formations["left"] = &ct.Formation{AppID: "app", ReleaseID: "left", Processes: map[string]int{"web": 1}}
	f.formations["cur"] = &ct.Formation{AppID: "app", ReleaseID: "cur", Processes: map[string]int{"web": 2}}
	dep := &ct.Deployment{ID: "dep", AppID: "app", OldReleaseID: "prev", NewReleaseID: "cur", Strategy: "all-at-once", Status: "running"}
	f.deployments = []*ct.Deployment{
		dep,
		{ID: "d-failed", AppID: "app", OldReleaseID: "x", NewReleaseID: "left", Status: "failed"},
		{ID: "d-from-left", AppID: "app", OldReleaseID: "left", NewReleaseID: "y", Status: "failed"},
	}
	c := &context{client: f, logger: log15.New()}
	c.zeroFailedDeploymentFormations(log15.New(), dep)
	if n := f.formations["left"].Processes["web"]; n != 1 {
		t.Fatalf("a release that was current was scaled to web=%d, want 1", n)
	}
}

// An omni leftover stays while some host it runs on has no job of the
// current release (the new scheduler is still pinned to devnode1,devnode2).
func TestZeroFailedFormationsKeepsAnOmniLeftoverOnAHostTheNewReleaseMisses(t *testing.T) {
	f := newFakeFormationCluster("devnode1", "devnode2", "devnode3")
	controllerReleases(f, "6e8553be", "97546bf0")
	f.formations["6e8553be"] = &ct.Formation{AppID: "controller", ReleaseID: "6e8553be", Processes: map[string]int{"scheduler": 1}}
	f.formations["97546bf0"] = &ct.Formation{AppID: "controller", ReleaseID: "97546bf0", Processes: map[string]int{"scheduler": 1},
		Tags: map[string]map[string]string{"scheduler": {ct.FormationHostIDsTag: "devnode1,devnode2"}}}
	dep := &ct.Deployment{ID: "d2", AppID: "controller", OldReleaseID: "de7c1efb", NewReleaseID: "97546bf0", Strategy: "one-by-one", Status: "running"}
	f.deployments = []*ct.Deployment{dep, {ID: "c7b6063a", AppID: "controller", OldReleaseID: "x", NewReleaseID: "6e8553be", Status: "failed"}}
	c := &context{client: f, logger: log15.New()}
	c.zeroFailedDeploymentFormations(log15.New(), dep)
	if n := f.formations["6e8553be"].Processes["scheduler"]; n != 1 {
		t.Fatalf("omni leftover scheduler=%d while devnode3 has no current scheduler, want 1", n)
	}
}
