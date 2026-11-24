package updaterdeploy

import (
	"errors"
	"fmt"
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
	host "github.com/randy-girard/flynn/host/types"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
)

// EDGE-69: right after the last host's discoverd was replaced, the rollout's
// first controller read (GET formation of the new release) failed once with
// `dial tcp 100.100.81.27:80: connect: connection refused` (an address from
// the new discoverd's old snapshot) and once with `service not found:
// "controller"`. The update stopped before any system app deploy; a rerun
// passed. The reads after the swap must wait for the controller like the
// formation writes already do.

// swapWindowController fails reads with the errors seen live for a number of
// calls once armed, as the controller is unreachable for a moment after the
// discoverd swap.
type swapWindowController struct {
	*fakeInfraController
	mu       sync.Mutex
	failures int
	failed   int
	// only, when set, limits the failures to these calls.
	only map[string]bool
}

func (c *swapWindowController) fail(call, op, path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed >= c.failures || c.only != nil && !c.only[call] {
		return nil
	}
	c.failed++
	u := "http://controller.discoverd" + path
	if c.failed%2 == 1 {
		return &url.Error{Op: op, URL: u, Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}
	}
	return &url.Error{Op: op, URL: u, Err: hh.JSONError{Code: hh.ObjectNotFoundErrorCode, Message: `service not found: "controller"`}}
}

func (c *swapWindowController) GetFormation(appID, releaseID string) (*ct.Formation, error) {
	if err := c.fail("GetFormation", "Get", "/apps/"+appID+"/formations/"+releaseID); err != nil {
		return nil, err
	}
	return c.fakeInfraController.GetFormation(appID, releaseID)
}

func (c *swapWindowController) AppReleaseList(appID string) ([]*ct.Release, error) {
	if err := c.fail("AppReleaseList", "Get", "/apps/"+appID+"/releases"); err != nil {
		return nil, err
	}
	return c.fakeInfraController.AppReleaseList(appID)
}

func (c *swapWindowController) SetAppRelease(appID, releaseID string) error {
	if err := c.fail("SetAppRelease", "Put", "/apps/"+appID+"/release"); err != nil {
		return err
	}
	return c.fakeInfraController.SetAppRelease(appID, releaseID)
}

type captureLog struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureLog) logger() log15.Logger {
	l := log15.New()
	l.SetHandler(log15.FuncHandler(func(r *log15.Record) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		line := r.Msg
		for i := 0; i+1 < len(r.Ctx); i += 2 {
			line += fmt.Sprintf(" %v=%v", r.Ctx[i], r.Ctx[i+1])
		}
		c.lines = append(c.lines, line)
		return nil
	}))
	return l
}

func (c *captureLog) has(s string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.lines {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func swapRollout(t *testing.T, failures int) (*swapWindowController, []*fakeInfraHost, *hostInfraRollout) {
	t.Helper()
	grace, delay := hostInfraSchedulerGrace, hostInfraFormationRetryDelay
	hostInfraSchedulerGrace, hostInfraFormationRetryDelay = 0, time.Millisecond
	t.Cleanup(func() { hostInfraSchedulerGrace, hostInfraFormationRetryDelay = grace, delay })

	fake := newFakeInfraController(t, "layer-old")
	h1 := &fakeInfraHost{id: "n1", jobs: map[string]host.ActiveJob{"a": infraJob("a", "rel-old", "layer-old")}}
	h2 := &fakeInfraHost{id: "n2", jobs: map[string]host.ActiveJob{"b": infraJob("b", "rel-old", "layer-old")}}
	fake.hosts = []*fakeInfraHost{h1, h2}
	c := &swapWindowController{fakeInfraController: fake}
	r, err := prepareHostInfraRollout(c, "flannel", newInfraImage(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Prepared before the swap; the controller is unreachable from here on
	// for the given number of reads.
	c.failures = failures
	return c, []*fakeInfraHost{h1, h2}, r
}

func TestHostInfraRolloutWaitsForControllerAfterDiscoverdSwap(t *testing.T) {
	for name, only := range map[string]map[string]bool{
		"every read": nil,
		// The live failures: SetAppRelease went through, then the GET of the
		// new release's formation failed.
		"formation GET": {"GetFormation": true},
	} {
		t.Run(name, func(t *testing.T) { testRolloutWaitsForController(t, only) })
	}
}

func testRolloutWaitsForController(t *testing.T, only map[string]bool) {
	c, hosts, r := swapRollout(t, 6)
	c.only = only
	logs := &captureLog{}
	err := replaceHostInfraJobs([]hostInfraHost{hosts[0], hosts[1]}, map[string]*ct.Artifact{"flannel": newInfraImage(t)},
		map[string]*hostInfraRollout{"flannel": r}, false, logs.logger())
	if err != nil {
		t.Fatalf("rollout failed on a short controller gap after the swap: %v", err)
	}
	if c.failed != 6 {
		t.Fatalf("failed reads %d, want all 6 used", c.failed)
	}
	newID := c.created[0].ID
	if c.release.ID != newID {
		t.Fatalf("app release=%s, want %s", c.release.ID, newID)
	}
	if n := c.formations["rel-old"].Processes["app"]; n != 0 {
		t.Fatalf("old formation app=%d, want 0", n)
	}
	for _, h := range hosts {
		assertHostRunsRelease(t, h, newID)
	}
	if !logs.has("waiting for the controller after the host-infra swap") || !logs.has("connection refused") || !logs.has("service not found") {
		t.Fatalf("retries not logged: %q", logs.lines)
	}
	if !logs.has("controller answered after the host-infra swap") {
		t.Fatalf("recovery not logged: %q", logs.lines)
	}
}

func TestHostInfraRolloutFailsClearlyWhenControllerStaysUnreachable(t *testing.T) {
	budget := hostInfraFormationBudget
	hostInfraFormationBudget = 50 * time.Millisecond
	t.Cleanup(func() { hostInfraFormationBudget = budget })

	_, hosts, r := swapRollout(t, 1<<30)
	start := time.Now()
	err := replaceHostInfraJobs([]hostInfraHost{hosts[0], hosts[1]}, map[string]*ct.Artifact{"flannel": newInfraImage(t)},
		map[string]*hostInfraRollout{"flannel": r}, false, nil)
	if err == nil {
		t.Fatal("rollout succeeded with no controller")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("budget not honoured: %s", time.Since(start))
	}
	msg := err.Error()
	for _, want := range []string{"controller did not answer", "50ms", "attempts", "last error", "controller.discoverd"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not say %q", msg, want)
		}
	}
}

// A real 404 from the controller (the release has no formation) is an answer,
// not an outage: it must not wait out the budget.
func TestHostInfraControllerReadDoesNotRetryNotFound(t *testing.T) {
	calls := 0
	start := time.Now()
	err := hostInfraRead("get formation", nil, func() error {
		calls++
		return controller.ErrNotFound
	})
	if !errors.Is(err, controller.ErrNotFound) || calls != 1 || time.Since(start) > time.Second {
		t.Fatalf("err=%v calls=%d took %s", err, calls, time.Since(start))
	}
}
