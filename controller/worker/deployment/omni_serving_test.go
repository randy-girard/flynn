package deployment

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
)

// EDGE-42: an omni roll must not stop the next host's old job before the
// new job on the previous host serves, and it must not report complete
// before every host runs an up job of the new release.

// omniJob is one job of the fake scheduler below. Its states follow the
// clock: invisible before createAt, pending, then starting, then up at upAt.
// stopAt (once set) ends it: stopping for the fake's stopDelay, then down.
type omniJob struct {
	id, rid, typ, host string
	createAt, upAt     time.Time
	stopAt             time.Time
	crashAt            time.Time
	crashed            bool
}

func (j *omniJob) stopped() bool { return !j.stopAt.IsZero() }

// fakeOmniScheduler is fakeFormationCluster with a scheduler that keeps job
// state: on every formation change it starts one job per matching host for
// each omni type and stops jobs that no longer match, and each job moves
// through pending and starting to up after the delays the test sets.
type fakeOmniScheduler struct {
	*fakeFormationCluster

	jobs []*omniJob
	seq  int
	// delay is how long after the scheduler decides to run a job on a host
	// the job appears (create) and then reaches up.
	delay func(rid, host string, n int) (create, up time.Duration)
	// crashAfter, when non-zero, makes the nth job (counted from 0) of
	// release rid on host go down that long after it came up.
	crashAfter func(rid, host string, n int) time.Duration
	stopDelay  time.Duration
	// started counts jobs created per release and host.
	started map[string]int
	// stops records when the deployer's formation change stopped a job.
	stops []omniStop
	// hostListErr, when set, can fail a HostList call.
	hostListErr func() error
	// shuttingDown marks listed hosts that discoverd says are draining.
	shuttingDown map[string]bool
	// regDelay is how long after a job is up it registers in discoverd.
	regDelay func(rid, host string) time.Duration
}

type omniStop struct {
	at        time.Time
	rid, host string
}

func newFakeOmniScheduler(hosts ...string) *fakeOmniScheduler {
	s := &fakeOmniScheduler{
		fakeFormationCluster: newFakeFormationCluster(hosts...),
		stopDelay:            50 * time.Millisecond,
		started:              map[string]int{},
	}
	s.delay = func(string, string, int) (time.Duration, time.Duration) { return 0, 50 * time.Millisecond }
	s.observe = func(*fakeFormationCluster) { s.reconcileLocked(time.Now()) }
	s.jobsOverride = func([]*ct.Job) []*ct.Job {
		now := time.Now()
		s.reconcileLocked(now)
		return s.visibleLocked(now)
	}
	return s
}

func (s *fakeOmniScheduler) hostListed(h string) bool {
	for _, id := range s.hosts {
		if id == h {
			return true
		}
	}
	return false
}

// reconcileLocked runs with s.mu held.
func (s *fakeOmniScheduler) reconcileLocked(now time.Time) {
	for _, j := range s.jobs {
		if !j.stopped() && !j.crashAt.IsZero() && !j.crashAt.After(now) {
			j.stopAt, j.crashed = j.crashAt, true
		}
	}
	for _, j := range s.jobs {
		if j.stopped() {
			continue
		}
		form := s.formations[j.rid]
		keep := form != nil && form.Processes[j.typ] > 0 && s.hostListed(j.host) && ct.HostIDsTagMatches(form.Tags[j.typ], j.host)
		if !keep {
			j.stopAt = now
			s.stops = append(s.stops, omniStop{at: now, rid: j.rid, host: j.host})
		}
	}
	rids := make([]string, 0, len(s.formations))
	for rid := range s.formations {
		rids = append(rids, rid)
	}
	sort.Strings(rids)
	for _, rid := range rids {
		form := s.formations[rid]
		rel := s.releases[rid]
		for typ, n := range form.Processes {
			if n <= 0 || rel == nil || !rel.Processes[typ].Omni {
				continue
			}
			for _, h := range s.hosts {
				if !ct.HostIDsTagMatches(form.Tags[typ], h) {
					continue
				}
				live := false
				for _, j := range s.jobs {
					if j.rid == rid && j.typ == typ && j.host == h && !j.stopped() {
						live = true
					}
				}
				if live {
					continue
				}
				key := rid + "/" + h
				n := s.started[key]
				s.started[key]++
				create, up := s.delay(rid, h, n)
				s.seq++
				j := &omniJob{id: fmt.Sprintf("%s-%s-%d", rid, h, s.seq), rid: rid, typ: typ, host: h, createAt: now.Add(create)}
				j.upAt = j.createAt.Add(up)
				if s.crashAfter != nil {
					if d := s.crashAfter(rid, h, n); d > 0 {
						j.crashAt = j.upAt.Add(d)
					}
				}
				s.jobs = append(s.jobs, j)
			}
		}
	}
}

func (s *fakeOmniScheduler) stateLocked(j *omniJob, now time.Time) (ct.JobState, bool) {
	if j.stopped() && j.stopAt.Before(j.createAt) {
		return "", false
	}
	if now.Before(j.createAt) {
		return "", false
	}
	if j.stopped() && !j.stopAt.After(now) {
		if !j.crashed && now.Before(j.stopAt.Add(s.stopDelay)) {
			return ct.JobStateStopping, true
		}
		return ct.JobStateDown, true
	}
	switch {
	case now.Before(j.createAt.Add(j.upAt.Sub(j.createAt) / 2)):
		return ct.JobStatePending, true
	case now.Before(j.upAt):
		return ct.JobStateStarting, true
	default:
		return ct.JobStateUp, true
	}
}

func (s *fakeOmniScheduler) visibleLocked(now time.Time) []*ct.Job {
	var out []*ct.Job
	for _, j := range s.jobs {
		state, ok := s.stateLocked(j, now)
		if !ok {
			continue
		}
		out = append(out, &ct.Job{ID: "host-" + j.id, UUID: j.id, ReleaseID: j.rid, Type: j.typ, HostID: j.host, State: state})
	}
	return out
}

func (s *fakeOmniScheduler) DeleteJob(appID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.id == id && !j.stopped() {
			j.stopAt = time.Now()
		}
	}
	return nil
}

func (s *fakeOmniScheduler) PutJob(*ct.Job) error { return nil }

func (s *fakeOmniScheduler) HostList() ([]*ct.HostInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hostListErr != nil {
		if err := s.hostListErr(); err != nil {
			return nil, err
		}
	}
	out := make([]*ct.HostInfo, 0, len(s.hosts))
	for _, h := range s.hosts {
		out = append(out, &ct.HostInfo{ID: h, Shutdown: s.shuttingDown[h]})
	}
	return out, nil
}

func (s *fakeOmniScheduler) setHosts(hosts ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts = hosts
	s.reconcileLocked(time.Now())
}

// upJobs is how many up jobs of release rid each host runs now.
func (s *fakeOmniScheduler) upJobs(rid string) map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.reconcileLocked(now)
	out := map[string]int{}
	for _, j := range s.visibleLocked(now) {
		if j.ReleaseID == rid && j.State == ct.JobStateUp {
			out[j.HostID]++
		}
	}
	return out
}

// minServing is the smallest number of hosts with an up job (of any
// release) at any moment in [from, to].
func (s *fakeOmniScheduler) minServing(from, to time.Time) (int, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type iv struct {
		host     string
		from, to time.Time
	}
	var ivs []iv
	far := to.Add(time.Hour)
	points := []time.Time{from}
	for _, j := range s.jobs {
		end := far
		if j.stopped() {
			end = j.stopAt
		}
		if !j.upAt.Before(end) {
			continue
		}
		ivs = append(ivs, iv{j.host, j.upAt, end})
		points = append(points, j.upAt, end)
	}
	best, at := -1, from
	for _, p := range points {
		if p.Before(from) || p.After(to) {
			continue
		}
		hosts := map[string]bool{}
		for _, v := range ivs {
			if !p.Before(v.from) && p.Before(v.to) {
				hosts[v.host] = true
			}
		}
		if best < 0 || len(hosts) < best {
			best, at = len(hosts), p
		}
	}
	return best, at
}

func (s *fakeOmniScheduler) firstUp(rid, host string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.rid == rid && j.host == host && (!j.stopped() || j.upAt.Before(j.stopAt)) {
			return j.upAt
		}
	}
	return time.Time{}
}

func (s *fakeOmniScheduler) stopOf(rid, host string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.stops {
		if st.rid == rid && st.host == host {
			return st.at
		}
	}
	return time.Time{}
}

func routerReleases(f *fakeFormationCluster, ids ...string) {
	for _, id := range ids {
		f.releases[id] = &ct.Release{ID: id, Processes: map[string]ct.ProcessType{
			"app": {Omni: true, HostNetwork: true, Service: "router-api"},
		}}
	}
}

// instances is the router-api service: one instance per running job, from
// regDelay after it came up.
func (s *fakeOmniScheduler) instances(service string) ([]*discoverd.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if service != "router-api" {
		return nil, fmt.Errorf("unexpected service %q", service)
	}
	now := time.Now()
	var out []*discoverd.Instance
	for _, j := range s.jobs {
		var reg time.Duration
		if s.regDelay != nil {
			reg = s.regDelay(j.rid, j.host)
		}
		if now.Before(j.upAt.Add(reg)) || (j.stopped() && !now.Before(j.stopAt)) {
			continue
		}
		out = append(out, &discoverd.Instance{Addr: j.host + ":5000", Meta: map[string]string{"FLYNN_JOB_ID": "host-" + j.id}})
	}
	return out, nil
}

// registeredAt is when the job's router-api instance appeared.
func (s *fakeOmniScheduler) registeredAt(rid, host string) time.Time {
	up := s.firstUp(rid, host)
	if s.regDelay == nil || up.IsZero() {
		return up
	}
	return up.Add(s.regDelay(rid, host))
}

func routerDeployment(oldID, newID string) *ct.Deployment {
	return &ct.Deployment{
		ID:           "dep-" + newID,
		AppID:        "router",
		OldReleaseID: oldID,
		NewReleaseID: newID,
		Strategy:     "one-down-one-up",
		Processes:    map[string]int{"app": 1},
		Tags:         map[string]map[string]string{"app": {}},
	}
}

// newRouterCluster runs release r0 up on every host.
func newRouterCluster(t *testing.T, hosts ...string) *fakeOmniScheduler {
	t.Helper()
	s := newFakeOmniScheduler(hosts...)
	routerReleases(s.fakeFormationCluster, "r0", "r1", "r2")
	s.mu.Lock()
	s.formations["r0"] = &ct.Formation{AppID: "router", ReleaseID: "r0", Processes: map[string]int{"app": 1}, Tags: map[string]map[string]string{"app": {}}}
	s.reconcileLocked(time.Now().Add(-time.Minute))
	s.mu.Unlock()
	prev := omniServiceInstances
	omniServiceInstances = s.instances
	t.Cleanup(func() { omniServiceInstances = prev })
	return s
}

func performOmniDeploy(s *fakeOmniScheduler, dep *ct.Deployment) error {
	j := &DeployJob{
		Deployment:   dep,
		client:       s,
		deployEvents: make(chan ct.DeploymentEvent, 256),
		logger:       log15.New(),
		stop:         make(chan struct{}),
	}
	j.logger.SetHandler(log15.DiscardHandler())
	return j.Perform()
}

// testOmniWindow is the stabilization window of these tests.
const testOmniWindow = 200 * time.Millisecond

func shortenOmniWaits(t *testing.T) {
	t.Helper()
	poll, limit, force, settle := omniStopPoll, omniStopWaitLimit, omniForceStopAfter, omniPortReleaseSettle
	window, cover := omniStabilizeWindow, omniCoverWaitLimit
	omniStopPoll, omniStopWaitLimit, omniForceStopAfter, omniPortReleaseSettle = 10*time.Millisecond, 10*time.Second, 5*time.Second, 10*time.Millisecond
	omniStabilizeWindow, omniCoverWaitLimit = testOmniWindow, 10*time.Second
	t.Cleanup(func() {
		omniStopPoll, omniStopWaitLimit, omniForceStopAfter, omniPortReleaseSettle = poll, limit, force, settle
		omniStabilizeWindow, omniCoverWaitLimit = window, cover
	})
}

// The other tests of this package use fakes whose jobs are up at once; the
// window is tested here.
func init() { omniStabilizeWindow = 0 }

func assertOneUpJobPerHost(t *testing.T, s *fakeOmniScheduler, rid string, when string) {
	t.Helper()
	got := s.upJobs(rid)
	var bad []string
	for _, h := range s.hosts {
		if got[h] != 1 {
			bad = append(bad, fmt.Sprintf("%s=%d", h, got[h]))
		}
	}
	if len(bad) > 0 {
		t.Fatalf("%s: up %s jobs per host %v, want exactly one on every host (%s)", when, rid, got, strings.Join(bad, " "))
	}
}

// The live roll (EDGE-11 checks): the new router on host 1 was still
// starting when the roll stopped the old router on host 2, so for that
// moment only one of three hosts served.
func TestOmniRollWaitsForTheNewJobToServeBeforeTheNextHost(t *testing.T) {
	shortenOmniWaits(t)
	s := newRouterCluster(t, "h1", "h2", "h3")
	s.delay = func(rid, host string, n int) (time.Duration, time.Duration) {
		if rid == "r1" && host == "h1" {
			return 0, 800 * time.Millisecond
		}
		return 0, 50 * time.Millisecond
	}
	start := time.Now()
	if err := performOmniDeploy(s, routerDeployment("r0", "r1")); err != nil {
		t.Fatal(err)
	}
	end := time.Now()

	up1, stop2 := s.firstUp("r1", "h1"), s.stopOf("r0", "h2")
	if up1.IsZero() || stop2.IsZero() {
		t.Fatalf("missing events: new h1 up %v, old h2 stop %v", up1, stop2)
	}
	if stop2.Before(up1) {
		t.Fatalf("the roll stopped the old job on h2 %s before the new job on h1 was up", up1.Sub(stop2).Round(time.Millisecond))
	}
	if stop2.Before(up1.Add(testOmniWindow)) {
		t.Fatalf("the roll stopped the old job on h2 %s after the new job on h1 came up, before the %s window", stop2.Sub(up1).Round(time.Millisecond), testOmniWindow)
	}
	if n, at := s.minServing(start, end); n < 2 {
		t.Fatalf("only %d of 3 hosts served at +%s during the roll, want at least 2", n, at.Sub(start).Round(time.Millisecond))
	}
	assertOneUpJobPerHost(t, s, "r1", "when the deploy finished")
	if got := s.upJobs("r0"); len(got) != 0 {
		t.Fatalf("old release still up on %v", got)
	}
}

// The live double deploy (deployments ca836962, 8e96c241): the first roll
// said complete while the scheduler had not yet placed the new router on
// the last host, so the second roll's plan left that host out.
func TestOmniBackToBackDeploysCoverEveryHost(t *testing.T) {
	shortenOmniWaits(t)
	s := newRouterCluster(t, "h1", "h2", "h3")
	s.delay = func(rid, host string, n int) (time.Duration, time.Duration) {
		if rid == "r1" && host == "h3" {
			// the scheduler places the job on h3 late
			return 400 * time.Millisecond, 100 * time.Millisecond
		}
		return 0, 50 * time.Millisecond
	}
	start := time.Now()
	if err := performOmniDeploy(s, routerDeployment("r0", "r1")); err != nil {
		t.Fatal(err)
	}
	assertOneUpJobPerHost(t, s, "r1", "when the first deploy finished")
	if err := performOmniDeploy(s, routerDeployment("r1", "r2")); err != nil {
		t.Fatal(err)
	}
	end := time.Now()
	assertOneUpJobPerHost(t, s, "r2", "when the second deploy finished")
	if n, at := s.minServing(start, end); n < 2 {
		t.Fatalf("only %d of 3 hosts served at +%s during the two rolls, want at least 2", n, at.Sub(start).Round(time.Millisecond))
	}
}

// A router that is up but has not registered router-api yet is not
// serving: the next host's old router keeps running until it registers.
func TestOmniRollWaitsForTheDiscoverdInstanceOfTheNewJob(t *testing.T) {
	shortenOmniWaits(t)
	s := newRouterCluster(t, "h1", "h2", "h3")
	s.regDelay = func(rid, host string) time.Duration {
		if rid == "r1" && host == "h1" {
			return 700 * time.Millisecond
		}
		return 0
	}
	if err := performOmniDeploy(s, routerDeployment("r0", "r1")); err != nil {
		t.Fatal(err)
	}
	reg1, stop2 := s.registeredAt("r1", "h1"), s.stopOf("r0", "h2")
	if stop2.Before(reg1) {
		t.Fatalf("the roll stopped the old job on h2 %s before the new job on h1 registered router-api", reg1.Sub(stop2).Round(time.Millisecond))
	}
	assertOneUpJobPerHost(t, s, "r1", "when the deploy finished")
}

// A new job that goes down inside the window does not count: the roll
// waits for its replacement to stay up.
func TestOmniRollRestartsTheWindowWhenTheNewJobCrashes(t *testing.T) {
	shortenOmniWaits(t)
	s := newRouterCluster(t, "h1", "h2", "h3")
	s.crashAfter = func(rid, host string, n int) time.Duration {
		if rid == "r1" && host == "h1" && n == 0 {
			return testOmniWindow / 2
		}
		return 0
	}
	if err := performOmniDeploy(s, routerDeployment("r0", "r1")); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	var second time.Time
	for _, j := range s.jobs {
		if j.rid == "r1" && j.host == "h1" && !j.crashed {
			second = j.upAt
		}
	}
	s.mu.Unlock()
	stop2 := s.stopOf("r0", "h2")
	if second.IsZero() || stop2.Before(second.Add(testOmniWindow)) {
		t.Fatalf("the roll stopped the old job on h2 at %v, before the replacement on h1 (up %v) stayed up for %s", stop2, second, testOmniWindow)
	}
	assertOneUpJobPerHost(t, s, "r1", "when the deploy finished")
}

// A host that joins during the roll is covered: the roll ends with a new
// router on it too.
func TestOmniRollCoversAHostThatJoinsDuringTheRoll(t *testing.T) {
	shortenOmniWaits(t)
	s := newRouterCluster(t, "h1", "h2")
	s.delay = func(rid, host string, n int) (time.Duration, time.Duration) {
		if rid == "r1" && host == "h1" {
			return 0, 300 * time.Millisecond
		}
		return 0, 50 * time.Millisecond
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.setHosts("h1", "h2", "h3")
	}()
	if err := performOmniDeploy(s, routerDeployment("r0", "r1")); err != nil {
		t.Fatal(err)
	}
	assertOneUpJobPerHost(t, s, "r1", "when the deploy finished")
}

// A host that leaves during the roll is no longer waited for.
func TestOmniRollDoesNotWaitForAHostThatLeft(t *testing.T) {
	shortenOmniWaits(t)
	omniStopWaitLimit = 2 * time.Second
	s := newRouterCluster(t, "h1", "h2", "h3")
	s.delay = func(rid, host string, n int) (time.Duration, time.Duration) {
		if rid == "r1" && host == "h3" {
			return time.Hour, 0
		}
		if rid == "r1" && host == "h1" {
			return 0, 300 * time.Millisecond
		}
		return 0, 50 * time.Millisecond
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.setHosts("h1", "h2")
	}()
	start := time.Now()
	if err := performOmniDeploy(s, routerDeployment("r0", "r1")); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > omniStopWaitLimit {
		t.Fatalf("the roll took %s: it waited for the host that left", took)
	}
	assertOneUpJobPerHost(t, s, "r1", "when the deploy finished")
}

// A host that never gets an up job of the new release fails the deploy at
// the deadline, naming the host, and the rollback brings the old release
// back on every host.
func TestOmniRollFailsWhenAHostNeverRunsTheNewRelease(t *testing.T) {
	shortenOmniWaits(t)
	omniCoverWaitLimit = time.Second
	s := newRouterCluster(t, "h1", "h2", "h3")
	s.delay = func(rid, host string, n int) (time.Duration, time.Duration) {
		if rid == "r1" && host == "h3" {
			return time.Hour, 0
		}
		return 0, 50 * time.Millisecond
	}
	dep := routerDeployment("r0", "r1")
	orig, err := s.GetFormation(dep.AppID, dep.OldReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	err = performOmniDeploy(s, dep)
	if err == nil {
		t.Fatal("the deploy said complete with no router of the new release on h3")
	}
	if !strings.Contains(err.Error(), "h3: no job") {
		t.Fatalf("error does not name the host: %v", err)
	}
	c := &context{client: s, logger: log15.New()}
	if err := c.rollback(log15.New(), dep, orig, make(chan struct{})); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(s.upJobs("r0")) < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	assertOneUpJobPerHost(t, s, "r0", "after the rollback")
}

// A failed HostList is one missed poll (EDGE-65).
func TestOmniRollToleratesTransientHostListError(t *testing.T) {
	shortenOmniWaits(t)
	shortTransientBudgets(t)
	s := newRouterCluster(t, "h1", "h2", "h3")
	calls := 0
	s.hostListErr = func() error {
		calls++
		if calls == 2 || calls == 5 {
			return err503
		}
		return nil
	}
	if err := performOmniDeploy(s, routerDeployment("r0", "r1")); err != nil {
		t.Fatalf("deploy failed on a transient HostList error: %v", err)
	}
	assertOneUpJobPerHost(t, s, "r1", "when the deploy finished")
}

func schedulerReleasesOn(s *fakeOmniScheduler, hosts ...string) {
	for _, id := range []string{"r0", "r1"} {
		s.releases[id] = &ct.Release{ID: id, Processes: map[string]ct.ProcessType{"scheduler": {Omni: true}}}
	}
	s.mu.Lock()
	s.formations["r0"] = &ct.Formation{AppID: "controller", ReleaseID: "r0", Processes: map[string]int{"scheduler": 1}, Tags: map[string]map[string]string{"scheduler": {}}}
	s.reconcileLocked(time.Now().Add(-time.Minute))
	s.mu.Unlock()
}

func schedulerDeployment() *ct.Deployment {
	return &ct.Deployment{ID: "dep-r1", AppID: "controller", OldReleaseID: "r0", NewReleaseID: "r1", Strategy: "one-by-one",
		Processes: map[string]int{"scheduler": 1}, Tags: map[string]map[string]string{"scheduler": {}}}
}

// Overlay omni (the controller scheduler) starts the last host's new job
// and waits for it before it stops the last old one, and a host that is
// still joining (no job of either release) is not waited for.
func TestOverlayOmniRollStartsTheLastHostFirstAndSkipsAJoiningHost(t *testing.T) {
	shortenOmniWaits(t)
	s := newFakeOmniScheduler("h1", "h2", "h3")
	schedulerReleasesOn(s)
	s.delay = func(rid, host string, n int) (time.Duration, time.Duration) {
		if rid == "r1" && host == "h3" {
			return 0, 500 * time.Millisecond
		}
		if host == "h4" {
			// joining: no job of either release starts there yet
			return time.Hour, 0
		}
		return 0, 50 * time.Millisecond
	}
	s.setHosts("h1", "h2", "h3", "h4")
	if err := performOmniDeploy(s, schedulerDeployment()); err != nil {
		t.Fatal(err)
	}
	up3, stop3 := s.firstUp("r1", "h3"), s.stopOf("r0", "h3")
	if stop3.Before(up3.Add(testOmniWindow)) {
		t.Fatalf("the old scheduler on h3 stopped at %v, before the new one (up %v) served", stop3, up3)
	}
	got := s.upJobs("r1")
	for _, h := range []string{"h1", "h2", "h3"} {
		if got[h] != 1 {
			t.Fatalf("new schedulers per host %v, want one on h1-h3", got)
		}
	}
}

func TestParseOmniStabilize(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"":       omniStabilizeDefault,
		"0":      0,
		"3":      3 * time.Second,
		"2500ms": 2500 * time.Millisecond,
		"bad":    omniStabilizeDefault,
		"-1":     omniStabilizeDefault,
	} {
		if got := parseOmniStabilize(raw); got != want {
			t.Errorf("parseOmniStabilize(%q) = %s, want %s", raw, got, want)
		}
	}
	if omniStabilizeDefault != 5*time.Second {
		t.Fatalf("default window %s, the docs say 5s", omniStabilizeDefault)
	}
}

// A draining host is still listed until its down event, but the scheduler
// places nothing there: the roll must not wait for it.
func TestOmniRollDoesNotWaitForAShuttingDownHost(t *testing.T) {
	shortenOmniWaits(t)
	omniCoverWaitLimit = time.Second
	s := newRouterCluster(t, "h1", "h2", "h3")
	s.shuttingDown = map[string]bool{"h3": true}
	s.delay = func(rid, host string, n int) (time.Duration, time.Duration) {
		if host == "h3" && rid != "r0" {
			return time.Hour, 0
		}
		return 0, 50 * time.Millisecond
	}
	if err := performOmniDeploy(s, routerDeployment("r0", "r1")); err != nil {
		t.Fatalf("the roll waited for the draining host: %v", err)
	}
	if got := s.upJobs("r1"); got["h1"] != 1 || got["h2"] != 1 {
		t.Fatalf("new routers per host %v, want one on h1 and h2", got)
	}
}
