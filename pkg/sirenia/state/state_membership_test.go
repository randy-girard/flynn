package state

import (
	"testing"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
)

func memInst(addr, id string) *discoverd.Instance {
	return &discoverd.Instance{
		Addr:  addr,
		Proto: "tcp",
		ID:    addr,
		Meta:  map[string]string{"POSTGRES_ID": id},
	}
}

func TestNextAsyncMembershipAppendsNewcomerAsTail(t *testing.T) {
	primary := memInst("10.0.0.1:5432", "p")
	sync := memInst("10.0.0.2:5432", "s")
	async := memInst("10.0.0.3:5432", "a")
	newPeer := memInst("10.0.0.4:5432", "n")
	st := &State{Primary: primary, Sync: sync, Async: []*discoverd.Instance{async}}
	peers := []*discoverd.Instance{primary, sync, async, newPeer}

	got, _, changes := nextAsyncMembership("POSTGRES_ID", st, peers)
	if !changes {
		t.Fatal("expected membership change when a new peer joins")
	}
	if len(got) != 2 || peerApplianceID("POSTGRES_ID", got[0]) != "a" || peerApplianceID("POSTGRES_ID", got[1]) != "n" {
		t.Fatalf("new peer must be the tail async, got %#v", got)
	}
}

func TestNextAsyncMembershipKeepsMissingAsyncWhenNewPeerJoins(t *testing.T) {
	primary := memInst("10.0.0.1:5432", "p")
	sync := memInst("10.0.0.2:5432", "s")
	async := memInst("10.0.0.3:5432", "a")
	newPeer := memInst("10.0.0.4:5432", "n")
	st := &State{Primary: primary, Sync: sync, Async: []*discoverd.Instance{async}}
	// First async is flapping out of discoverd at the same moment the
	// replacement registers. Dropping it would put the newcomer at index 0
	// and make the existing async (when it returns) re-point its upstream
	// at a peer that has not finished its base backup.
	peers := []*discoverd.Instance{primary, sync, newPeer}

	got, _, changes := nextAsyncMembership("POSTGRES_ID", st, peers)
	if !changes {
		t.Fatal("expected membership change when a new peer joins")
	}
	if len(got) != 2 || peerApplianceID("POSTGRES_ID", got[0]) != "a" || peerApplianceID("POSTGRES_ID", got[1]) != "n" {
		t.Fatalf("missing async slot must be kept so the newcomer stays tail, got %#v", got)
	}
}

func TestNextAsyncMembershipDropsMissingAsyncWithoutNewcomer(t *testing.T) {
	primary := memInst("10.0.0.1:5432", "p")
	sync := memInst("10.0.0.2:5432", "s")
	async := memInst("10.0.0.3:5432", "a")
	st := &State{Primary: primary, Sync: sync, Async: []*discoverd.Instance{async}}
	peers := []*discoverd.Instance{primary, sync}

	got, _, changes := nextAsyncMembership("POSTGRES_ID", st, peers)
	if !changes {
		t.Fatal("expected membership change when the only async disappears")
	}
	if len(got) != 0 {
		t.Fatalf("missing async should be dropped when no newcomer is joining, got %#v", got)
	}
}

// A peer listed twice in State.Async makes the second copy its own
// downstream (lookupDownstream returns Async[i+1]), so it waits forever for
// itself in pg_stat_replication and deploys refuse the cluster for "too few
// asyncs" (2 asyncs for 3 processes). Seen on the dev cluster after a chaos
// run on 2026-11-20. The primary must collapse duplicates.
func TestNextAsyncMembershipDropsDuplicateAsync(t *testing.T) {
	primary := memInst("10.0.0.1:5432", "p")
	sync := memInst("10.0.0.2:5432", "s")
	async := memInst("10.0.0.3:5432", "a")
	stale := memInst("10.0.0.9:5432", "a")
	st := &State{Primary: primary, Sync: sync, Async: []*discoverd.Instance{async, stale}}
	peers := []*discoverd.Instance{primary, sync, async}

	got, _, changes := nextAsyncMembership("POSTGRES_ID", st, peers)
	if !changes {
		t.Fatal("expected membership change when an async is listed twice")
	}
	if len(got) != 1 || peerApplianceID("POSTGRES_ID", got[0]) != "a" || got[0].Addr != async.Addr {
		t.Fatalf("duplicate async must collapse to the present instance, got %#v", got)
	}
}

func TestNextAsyncMembershipDropsAsyncThatIsPrimaryOrSync(t *testing.T) {
	primary := memInst("10.0.0.1:5432", "p")
	sync := memInst("10.0.0.2:5432", "s")
	async := memInst("10.0.0.3:5432", "a")
	st := &State{Primary: primary, Sync: sync, Async: []*discoverd.Instance{sync, async, primary}}
	peers := []*discoverd.Instance{primary, sync, async}

	got, _, changes := nextAsyncMembership("POSTGRES_ID", st, peers)
	if !changes {
		t.Fatal("expected membership change when the sync or primary is also an async")
	}
	if len(got) != 1 || peerApplianceID("POSTGRES_ID", got[0]) != "a" {
		t.Fatalf("only the real async may stay, got %#v", got)
	}
}

func TestNextAsyncMembershipDeposedAlreadyAsyncIsNotDuplicated(t *testing.T) {
	primary := memInst("10.0.0.1:5432", "p")
	sync := memInst("10.0.0.2:5432", "s")
	async := memInst("10.0.0.3:5432", "a")
	st := &State{Primary: primary, Sync: sync, Async: []*discoverd.Instance{async}, Deposed: []*discoverd.Instance{async}}
	peers := []*discoverd.Instance{primary, sync, async}

	got, deposed, changes := nextAsyncMembership("POSTGRES_ID", st, peers)
	if !changes {
		t.Fatal("expected membership change when a deposed peer is already an async")
	}
	if len(got) != 1 || peerApplianceID("POSTGRES_ID", got[0]) != "a" {
		t.Fatalf("deposed peer already in async must not be appended again, got %#v", got)
	}
	if len(deposed) != 0 {
		t.Fatalf("present deposed peer must leave deposed, got %#v", deposed)
	}
}
