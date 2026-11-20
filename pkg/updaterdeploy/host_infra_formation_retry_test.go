package updaterdeploy

import (
	"errors"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

// flakyFormationController fails PutFormation the first fails times.
type flakyFormationController struct {
	HostInfraController
	fails int
	calls int
}

func (c *flakyFormationController) PutFormation(*ct.Formation) error {
	c.calls++
	if c.calls <= c.fails {
		return errors.New("unknown_error: ERROR: cannot execute SELECT in a read-only transaction (SQLSTATE 25006)")
	}
	return nil
}

// TestHostInfraPutFormationOutlastsPostgresFailover: replacing discoverd on
// every host makes the controller database fail over. On the dev cluster
// PutFormation returned SQLSTATE 25006 (read-only transaction) for about a
// minute, longer than three tries, and the update aborted after discoverd and
// flannel were already replaced. The formation write must keep retrying for a
// time budget, not a fixed three tries.
func TestHostInfraPutFormationOutlastsPostgresFailover(t *testing.T) {
	delay, budget := hostInfraFormationRetryDelay, hostInfraFormationBudget
	hostInfraFormationRetryDelay, hostInfraFormationBudget = time.Millisecond, 5*time.Second
	t.Cleanup(func() { hostInfraFormationRetryDelay, hostInfraFormationBudget = delay, budget })

	c := &flakyFormationController{fails: 8}
	r := &hostInfraRollout{client: c, appID: "discoverd"}
	if err := r.putFormation("rel-1", map[string]int{"app": 1}, nil, nil); err != nil {
		t.Fatalf("put after failover: %v", err)
	}
	if c.calls != 9 {
		t.Fatalf("calls %d", c.calls)
	}

	// The budget still ends a write that never succeeds.
	hostInfraFormationBudget = 20 * time.Millisecond
	stuck := &flakyFormationController{fails: 1 << 30}
	r = &hostInfraRollout{client: stuck, appID: "discoverd"}
	start := time.Now()
	err := r.putFormation("rel-1", map[string]int{"app": 1}, nil, nil)
	if err == nil || stuck.calls < 2 {
		t.Fatalf("stuck put: err %v calls %d", err, stuck.calls)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("budget not honoured: %s", time.Since(start))
	}
}
