package discoverd

import (
	"testing"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
)

func watchInst(id, addr string) *discoverd.Instance {
	return &discoverd.Instance{
		ID:    id,
		Addr:  addr,
		Index: 1,
		Meta:  map[string]string{"POSTGRES_ID": id},
	}
}

func TestReconcileWatchPeersRestoresLiveSyncMissingFromLocalView(t *testing.T) {
	primary := watchInst("primary", "100.100.15.20:5432")
	sync := watchInst("sync", "100.100.31.34:5432")
	async := watchInst("async", "100.100.70.127:5432")
	// The view left behind by a partial watch: the unassigned peer is
	// visible and the live sync is not, which is what made both sides
	// try to promote that peer.
	have := instSlice{primary, async}

	next, changed := reconcileWatchPeers(have, []*discoverd.Instance{sync, primary, async})
	if !changed {
		t.Fatal("expected the live instance list to replace the stale view")
	}
	for _, id := range []string{"primary", "sync", "async"} {
		if !containsPeer(next, id) {
			t.Fatalf("%s missing after resync: %#v", id, next)
		}
	}
}

func TestReconcileWatchPeersIgnoresEmptyList(t *testing.T) {
	have := instSlice{watchInst("primary", "100.100.15.20:5432"), watchInst("sync", "100.100.31.34:5432")}
	next, changed := reconcileWatchPeers(have, nil)
	if changed || !next.Equal(have) {
		t.Fatal("an empty discoverd read must not wipe membership")
	}
}

func TestWatchPeersEqualSeesAddressChange(t *testing.T) {
	old := instSlice{watchInst("sync", "100.100.31.34:5432")}
	moved := instSlice{watchInst("sync", "100.100.31.99:5432")}
	if old.Equal(moved) {
		t.Fatal("a new address must be published so replication is not left on the old IP")
	}
}

func containsPeer(peers instSlice, id string) bool {
	for _, peer := range peers {
		if peer.ID == id {
			return true
		}
	}
	return false
}
