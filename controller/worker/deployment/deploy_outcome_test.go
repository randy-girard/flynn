package deployment

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flynn/que-go"
	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
)

// EDGE-75: a deployment's outcome and its finished_at are one write, made
// after the rollback; a worker that claims the job again while another one
// rolls it back does not run Perform again.

// fakeDeployStore is the controller database as HandleDeployment sees it.
// ops is the order of writes, shared with the fake controller's formation
// changes.
type fakeDeployStore struct {
	mu          sync.Mutex
	ops         *[]string
	events      []ct.DeploymentEvent
	finishedAt  *time.Time
	rollbackAt  time.Time
	rollbackMsg string
	rollingBack bool
	markErr     error
	finishCalls int
}

func (s *fakeDeployStore) op(o string) { *s.ops = append(*s.ops, o) }

func (s *fakeDeployStore) CreateEvent(e ct.DeploymentEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Status == "" {
		e.Status = "running"
	}
	s.events = append(s.events, e)
	s.op("event:" + e.Status)
	return nil
}

func (s *fakeDeployStore) MarkRollingBack(d *ct.Deployment, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markErr != nil {
		return s.markErr
	}
	s.events = append(s.events, ct.DeploymentEvent{Status: "running", RollingBack: true, Progress: "rolling back: " + msg})
	s.rollingBack, s.rollbackAt, s.rollbackMsg = true, time.Now(), msg
	s.op("mark:rolling_back")
	return nil
}

func (s *fakeDeployStore) RollbackStarted(id string) (time.Time, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rollbackAt, s.rollbackMsg, s.rollingBack, nil
}

func (s *fakeDeployStore) Finish(id, status, msg string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishCalls++
	if s.finishedAt != nil {
		return false, nil
	}
	now := time.Now()
	s.finishedAt = &now
	s.events = append(s.events, ct.DeploymentEvent{Status: status, Error: msg})
	s.op("finish:" + status)
	return true, nil
}

// SetFinishedOnly is the old write: finished_at with no outcome.
func (s *fakeDeployStore) SetFinishedOnly(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.finishedAt = &now
	s.op("finished_at")
	return nil
}

func (s *fakeDeployStore) StartNextQueued(appID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.op("start_next")
	return nil
}

// view is what GetDeployment returns: deployment_status() is the latest
// event's status.
func (s *fakeDeployStore) view(d ct.Deployment) *ct.Deployment {
	s.mu.Lock()
	defer s.mu.Unlock()
	d.Status = "pending"
	if n := len(s.events); n > 0 {
		d.Status = s.events[n-1].Status
	}
	if s.finishedAt != nil {
		t := *s.finishedAt
		d.FinishedAt = &t
	}
	return &d
}

func (s *fakeDeployStore) lastEvent() ct.DeploymentEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return ct.DeploymentEvent{}
	}
	return s.events[len(s.events)-1]
}

// outcomeClient is the fake controller for HandleDeployment: formations from
// fakeFormationCluster, the deployment record from the fake store.
type outcomeClient struct {
	*fakeFormationCluster
	store    *fakeDeployStore
	dep      ct.Deployment
	ops      *[]string
	performs int32
	// beforeRestore runs (without any lock held) when the rollback writes
	// the old formation back, before the write.
	beforeRestore func()
	// seen is GetDeployment as another reader saw it during the rollback.
	seen []*ct.Deployment
}

func (c *outcomeClient) GetDeployment(id string) (*ct.Deployment, error) {
	return c.store.view(c.dep), nil
}

func (c *outcomeClient) GetRelease(id string) (*ct.Release, error) {
	if id == c.dep.OldReleaseID {
		// Perform starts by reading the old release.
		atomic.AddInt32(&c.performs, 1)
	}
	return c.fakeFormationCluster.GetRelease(id)
}

func (c *outcomeClient) ScaleAppRelease(appID, releaseID string, opts ct.ScaleOptions) error {
	if releaseID == c.dep.OldReleaseID && opts.Processes["web"] > 0 {
		c.seen = append(c.seen, c.store.view(c.dep))
		if f := c.beforeRestore; f != nil {
			c.beforeRestore = nil
			f()
		}
		*c.ops = append(*c.ops, "restore:"+releaseID)
	} else {
		*c.ops = append(*c.ops, "scale:"+releaseID)
	}
	return c.fakeFormationCluster.ScaleAppRelease(appID, releaseID, opts)
}

func (c *outcomeClient) DeleteFormation(appID, releaseID string) error {
	c.seen = append(c.seen, c.store.view(c.dep))
	*c.ops = append(*c.ops, "delete:"+releaseID)
	return c.fakeFormationCluster.DeleteFormation(appID, releaseID)
}

func (c *outcomeClient) SetAppRelease(appID, releaseID string) error {
	*c.ops = append(*c.ops, "set_release:"+releaseID)
	return c.fakeFormationCluster.SetAppRelease(appID, releaseID)
}

func (c *outcomeClient) GetApp(id string) (*ct.App, error) { return &ct.App{ID: id}, nil }

func (c *outcomeClient) ScheduleAppGarbageCollection(appID string) error { return nil }

var errWebFails = errors.New("web job failed to start: exit 1")

// newOutcomeFixture is an all-at-once app on release old (web=2) deploying
// release new; failNew makes the new web jobs fail to start.
func newOutcomeFixture(failNew bool) (*context, *outcomeClient, *fakeDeployStore, *[]string) {
	ops := &[]string{}
	f := newFakeFormationCluster("h1")
	for _, id := range []string{"old", "new"} {
		f.releases[id] = &ct.Release{ID: id, Processes: map[string]ct.ProcessType{"web": {}}}
	}
	f.formations["old"] = &ct.Formation{AppID: "app", ReleaseID: "old", Processes: map[string]int{"web": 2}}
	f.appRelease = "old"
	if failNew {
		f.waitErr["new"] = errWebFails
	}
	store := &fakeDeployStore{ops: ops}
	client := &outcomeClient{
		fakeFormationCluster: f,
		store:                store,
		ops:                  ops,
		dep: ct.Deployment{
			ID: "dep-1", AppID: "app", OldReleaseID: "old", NewReleaseID: "new",
			Strategy: "all-at-once", Processes: map[string]int{"web": 2}, DeployTimeout: 10,
		},
	}
	c := &context{client: client, logger: log15.New(), store: store}
	return c, client, store, ops
}

func deployQueJob(t *testing.T) *que.Job {
	t.Helper()
	args, err := json.Marshal(ct.DeployID{ID: "dep-1"})
	if err != nil {
		t.Fatal(err)
	}
	return &que.Job{Args: args, Stop: make(chan struct{})}
}

func indexOf(ops []string, o string) int {
	for i, x := range ops {
		if x == o {
			return i
		}
	}
	return -1
}

// The CORE-52 shape: a failed deploy rolls back. While it does, a reader
// (flynn-host update polling GetDeployment) must not see the deployment as
// finished, and when it is finished the failed outcome is already there.
func TestFailedDeployIsNotFinishedBeforeTheRollbackCompletes(t *testing.T) {
	c, client, store, ops := newOutcomeFixture(true)
	err := c.HandleDeployment(deployQueJob(t))
	if err != nil {
		t.Fatalf("HandleDeployment: %v (the rollback succeeded, the job is done)", err)
	}
	if len(client.seen) == 0 {
		t.Fatal("the rollback never ran")
	}
	for i, d := range client.seen {
		if d.FinishedAt != nil {
			t.Errorf("rollback step %d: the deployment already had finished_at (status %q) while it was being rolled back", i, d.Status)
		}
	}
	restore, del, fin := indexOf(*ops, "restore:old"), indexOf(*ops, "delete:new"), indexOf(*ops, "finish:failed")
	if restore < 0 || del < 0 || fin < 0 || fin < restore || fin < del {
		t.Errorf("ops %v: want the rollback (restore old, delete new) and then one write of finished_at with the failed outcome", *ops)
	}
	if i := indexOf(*ops, "finished_at"); i >= 0 {
		t.Errorf("ops %v: finished_at was written without the outcome", *ops)
	}
	if last := store.lastEvent(); last.Status != "failed" || !strings.Contains(last.Error, errWebFails.Error()) {
		t.Errorf("last event %+v, want failed with the deploy error", last)
	}
	if got := store.view(client.dep); got.FinishedAt == nil || got.Status != "failed" {
		t.Errorf("final deployment %+v, want finished and failed", got)
	}
	if n := indexOf(*ops, "start_next"); n < fin {
		t.Errorf("ops %v: the next queued deployment must start only after this one is finished", *ops)
	}
}

// A second worker claims the job while the first is rolling back (the que
// lock is lost when the platform Postgres primary moves). It must not run
// Perform again, and it must not finish the deployment either.
func TestSecondClaimDuringRollbackDoesNotRerunPerform(t *testing.T) {
	c, client, store, ops := newOutcomeFixture(true)
	var secondErr error
	var performsBefore, performsAfter int32
	client.beforeRestore = func() {
		performsBefore = atomic.LoadInt32(&client.performs)
		c2 := &context{client: client, logger: log15.New(), store: store}
		secondErr = c2.HandleDeployment(deployQueJob(t))
		performsAfter = atomic.LoadInt32(&client.performs)
	}
	if err := c.HandleDeployment(deployQueJob(t)); err != nil {
		t.Fatalf("first worker: %v", err)
	}
	if performsAfter != performsBefore {
		t.Fatalf("the second claim ran Perform again during the rollback (ops %v)", *ops)
	}
	if secondErr == nil {
		t.Error("the second claim returned nil during the rollback; it must return an error so que keeps the job until the deployment is finished")
	}
	if store.finishCalls != 1 || indexOf(*ops, "finish:failed") < 0 {
		t.Errorf("finish calls %d, ops %v: want exactly one finish, by the worker that rolled back", store.finishCalls, *ops)
	}
}

// A claim that finds a rollback started recently leaves it to that worker.
func TestClaimWithARecentRollbackMarkerWaits(t *testing.T) {
	c, client, store, ops := newOutcomeFixture(true)
	store.rollingBack, store.rollbackAt, store.rollbackMsg = true, time.Now().Add(-time.Minute), "web job failed"
	store.events = []ct.DeploymentEvent{{Status: "running", RollingBack: true}}
	err := c.HandleDeployment(deployQueJob(t))
	if err == nil {
		t.Error("want an error so que retries the job later")
	}
	if n := atomic.LoadInt32(&client.performs); n != 0 {
		t.Errorf("Perform ran %d times", n)
	}
	if len(*ops) != 0 {
		t.Errorf("ops %v, want none", *ops)
	}
}

// When the worker that marked the rollback is gone (the marker is older than
// any rollback takes), the next claim rolls back again and finishes the
// deployment as failed, without running Perform.
func TestClaimWithAStaleRollbackMarkerFinishesTheRollback(t *testing.T) {
	c, client, store, ops := newOutcomeFixture(true)
	// What the first worker left: new release scaled up, old restored or not.
	client.fakeFormationCluster.formations["new"] = &ct.Formation{AppID: "app", ReleaseID: "new", Processes: map[string]int{"web": 2}}
	client.fakeFormationCluster.formations["old"].Processes["web"] = 0
	store.rollingBack, store.rollbackAt, store.rollbackMsg = true, time.Now().Add(-rollbackTakeoverAfter-time.Minute), "web job failed to start: exit 1"
	store.events = []ct.DeploymentEvent{{Status: "running", RollingBack: true}}
	if err := c.HandleDeployment(deployQueJob(t)); err != nil {
		t.Fatalf("HandleDeployment: %v", err)
	}
	if n := atomic.LoadInt32(&client.performs); n != 0 {
		t.Errorf("Perform ran %d times", n)
	}
	if got := client.fakeFormationCluster.formations["old"].Processes["web"]; got != 2 {
		t.Errorf("old web=%d, want 2 (the deployment's original count)", got)
	}
	if _, ok := client.fakeFormationCluster.formations["new"]; ok {
		t.Error("the new formation is still there")
	}
	last := store.lastEvent()
	if last.Status != "failed" || !strings.Contains(last.Error, "web job failed to start: exit 1") {
		t.Errorf("last event %+v, want failed with the recorded deploy error", last)
	}
	if indexOf(*ops, "finish:failed") < 0 {
		t.Errorf("ops %v", *ops)
	}
}

// If the rollback cannot be marked (the database is down), nothing is rolled
// back or finished; que retries the job.
func TestFailedDeployWithoutARollbackMarkerDoesNotRollBack(t *testing.T) {
	c, client, store, ops := newOutcomeFixture(true)
	store.markErr = errors.New("dial tcp: connection refused")
	if err := c.HandleDeployment(deployQueJob(t)); err == nil {
		t.Error("want an error so que retries the job")
	}
	if len(client.seen) != 0 || indexOf(*ops, "restore:old") >= 0 || indexOf(*ops, "delete:new") >= 0 {
		t.Errorf("ops %v: rolled back without a marker", *ops)
	}
	if store.finishedAt != nil {
		t.Errorf("ops %v: finished without an outcome", *ops)
	}
}

// A successful deploy is finished with status complete in the same write,
// after the new release is current.
func TestSuccessfulDeployFinishesWithTheCompleteOutcome(t *testing.T) {
	c, client, store, ops := newOutcomeFixture(false)
	if err := c.HandleDeployment(deployQueJob(t)); err != nil {
		t.Fatal(err)
	}
	set, fin := indexOf(*ops, "set_release:new"), indexOf(*ops, "finish:complete")
	if set < 0 || fin < set {
		t.Errorf("ops %v: want set_release then finish:complete", *ops)
	}
	if indexOf(*ops, "finished_at") >= 0 || indexOf(*ops, "event:complete") >= 0 {
		t.Errorf("ops %v: complete must be written with finished_at, not apart from it", *ops)
	}
	if got := store.view(client.dep); got.Status != "complete" || got.FinishedAt == nil {
		t.Errorf("final deployment %+v", got)
	}
}

// The failed event carries what the rollback left (EDGE-76 (d)), which
// flynn-host update and `flynn deployment` print.
func TestFailedEventCarriesTheRollbackError(t *testing.T) {
	shortTransientBudgets(t)
	c, client, store, _ := newOutcomeFixture(true)
	client.fakeFormationCluster.putErr["old"] = errors.New("old formation write refused")
	if err := c.HandleDeployment(deployQueJob(t)); err == nil {
		t.Error("a failed rollback must return its error so que retries")
	}
	last := store.lastEvent()
	// The restore write is retried (EDGE-65), then the rollback reports
	// the last error.
	if last.Status != "failed" || !strings.Contains(last.Error, errWebFails.Error()+"; rollback: restore the original formation: ") ||
		!strings.Contains(last.Error, "last error: old formation write refused") {
		t.Errorf("last event %+v, want failed with the deploy and the rollback errors", last)
	}
}
