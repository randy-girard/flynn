package deployment

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/updaterdeploy"
)

// EDGE-65: one transient controller error in the middle of a roll must not
// fail the deploy. These are the errors the deployer saw or can see while the
// controller it talks to is being replaced.
var (
	// discoverd while it catches up (EDGE-69), or a controller 503.
	err503 = httphelper.JSONError{Code: httphelper.ServiceUnavailableErrorCode, Message: "discoverd: store is catching up, retry", Retry: true}
	// a stale controller address (EDGE-29/EDGE-69).
	errRefused = &url.Error{Op: "Get", URL: "http://controller.discoverd/apps/controller/jobs",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}
	errForbidden = httphelper.JSONError{Code: httphelper.ForbiddenErrorCode, Message: "forbidden"}
)

// failTimes returns an error n times for calls that match, then nil.
type failTimes struct {
	mu    sync.Mutex
	left  map[string]int
	err   map[string]error
	calls map[string]int
}

func newFailTimes() *failTimes {
	return &failTimes{left: map[string]int{}, err: map[string]error{}, calls: map[string]int{}}
}

func (ft *failTimes) set(key string, n int, err error) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.left[key], ft.err[key] = n, err
}

func (ft *failTimes) hit(key string) error {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.calls[key]++
	if ft.left[key] > 0 {
		ft.left[key]--
		return ft.err[key]
	}
	return nil
}

func (ft *failTimes) failed(key string) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.calls[key] > 0 && ft.left[key] == 0
}

func shortTransientBudgets(t *testing.T) {
	t.Helper()
	oldLimit, oldPoll, oldForce, oldSettle := omniStopWaitLimit, omniStopPoll, omniForceStopAfter, omniPortReleaseSettle
	oldBudget, oldBackoff, oldMax := apiRetryBudget, apiRetryBackoff, apiRetryMaxBackoff
	omniStopWaitLimit, omniStopPoll, omniPortReleaseSettle = 2*time.Second, 10*time.Millisecond, 10*time.Millisecond
	apiRetryBudget, apiRetryBackoff, apiRetryMaxBackoff = time.Second, 5*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() {
		omniStopWaitLimit, omniStopPoll, omniForceStopAfter, omniPortReleaseSettle = oldLimit, oldPoll, oldForce, oldSettle
		apiRetryBudget, apiRetryBackoff, apiRetryMaxBackoff = oldBudget, oldBackoff, oldMax
	})
}

func newControllerRoll() *fakeFormationCluster {
	f := newFakeFormationCluster("devnode1", "devnode2", "devnode3")
	controllerReleases(f, "old", "new")
	f.formations["old"] = &ct.Formation{AppID: "controller", ReleaseID: "old",
		Processes: map[string]int{"scheduler": 1, "web": 2, "worker": 2},
		Tags:      map[string]map[string]string{"scheduler": {}}}
	return f
}

func performDeploy(f controller.Client, dep *ct.Deployment) (*DeployJob, error) {
	j := &DeployJob{
		Deployment:   dep,
		client:       f,
		deployEvents: make(chan ct.DeploymentEvent, 256),
		logger:       log15.New(),
		stop:         make(chan struct{}),
	}
	return j, j.Perform()
}

// The c7b6063a shape: the roll's last step scaled the old scheduler to zero
// and the next JobList timed out. A 503 on the first step as well.
func TestOmniRollToleratesTransientJobListError(t *testing.T) {
	shortTransientBudgets(t)
	f := newControllerRoll()
	ft := newFailTimes()
	ft.set("first-step", 1, err503)
	ft.set("old-zero", 1, errJobListTimeout)
	f.jobListErr = func(f *fakeFormationCluster) error {
		if n := f.formations["new"]; n != nil && n.Tags["scheduler"][ct.FormationHostIDsTag] == "devnode1" {
			if err := ft.hit("first-step"); err != nil {
				return err
			}
		}
		if f.formations["old"].Processes["scheduler"] == 0 {
			return ft.hit("old-zero")
		}
		return nil
	}
	if _, err := performDeploy(f, controllerDeployment("old", "new")); err != nil {
		t.Fatalf("deploy failed on a transient JobList error: %v", err)
	}
	if !ft.failed("first-step") || !ft.failed("old-zero") {
		t.Fatalf("the injected JobList failures did not happen: %v", ft.calls)
	}
	if n := f.formations["old"].Processes["scheduler"]; n != 0 {
		t.Fatalf("old scheduler = %d, want 0", n)
	}
	assertOneSchedulerPerHost(t, f, "new")
}

// Formation writes (scale requests) and reads fail once or twice mid-roll.
func TestOmniRollToleratesTransientFormationCalls(t *testing.T) {
	shortTransientBudgets(t)
	f := newControllerRoll()
	ft := newFailTimes()
	ft.set("scale/new", 2, err503)
	ft.set("scale/old", 1, errRefused)
	f.callErr = func(call, rid string) error {
		if call != "scale" {
			return nil
		}
		// fail once the roll is under way, not on the first write
		if n := f.formations["new"]; n == nil || n.Processes["scheduler"] == 0 {
			return nil
		}
		return ft.hit(call + "/" + rid)
	}
	if _, err := performDeploy(f, controllerDeployment("old", "new")); err != nil {
		t.Fatalf("deploy failed on a transient formation write: %v", err)
	}
	if !ft.failed("scale/new") || !ft.failed("scale/old") {
		t.Fatalf("the injected write failures did not happen: %v", ft.calls)
	}
	assertOneSchedulerPerHost(t, f, "new")
	if f.formations["new"].Processes["web"] != 2 || f.formations["old"].Processes["web"] != 0 {
		t.Fatalf("web not rolled: new %v old %v", f.formations["new"].Processes, f.formations["old"].Processes)
	}
}

// A permanent answer is not an outage: it still fails at once, well inside
// the loop's deadline.
func TestOmniRollFailsFastOnPermanentJobListError(t *testing.T) {
	shortTransientBudgets(t)
	omniStopWaitLimit = 30 * time.Second
	for name, perm := range map[string]error{
		"forbidden": errForbidden,
		"not found": controller.ErrNotFound,
		"401":       &url.Error{Op: "Get", URL: "http://controller.discoverd/apps/controller/jobs", Err: errors.New("httpclient: raw req: unexpected status 401")},
	} {
		t.Run(name, func(t *testing.T) {
			f := newControllerRoll()
			f.jobListErr = func(f *fakeFormationCluster) error {
				if f.formations["old"].Processes["scheduler"] == 0 {
					return perm
				}
				return nil
			}
			start := time.Now()
			_, err := performDeploy(f, controllerDeployment("old", "new"))
			if err == nil {
				t.Fatal("deploy succeeded on a permanent error")
			}
			if !errors.Is(err, perm) && err.Error() != perm.Error() {
				t.Fatalf("err = %v, want %v", err, perm)
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("a permanent error took %s to fail, want at once", d)
			}
		})
	}
}

// When the controller does not come back, the loop fails at its own deadline
// and says which loop, how long it waited and the last error.
func TestOmniWaitDeadlineErrorNamesLoopElapsedAndLastError(t *testing.T) {
	shortTransientBudgets(t)
	omniStopWaitLimit = 300 * time.Millisecond
	f := newControllerRoll()
	f.jobListErr = func(f *fakeFormationCluster) error {
		if f.formations["old"].Processes["scheduler"] == 0 {
			return errJobListTimeout
		}
		return nil
	}
	start := time.Now()
	_, err := performDeploy(f, controllerDeployment("old", "new"))
	if err == nil {
		t.Fatal("deploy succeeded while JobList kept failing")
	}
	if d := time.Since(start); d < omniStopWaitLimit {
		t.Fatalf("gave up after %s, before the loop's %s deadline", d, omniStopWaitLimit)
	}
	msg := err.Error()
	t.Logf("deadline error: %s", msg)
	for _, want := range []string{"timed out waiting for old scheduler jobs to stop", "omni roll", "after ", "last error", "timeout awaiting response headers"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	if !errors.Is(err, errJobListTimeout) {
		t.Errorf("error %q does not wrap the last JobList error", msg)
	}
	if !updaterdeploy.ShouldRetryAfterScaleTimeout(err) {
		t.Errorf("flynn-host update no longer recognises %q as an omni wait timeout", msg)
	}
}

// waitNewOmniJobsUp: JobList fails while the new jobs start.
func TestOmniNewJobsWaitToleratesTransientJobListError(t *testing.T) {
	shortTransientBudgets(t)
	f := newControllerRoll()
	ft := newFailTimes()
	ft.set("new-up", 2, errRefused)
	f.jobListErr = func(f *fakeFormationCluster) error {
		if n := f.formations["new"]; n != nil && n.Processes["scheduler"] > 0 && n.Tags["scheduler"][ct.FormationHostIDsTag] == "devnode1,devnode2" {
			return ft.hit("new-up")
		}
		return nil
	}
	if _, err := performDeploy(f, controllerDeployment("old", "new")); err != nil {
		t.Fatalf("deploy failed on a transient JobList error while new jobs started: %v", err)
	}
	if !ft.failed("new-up") {
		t.Fatalf("the injected failures did not happen: %v", ft.calls)
	}
	assertOneSchedulerPerHost(t, f, "new")
}

// The reads Perform starts with and the formation calls of an adopted sirenia
// engines release (EDGE-73) are retried too.
func TestSireniaEngineReleaseDownToleratesTransientCalls(t *testing.T) {
	shortTransientBudgets(t)
	for _, wait := range []bool{false, true} {
		f := newFakeFormationCluster("h1")
		f.formations["engines"] = &ct.Formation{AppID: "pg", ReleaseID: "engines", Processes: map[string]int{"postgres": 3, "web": 1}}
		ft := newFailTimes()
		ft.set("get_formation", 1, err503)
		ft.set("put_formation", 1, errRefused)
		ft.set("scale", 1, err503)
		f.callErr = func(call, rid string) error {
			if rid != "engines" {
				return nil
			}
			return ft.hit(call)
		}
		d := &DeployJob{Deployment: &ct.Deployment{AppID: "pg"}, client: f, logger: log15.New(), stop: make(chan struct{}),
			sireniaEngine: &ct.Formation{AppID: "pg", ReleaseID: "engines"}}
		if err := d.scaleSireniaEngineReleaseDown("postgres", wait, log15.New()); err != nil {
			t.Fatalf("wait=%v: %v", wait, err)
		}
		if got := f.formations["engines"].Processes; got["postgres"] != 0 || got["web"] != 1 {
			t.Fatalf("wait=%v: engines formation = %v, want postgres=0 web=1", wait, got)
		}
	}
}

// HandleDeployment: the old formation read before Perform and SetAppRelease
// after it fail once. Before, the first failed the deploy and the second
// rolled back a deploy that had finished rolling.
func TestHandleDeploymentRetriesTransientReadsAndSetAppRelease(t *testing.T) {
	shortTransientBudgets(t)
	c, client, store, ops := newOutcomeFixture(false)
	ft := newFailTimes()
	ft.set("get_formation/old", 1, errJobListTimeout)
	ft.set("get_release/new", 1, err503)
	ft.set("set_release/new", 1, errRefused)
	client.callErr = func(call, rid string) error { return ft.hit(call + "/" + rid) }
	if err := c.HandleDeployment(deployQueJob(t)); err != nil {
		t.Fatalf("HandleDeployment: %v", err)
	}
	for _, k := range []string{"get_formation/old", "get_release/new", "set_release/new"} {
		if !ft.failed(k) {
			t.Errorf("injected failure %s did not happen (%v)", k, ft.calls)
		}
	}
	if last := store.lastEvent(); last.Status != "complete" {
		t.Fatalf("deployment ended %q (%s), want complete; ops %v", last.Status, last.Error, *ops)
	}
	if client.appRelease != "new" {
		t.Fatalf("app release = %q, want new", client.appRelease)
	}
	if indexOf(*ops, "restore:old") >= 0 {
		t.Fatalf("ops %v: a finished deploy was rolled back", *ops)
	}
}

// A deleted release is an answer: Perform fails at once.
func TestPerformFailsFastOnDeletedRelease(t *testing.T) {
	shortTransientBudgets(t)
	f := newControllerRoll()
	delete(f.releases, "new")
	start := time.Now()
	_, err := performDeploy(f, controllerDeployment("old", "new"))
	if err != controller.ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %s", d)
	}
}

// The job list the release phase starts with is retried as well.
func TestReleasePhaseToleratesTransientJobList(t *testing.T) {
	shortTransientBudgets(t)
	f := newFakeFormationCluster("h1")
	ft := newFailTimes()
	ft.set("list", 2, errRefused)
	f.jobListErr = func(*fakeFormationCluster) error { return ft.hit("list") }
	zero := int32(0)
	f.jobsOverride = func(jobs []*ct.Job) []*ct.Job {
		return append(jobs, &ct.Job{ReleaseID: "new", Type: ct.ProcessTypeRelease, State: ct.JobStateDown, ExitStatus: &zero})
	}
	d := &DeployJob{Deployment: &ct.Deployment{AppID: "app", NewReleaseID: "new"}, client: f, logger: log15.New(), stop: make(chan struct{}),
		newRelease: &ct.Release{ID: "new", Processes: map[string]ct.ProcessType{ct.ProcessTypeRelease: {Args: []string{"migrate"}}}}}
	if err := d.runReleasePhase(log15.New()); err != nil {
		t.Fatalf("release phase failed on a transient JobList error: %v", err)
	}
	if !ft.failed("list") {
		t.Fatal("the injected failures did not happen")
	}
}
