package state

import (
	"testing"

	"github.com/inconshreveable/log15"
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

// recordingDiscoverd keeps every cluster state written, in order.
type recordingDiscoverd struct {
	states []*DiscoverdState
}

func (d *recordingDiscoverd) SetState(s *DiscoverdState) error {
	c := *s
	c.State = s.State.Clone()
	d.states = append(d.states, &c)
	return nil
}

func (d *recordingDiscoverd) Events() <-chan *DiscoverdEvent {
	return make(chan *DiscoverdEvent)
}

func memInstAt(addr, id string, index uint64) *discoverd.Instance {
	inst := memInst(addr, id)
	inst.Index = index
	return inst
}

// CORE-38: a replaced job that keeps its volume keeps its appliance ID and
// registers at a new address while discoverd still lists the old instance.
// Both instances are in the peer list. The unassigned-peer loop must add
// that peer once, using the newest registration, or the second copy becomes
// the first copy's downstream.
func TestNextAsyncMembershipCollapsesTwoInstancesOfOnePeer(t *testing.T) {
	primary := memInstAt("10.0.0.1:5432", "p", 1)
	sync := memInstAt("10.0.0.2:5432", "s", 2)
	oldA := memInstAt("10.0.0.3:5432", "a", 3)
	newA := memInstAt("10.0.0.9:5432", "a", 7)
	st := &State{Primary: primary, Sync: sync}
	peers := []*discoverd.Instance{primary, sync, oldA, newA}

	got, _, changes := nextAsyncMembership("POSTGRES_ID", st, peers)
	if !changes {
		t.Fatal("expected membership change when a new peer joins")
	}
	if len(got) != 1 {
		t.Fatalf("one peer must be one async, got %d: %#v", len(got), got)
	}
	if got[0].Addr != newA.Addr {
		t.Fatalf("async must use the newest instance %s, got %s", newA.Addr, got[0].Addr)
	}
}

// A recorded async whose job was replaced must move to the new address even
// when the old instance is still listed ahead of it.
func TestNextAsyncMembershipRecordedAsyncPrefersNewestInstance(t *testing.T) {
	primary := memInstAt("10.0.0.1:5432", "p", 1)
	sync := memInstAt("10.0.0.2:5432", "s", 2)
	oldA := memInstAt("10.0.0.3:5432", "a", 3)
	newA := memInstAt("10.0.0.9:5432", "a", 7)
	st := &State{Primary: primary, Sync: sync, Async: []*discoverd.Instance{oldA}}
	peers := []*discoverd.Instance{primary, sync, oldA, newA}

	got, _, changes := nextAsyncMembership("POSTGRES_ID", st, peers)
	if !changes {
		t.Fatal("expected a change: the async moved to a new address")
	}
	if len(got) != 1 || got[0].Addr != newA.Addr {
		t.Fatalf("recorded async must be the newest instance only, got %#v", got)
	}
}

// startTransitionToNormalMode builds Async from every peer that is not self
// or the new sync. Two instances of one peer must become one async.
func TestTransitionToNormalModeDedupesPeers(t *testing.T) {
	db := &reconfigureStartsDB{running: true}
	dsd := &recordingDiscoverd{}
	self := memInstAt("10.0.0.1:5432", "p", 1)
	oldB := memInstAt("10.0.0.2:5432", "b", 2)
	newB := memInstAt("10.0.0.5:5432", "b", 5)
	c := memInstAt("10.0.0.3:5432", "c", 6)

	peer := NewPeer(self, "p", "POSTGRES_ID", false, dsd, db, log15.New())
	online := true
	peer.online = &online
	peer.setState(&State{Generation: 1, Primary: self, Singleton: true, InitWAL: "0-1-1"})
	peer.generation = 1
	peer.setRole(RolePrimary)
	// Set the raw list, bypassing setPeers, so the function's own handling is
	// what is tested.
	info := *peer.Info()
	info.Peers = []*discoverd.Instance{self, oldB, newB, c}
	peer.setInfo(info)

	peer.startTransitionToNormalMode()

	// The first write is the transition itself; later writes come from the
	// evaluation that follows and could hide a duplicate.
	if len(dsd.states) == 0 || dsd.states[0].State == nil {
		t.Fatal("expected a new cluster state")
	}
	st := dsd.states[0].State
	seen := map[string]int{}
	for _, inst := range append([]*discoverd.Instance{st.Primary, st.Sync}, st.Async...) {
		seen[peerApplianceID("POSTGRES_ID", inst)]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("peer %s listed %d times in primary/sync/async: %+v", id, n, st)
		}
	}
	for _, inst := range append([]*discoverd.Instance{st.Sync}, st.Async...) {
		if peerApplianceID("POSTGRES_ID", inst) == "b" && inst.Addr != newB.Addr {
			t.Fatalf("peer b must use its newest instance %s, got %s", newB.Addr, inst.Addr)
		}
	}
}

// The peer list from discoverd is keyed by instance ID, so a replaced job
// that kept its volume shows up twice until the old registration expires.
// Every consumer of Peers (initial setup, takeover, presentPeer, membership)
// must see that peer once, at its newest address.
func TestDiscoverdPeersCollapseReplacedInstance(t *testing.T) {
	self := memInstAt("10.0.0.1:5432", "p", 1)
	oldB := memInstAt("10.0.0.2:5432", "b", 2)
	c := memInstAt("10.0.0.3:5432", "c", 3)
	newB := memInstAt("10.0.0.5:5432", "b", 5)
	peer := NewPeer(self, "p", "POSTGRES_ID", false, &fakeDiscoverd{}, &reconfigureStartsDB{}, log15.New())
	peer.handleDiscoverdInit(&DiscoverdEvent{Kind: DiscoverdEventInit, Peers: []*discoverd.Instance{self, oldB, c, newB}, State: &DiscoverdState{}})

	got := peer.Info().Peers
	if len(got) != 3 {
		t.Fatalf("expected 3 peers after collapsing b, got %d: %#v", len(got), got)
	}
	want := []string{self.Addr, c.Addr, newB.Addr}
	for i, inst := range got {
		if inst.Addr != want[i] {
			t.Fatalf("peer %d = %s, want %s (order kept, newest b only)", i, inst.Addr, want[i])
		}
	}
	if cur := peer.presentPeer(oldB); cur == nil || cur.Addr != newB.Addr {
		t.Fatalf("presentPeer(old b) must return the new instance, got %#v", cur)
	}
}

// Two registrations of this peer itself must not count as a second peer for
// initial cluster setup (it would make itself its own sync).
func TestDiscoverdPeersCollapseSelf(t *testing.T) {
	oldSelf := memInstAt("10.0.0.1:5432", "p", 1)
	self := memInstAt("10.0.0.7:5432", "p", 4)
	peer := NewPeer(self, "p", "POSTGRES_ID", false, &fakeDiscoverd{}, &reconfigureStartsDB{}, log15.New())
	peer.handleDiscoverdInit(&DiscoverdEvent{Kind: DiscoverdEventInit, Peers: []*discoverd.Instance{oldSelf, self}, State: &DiscoverdState{}})
	if got := peer.Info().Peers; len(got) != 1 || got[0].Addr != self.Addr {
		t.Fatalf("expected only the newest self instance, got %#v", got)
	}
}
