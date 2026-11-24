package deployment

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
)

// sireniaFollower makes a fakeSireniaCluster follow the formations, as the
// scheduler and sirenia do. A formation scaled below the number of members
// of its release stops members of that release (asyncs first, the primary
// last; the next member then takes over as primary). A formation that asks
// for more engines than its release has members starts new peers, but they
// join the cluster one at a time, one per read of the cluster state: a
// restarted peer takes a while to start and register.
type sireniaFollower struct {
	*fakeSireniaCluster
	processType string
	rolling     bool
	pending     []string
	// primaryStopped is set when a formation change stopped the primary.
	primaryStopped bool
	// lowOn is the lowest number of members of a release seen once it had
	// the primary.
	lowOn map[string]int
	// failAt makes the fake HA roll fail: "sync" once the asyncs and the
	// sync were replaced (the primary is still on the old release),
	// "read-write" once the primary moved to the new release.
	failAt string
}

func (s *sireniaFollower) countOf(list []string, rel string) int {
	n := 0
	for _, m := range list {
		if m == rel {
			n++
		}
	}
	return n
}

func (s *sireniaFollower) noteLow() {
	s.note()
	if s.lowOn == nil {
		s.lowOn = map[string]int{}
	}
	p := s.primary()
	if _, ok := s.lowOn[p]; !ok && p != "" {
		s.lowOn[p] = s.countOf(s.members, p)
	}
	for rel, low := range s.lowOn {
		if n := s.countOf(s.members, rel); n < low {
			s.lowOn[rel] = n
		}
	}
}

// follow runs after every formation change, with the fake's lock held.
func (s *sireniaFollower) follow(f *fakeFormationCluster) {
	want := func(rel string) int {
		if form := f.formations[rel]; form != nil {
			return form.Processes[s.processType]
		}
		return 0
	}
	// stop members (pending ones first) of releases scaled down
	for _, rel := range append(append([]string(nil), s.members...), s.pending...) {
		for s.countOf(s.members, rel)+s.countOf(s.pending, rel) > want(rel) {
			if i := lastIndex(s.pending, rel); i >= 0 {
				s.pending = append(s.pending[:i], s.pending[i+1:]...)
				continue
			}
			i := lastIndex(s.members, rel)
			if i == 0 {
				s.primaryStopped = true
			}
			s.members = append(s.members[:i], s.members[i+1:]...)
			s.noteLow()
		}
	}
	if s.rolling {
		return
	}
	for rel, form := range f.formations {
		for n := form.Processes[s.processType] - s.countOf(s.members, rel) - s.countOf(s.pending, rel); n > 0; n-- {
			s.pending = append(s.pending, rel)
		}
	}
}

func lastIndex(list []string, rel string) int {
	for i := len(list) - 1; i >= 0; i-- {
		if list[i] == rel {
			return i
		}
	}
	return -1
}

// join lets one pending peer register (asyncs are appended at the tail).
func (s *sireniaFollower) join() {
	if len(s.pending) == 0 {
		return
	}
	s.members = append(s.members, s.pending[0])
	s.pending = s.pending[1:]
	s.noteLow()
}

func (s *sireniaFollower) installFollowing(t *testing.T, f *fakeFormationCluster) {
	t.Helper()
	s.install(t, f)
	s.noteLow()
	origMembers, origWait, origPoll := sireniaClusterReleases, sireniaRollbackJoinTimeout, sireniaRollbackPoll
	t.Cleanup(func() {
		sireniaClusterReleases, sireniaRollbackJoinTimeout, sireniaRollbackPoll = origMembers, origWait, origPoll
	})
	sireniaRollbackJoinTimeout, sireniaRollbackPoll = 2*time.Second, time.Millisecond
	sireniaPrimaryRelease = func(string) (string, error) {
		s.join()
		return s.primary(), nil
	}
	sireniaClusterReleases = func(string) ([]string, error) {
		s.join()
		return append([]string(nil), s.members...), nil
	}
	inner := f.observe
	f.observe = func(f *fakeFormationCluster) {
		if inner != nil {
			inner(f)
		}
		s.follow(f)
	}
	// the HA roll of sirenia.go: start a new peer, then stop an old one;
	// asyncs first, then the sync, then the primary (the new sync takes over)
	sireniaRollHA = func(d *DeployJob, processType string, proc ct.ProcessType, stopped *[]string, log log15.Logger) error {
		s.rolling = true
		defer func() { s.rolling = false }()
		old := len(s.members)
		for i := 0; i < old; i++ {
			idx := old - 1 - i
			if idx == 0 && s.failAt == "sync" {
				return errors.New("timed out waiting for successor to become the synchronous peer")
			}
			d.newFormation.Processes[processType]++
			if err := d.client.PutFormation(d.newFormation); err != nil {
				return err
			}
			s.members = append(s.members, d.NewReleaseID)
			s.noteLow()
			s.members = append(s.members[:idx], s.members[idx+1:]...)
			s.noteLow()
		}
		if s.failAt == "read-write" {
			return errors.New("new sirenia primary did not become read-write: timeout")
		}
		return nil
	}
}

// runFailedSireniaDeploy runs Perform, which must fail, and then the
// rollback, as HandleDeployment does.
func runFailedSireniaDeploy(t *testing.T, f *fakeFormationCluster, dep *ct.Deployment) (deployErr, rollbackErr error) {
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
	deployErr = j.Perform()
	if deployErr == nil {
		t.Fatal("expected the deploy to fail")
	}
	c := &context{client: f, logger: log15.New()}
	return deployErr, c.rollback(log15.New(), dep, orig, stop)
}

func newSireniaRollbackFixture(t *testing.T) (*fakeFormationCluster, *sireniaFollower, map[string]int) {
	f := newFakeFormationCluster("devnode1", "devnode2", "devnode3")
	postgresReleases(f, relCurrent, relNew)
	f.formations[relCurrent] = &ct.Formation{AppID: "postgres", ReleaseID: relCurrent, Processes: map[string]int{"postgres": 3, "web": 2}}
	f.appRelease = relCurrent
	s := &sireniaFollower{fakeSireniaCluster: &fakeSireniaCluster{members: []string{relCurrent, relCurrent, relCurrent}}, processType: "postgres"}
	low := watchCapacity(f)
	s.installFollowing(t, f)
	return f, s, low
}

func formationCount(f *fakeFormationCluster, rel, typ string) int {
	if form := f.formations[rel]; form != nil {
		return form.Processes[typ]
	}
	return 0
}

// EDGE-76. The sirenia deploy moved every engine, the primary included, to
// the new release; then the web roll failed. The rollback restored the old
// formation (postgres=3 again: fresh engines with no data) and deleted the
// new formation, which stopped the primary and the sync. Now the formation
// that runs the primary is kept, the old release's engines are not started
// again, and the error says what was done.
func TestSireniaRollbackKeepsTheFormationThatRunsThePrimary(t *testing.T) {
	f, s, low := newSireniaRollbackFixture(t)
	f.waitErr[relNew] = errors.New("web job failed to start: exit 1")

	deployErr, rbErr := runFailedSireniaDeploy(t, f, postgresDeploy(map[string]int{"postgres": 3, "web": 2}))
	t.Logf("deploy error: %v; rollback: %v", deployErr, rbErr)

	if s.primaryStopped {
		t.Errorf("the rollback stopped the primary (members now %v)", s.members)
	}
	if n := formationCount(f, relNew, "postgres"); n != 3 {
		t.Errorf("new release postgres=%d after the rollback, want 3: it runs the primary", n)
	}
	if s.lowOn[relNew] < 3 {
		t.Errorf("members on the new release dropped to %d once it had the primary, want never below 3", s.lowOn[relNew])
	}
	if s.primary() != relNew {
		t.Errorf("primary on %q, want %s (members %v)", s.primary(), relNew, s.members)
	}
	if n := formationCount(f, relCurrent, "postgres"); n != 0 {
		t.Errorf("old release postgres=%d, want 0: its peers were stopped and the data is on the new release", n)
	}
	if n := formationCount(f, relCurrent, "web"); n != 2 {
		t.Errorf("old release web=%d, want restored to 2", n)
	}
	if n := formationCount(f, relNew, "web"); n != 0 {
		t.Errorf("new release web=%d, want 0 (the failed type is rolled back)", n)
	}
	if low["postgres"] < 3 {
		t.Errorf("running engines dropped to %d, want never below 3", low["postgres"])
	}
	if s.lowMembers < 3 {
		t.Errorf("sirenia members dropped to %d, want never below 3", s.lowMembers)
	}
	if f.appRelease != relCurrent {
		t.Errorf("current release %s, want %s (the deploy failed)", f.appRelease, relCurrent)
	}
	if rbErr == nil || !strings.Contains(rbErr.Error(), "primary") || !strings.Contains(rbErr.Error(), relNew) {
		t.Errorf("rollback error %v, want one that says the primary runs from %s", rbErr, relNew)
	}
}

// The primary moved but never became read-write: the old formation still
// asks for three engines (its stopped peers were killed and restart with no
// data). The new formation is kept and the old one stops asking for engines.
func TestSireniaRollbackAfterThePrimaryMovedButIsNotReadWrite(t *testing.T) {
	f, s, low := newSireniaRollbackFixture(t)
	s.failAt = "read-write"

	_, rbErr := runFailedSireniaDeploy(t, f, postgresDeploy(map[string]int{"postgres": 3, "web": 2}))

	if s.primaryStopped || formationCount(f, relNew, "postgres") != 3 {
		t.Errorf("primary stopped=%v, new postgres=%d: the formation that runs the primary must stay", s.primaryStopped, formationCount(f, relNew, "postgres"))
	}
	if n := formationCount(f, relCurrent, "postgres"); n != 0 {
		t.Errorf("old release postgres=%d, want 0", n)
	}
	if low["postgres"] < 3 || s.lowMembers < 3 {
		t.Errorf("engines dropped to %d, members to %d; want never below 3", low["postgres"], s.lowMembers)
	}
	if rbErr == nil {
		t.Error("the rollback must report that it kept the new release's engines")
	}
}

// Mid-roll: the asyncs and the sync are on the new release, the primary is
// still on the old one. The rollback brings the old release back and may
// remove the new peers only once the old release's restarted peers have
// joined, so the cluster never runs fewer than three members.
func TestSireniaRollbackMidRollWaitsForTheOldPeers(t *testing.T) {
	f, s, low := newSireniaRollbackFixture(t)
	s.failAt = "sync"

	_, rbErr := runFailedSireniaDeploy(t, f, postgresDeploy(map[string]int{"postgres": 3, "web": 2}))
	if rbErr != nil {
		t.Fatalf("rollback: %v", rbErr)
	}
	if s.primaryStopped || s.primary() != relCurrent {
		t.Errorf("primary stopped=%v, on %q; want the old primary kept", s.primaryStopped, s.primary())
	}
	if _, ok := f.formations[relNew]; ok && formationCount(f, relNew, "postgres") != 0 {
		t.Errorf("new release postgres=%d after rolling back, want it removed", formationCount(f, relNew, "postgres"))
	}
	if n := formationCount(f, relCurrent, "postgres"); n != 3 {
		t.Errorf("old release postgres=%d, want 3", n)
	}
	if s.lowMembers < 3 {
		t.Errorf("sirenia members dropped to %d during the rollback, want never below 3 (members %v)", s.lowMembers, s.members)
	}
	if low["postgres"] < 3 {
		t.Errorf("running engines dropped to %d, want never below 3", low["postgres"])
	}
}

// Mid-roll, and the old release's peers never come back: the new peers
// are kept (they may be the sync) and the rollback says so.
func TestSireniaRollbackMidRollKeepsNewPeersWhenTheOldOnesDoNotJoin(t *testing.T) {
	f, s, _ := newSireniaRollbackFixture(t)
	s.failAt = "sync"
	orig, _ := f.GetFormation("postgres", relCurrent)
	dep := postgresDeploy(map[string]int{"postgres": 3, "web": 2})
	j := &DeployJob{Deployment: dep, client: f, deployEvents: make(chan ct.DeploymentEvent, 64), logger: log15.New(), stop: make(chan struct{})}
	if err := j.Perform(); err == nil {
		t.Fatal("expected the deploy to fail")
	}
	// the restarted peers never register
	sireniaClusterReleases = func(string) ([]string, error) { return append([]string(nil), s.members...), nil }
	sireniaPrimaryRelease = func(string) (string, error) { return s.primary(), nil }
	sireniaRollbackJoinTimeout = 20 * time.Millisecond
	c := &context{client: f, logger: log15.New()}
	rbErr := c.rollback(log15.New(), dep, orig, make(chan struct{}))

	if rbErr == nil || !strings.Contains(rbErr.Error(), relNew) {
		t.Errorf("rollback error %v, want one naming the kept release %s", rbErr, relNew)
	}
	if n := formationCount(f, relNew, "postgres"); n != 2 {
		t.Errorf("new release postgres=%d, want its 2 peers kept", n)
	}
	if s.lowMembers < 3 {
		t.Errorf("sirenia members dropped to %d, want never below 3", s.lowMembers)
	}
}

// Nothing moved yet (the deploy failed before the first new peer): the
// rollback is the plain one and leaves the old engines alone.
func TestSireniaRollbackBeforeAnyEngineMoved(t *testing.T) {
	f, s, low := newSireniaRollbackFixture(t)
	sireniaRollHA = func(d *DeployJob, processType string, proc ct.ProcessType, stopped *[]string, log log15.Logger) error {
		return errors.New("timed out waiting for current service event")
	}

	_, rbErr := runFailedSireniaDeploy(t, f, postgresDeploy(map[string]int{"postgres": 3, "web": 2}))
	if rbErr != nil {
		t.Fatalf("rollback: %v", rbErr)
	}
	if _, ok := f.formations[relNew]; ok {
		t.Errorf("new formation %v left after rolling back a deploy that moved nothing", f.formations[relNew].Processes)
	}
	if n := formationCount(f, relCurrent, "postgres"); n != 3 || s.primary() != relCurrent || s.lowMembers < 3 || low["postgres"] < 3 {
		t.Errorf("old postgres=%d primary=%q low members=%d low engines=%d; want 3, %s, 3, 3", n, s.primary(), s.lowMembers, low["postgres"], relCurrent)
	}
}

// The failed event carries what the rollback left (EDGE-76 (d)): a sirenia
// rollback that kept the new release's engines says so in the deployment's
// error, which flynn-host update and `flynn deployment` print.
func TestHandleDeploymentReportsTheRollbackOutcome(t *testing.T) {
	src, err := os.ReadFile("context.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	rb := strings.Index(body, "e = c.rollback(log, deployment, f, job.Stop)")
	add := strings.Index(body, `errMsg += "; rollback: " + e.Error()`)
	ev := strings.Index(body, "Error:     errMsg,")
	if rb < 0 || add < rb || ev < add {
		t.Fatal("the failed event must include the rollback's error")
	}
}
