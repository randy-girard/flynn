package state

import (
	"testing"

	"github.com/inconshreveable/log15"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/sirenia/xlog"
)

func TestHoldTakeoverForUnknownWAL(t *testing.T) {
	if !holdTakeoverForUnknownWAL("") {
		t.Fatal("empty init_wal must hold takeover")
	}
	if !holdTakeoverForUnknownWAL("  ") {
		t.Fatal("blank init_wal must hold takeover")
	}
	if holdTakeoverForUnknownWAL(xlog.Position("11/3B000000")) {
		t.Fatal("a real wal position must allow takeover")
	}
	if holdTakeoverForUnknownWAL(xlog.Position("0/00000000")) {
		t.Fatal("the zero wal position is a known minimum")
	}
}

func TestRepairEmptyInitWALWhenSyncIsVisible(t *testing.T) {
	db := &reconfigureStartsDB{running: true}
	disc := &fakeDiscoverd{}
	self := memInst("10.0.0.1:5432", "p")
	sync := memInst("10.0.0.2:5432", "s")
	peer := NewPeer(self, "p", "POSTGRES_ID", false, disc, db, log15.New())
	peer.setPeers([]*discoverd.Instance{self, sync})
	peer.setState(&State{Generation: 1, Primary: self, Sync: sync})

	if !peer.repairEmptyInitWAL(log15.New()) {
		t.Fatal("expected init_wal to be recorded once the sync is visible")
	}
	if peer.Info().State.InitWAL != "0-1-1" {
		t.Fatalf("init_wal = %q, want the running primary position", peer.Info().State.InitWAL)
	}
	if disc.state == nil || disc.state.State.InitWAL != "0-1-1" {
		t.Fatal("repaired wal was not written to discoverd")
	}
}

func TestRepublishClusterStateWritesFreshMeta(t *testing.T) {
	disc := &fakeDiscoverd{}
	self := memInst("10.0.0.1:5432", "p")
	peer := NewPeer(self, "p", "POSTGRES_ID", true, disc, &reconfigureStartsDB{running: true}, log15.New())
	peer.setState(&State{Generation: 4, Singleton: true, Primary: self, InitWAL: "11/1"})
	peer.stateIndex = 99
	if !peer.republishClusterState(log15.New()) {
		t.Fatal("the recorded primary should republish")
	}
	if disc.state == nil || disc.state.Index != 0 || disc.state.State.Generation != 4 {
		t.Fatalf("republish wrote %#v", disc.state)
	}
}

func TestRepublishClusterStateSkipsFollower(t *testing.T) {
	disc := &fakeDiscoverd{}
	self := memInst("10.0.0.2:5432", "s")
	primary := memInst("10.0.0.1:5432", "p")
	peer := NewPeer(self, "s", "POSTGRES_ID", false, disc, &reconfigureStartsDB{running: true}, log15.New())
	peer.setState(&State{Generation: 4, Primary: primary, Sync: self})
	if peer.republishClusterState(log15.New()) {
		t.Fatal("a follower must not republish")
	}
	if disc.state != nil {
		t.Fatal("follower wrote cluster state")
	}
}

func TestRepairEmptyInitWALSkipsWhenSyncIsMissing(t *testing.T) {
	db := &reconfigureStartsDB{running: true}
	disc := &fakeDiscoverd{}
	self := memInst("10.0.0.1:5432", "p")
	sync := memInst("10.0.0.2:5432", "s")
	peer := NewPeer(self, "p", "POSTGRES_ID", false, disc, db, log15.New())
	peer.setPeers([]*discoverd.Instance{self})
	peer.setState(&State{Generation: 1, Primary: self, Sync: sync})

	if peer.repairEmptyInitWAL(log15.New()) {
		t.Fatal("must not record wal while the sync is missing from the peer view")
	}
	if disc.state != nil {
		t.Fatal("missing sync must not write cluster state")
	}
	if peer.Info().State.InitWAL != "" {
		t.Fatalf("init_wal changed to %q", peer.Info().State.InitWAL)
	}
}
