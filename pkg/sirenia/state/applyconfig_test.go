package state

import (
	"errors"
	"testing"

	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/sirenia/xlog"
	"github.com/inconshreveable/log15"
)

// reconfigureStartsDB simulates sirenia appliances where Reconfigure starts the
// database process directly (e.g. assumeStandby) before applyConfig calls Start.
type reconfigureStartsDB struct {
	running bool
}

func (d *reconfigureStartsDB) XLogPosition() (xlog.Position, error) {
	if !d.running {
		return "", errors.New("database is offline")
	}
	return "0-1-1", nil
}

type testXLog struct{}

func (testXLog) Zero() xlog.Position { return "" }
func (testXLog) Compare(a, b xlog.Position) (int, error) {
	if a == b {
		return 0, nil
	}
	if a == "" {
		return -1, nil
	}
	if b == "" {
		return 1, nil
	}
	return 0, nil
}

func (d *reconfigureStartsDB) XLog() xlog.XLog {
	return testXLog{}
}

func (d *reconfigureStartsDB) Reconfigure(config *Config) error {
	if config.Role != RoleNone {
		d.running = true
	}
	return nil
}

func (d *reconfigureStartsDB) Start() error {
	if d.running {
		return errors.New("process already running")
	}
	d.running = true
	return nil
}

func (d *reconfigureStartsDB) Stop() error {
	d.running = false
	return nil
}

func (d *reconfigureStartsDB) Running() bool {
	return d.running
}

func (d *reconfigureStartsDB) Ready() <-chan DatabaseEvent {
	ch := make(chan DatabaseEvent, 1)
	ch <- DatabaseEvent{}
	return ch
}

// fakeDiscoverd records the last cluster state written during a takeover so
// tests can drive the takeover path without a real discoverd.
type fakeDiscoverd struct {
	state *DiscoverdState
}

func (d *fakeDiscoverd) SetState(s *DiscoverdState) error {
	d.state = s
	return nil
}

func (d *fakeDiscoverd) Events() <-chan *DiscoverdEvent {
	return make(chan *DiscoverdEvent)
}

// TestSyncTakesOverWhenPrimaryGoneAfterDatabaseStarts verifies that a sync peer
// whose upstream primary has disappeared takes over as primary once its local
// database is running. Appliances (e.g. mariadb assumeStandby) start the
// database from an already-initialized data directory even when the upstream is
// unreachable; this is what allows the sync's startTakeover to proceed instead
// of deadlocking on ErrDatabaseOffline.
func TestSyncTakesOverWhenPrimaryGoneAfterDatabaseStarts(t *testing.T) {
	db := &reconfigureStartsDB{}
	dsd := &fakeDiscoverd{}

	self := &discoverd.Instance{Addr: ":3306", Meta: map[string]string{"MARIADB_ID": "node1"}}
	primary := &discoverd.Instance{Addr: ":3307", Meta: map[string]string{"MARIADB_ID": "primary"}}
	async := &discoverd.Instance{Addr: ":3308", Meta: map[string]string{"MARIADB_ID": "async"}}

	peer := NewPeer(self, "node1", "MARIADB_ID", false, dsd, db, log15.New())
	online := false
	peer.online = &online

	// The primary is absent from the present-peer list (it has died); only
	// ourselves (the sync) and the async remain.
	peer.setPeers([]*discoverd.Instance{self, async})
	peer.setState(&State{
		Generation: 1,
		Primary:    primary,
		Sync:       self,
		Async:      []*discoverd.Instance{async},
		InitWAL:    "0-1-1",
	})
	peer.generation = 1
	peer.setRole(RoleSync)
	peer.upstream = primary
	peer.downstream = async

	// Bring the local database online, as the appliance's Reconfigure does for
	// an initialized data directory even with the upstream unreachable.
	if err := peer.applyConfig(); err != nil {
		t.Fatalf("applyConfig: %v", err)
	}
	if !db.Running() {
		t.Fatal("expected database running after applyConfig")
	}

	// With the database online and the primary gone, evaluating cluster state
	// must trigger a takeover that promotes this peer to primary.
	peer.evalClusterState()

	if peer.Info().Role != RolePrimary {
		t.Fatalf("expected role primary after takeover, got %v", peer.Info().Role)
	}
	if dsd.state == nil || dsd.state.State == nil {
		t.Fatal("expected new cluster state to be written during takeover")
	}
	if dsd.state.State.Generation != 2 {
		t.Fatalf("expected generation 2 after takeover, got %d", dsd.state.State.Generation)
	}
	if dsd.state.State.Primary == nil || dsd.state.State.Primary.Meta["MARIADB_ID"] != "node1" {
		t.Fatalf("expected self to be recorded as new primary, got %+v", dsd.state.State.Primary)
	}
}

func TestApplyConfigMarksOnlineWhenReconfigureStartsDatabase(t *testing.T) {
	db := &reconfigureStartsDB{}
	inst := &discoverd.Instance{
		Addr: ":3306",
		Meta: map[string]string{"MARIADB_ID": "node1"},
	}
	peer := NewPeer(inst, "node1", "MARIADB_ID", false, nil, db, log15.New())
	online := false
	peer.online = &online

	primary := &discoverd.Instance{Addr: ":3306", Meta: map[string]string{"MARIADB_ID": "primary"}}
	sync := inst
	peer.setPeers([]*discoverd.Instance{primary, sync})
	peer.setState(&State{
		Generation: 1,
		Primary:    primary,
		Sync:       sync,
		InitWAL:    "0-1-1",
	})
	peer.setRole(RoleSync)
	peer.upstream = primary
	peer.downstream = nil

	if err := peer.applyConfig(); err != nil {
		t.Fatalf("applyConfig: %v", err)
	}
	if !*peer.online {
		t.Fatal("expected peer online after applyConfig when Reconfigure started database")
	}
	if !db.Running() {
		t.Fatal("expected database still running")
	}
}
