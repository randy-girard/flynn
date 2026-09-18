package state_test

import (
	"testing"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

func inst(addr, id string) *discoverd.Instance {
	return &discoverd.Instance{
		Addr:  addr,
		Proto: "tcp",
		Meta:  map[string]string{"POSTGRES_ID": id},
	}
}

// TestStateEqualDetectsAsyncAddressMove is the 2026-09-09 mariadb hang:
// replacement job keeps MARIADB_ID / POSTGRES_ID but gets a new flannel IP.
// Cluster state must compare unequal so the primary rewrites Async with the
// live discoverd instance instead of targeting the dead address.
func TestStateEqualDetectsAsyncAddressMove(t *testing.T) {
	primary := inst("10.0.0.1:5432", "p")
	sync := inst("10.0.0.2:5432", "s")
	oldAsync := inst("10.0.0.3:5432", "a")
	movedAsync := inst("10.0.0.4:5432", "a")

	before := &state.State{Generation: 1, Primary: primary, Sync: sync, Async: []*discoverd.Instance{oldAsync}}
	after := &state.State{Generation: 1, Primary: primary, Sync: sync, Async: []*discoverd.Instance{movedAsync}}
	if before.Equal(after) {
		t.Fatal("same appliance identity at a new address must not compare equal")
	}

	same := &state.State{Generation: 1, Primary: primary, Sync: sync, Async: []*discoverd.Instance{oldAsync}}
	if !before.Equal(same) {
		t.Fatal("identical cluster state must compare equal")
	}
}

func TestConfigEqualSameAddrDifferentIdentity(t *testing.T) {
	old := inst("10.0.0.2:5432", "old-sync")
	replaced := inst("10.0.0.2:5432", "new-sync")
	cfgOld := &state.Config{Role: state.RolePrimary, Downstream: old}
	cfgNew := &state.Config{Role: state.RolePrimary, Downstream: replaced}
	if cfgOld.Equal(cfgNew) {
		t.Fatal("same flannel IP with a new POSTGRES_ID must not short-circuit reconfigure")
	}
	if !cfgOld.IsNewDownstream(cfgNew) {
		t.Fatal("replacement at the same address is a new downstream")
	}
}
