package state

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/sirenia/xlog"
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

// A sync whose database cannot start must not retry takeover in a hot loop.
func TestTakeoverDatabaseOfflineRetriesAfterDelay(t *testing.T) {
	defer func(d time.Duration) { databaseOfflineRetryDelay = d }(databaseOfflineRetryDelay)
	databaseOfflineRetryDelay = 200 * time.Millisecond

	db := &reconfigureStartsDB{}
	self := &discoverd.Instance{Addr: ":5432", Meta: map[string]string{"POSTGRES_ID": "sync"}}
	primary := &discoverd.Instance{Addr: ":5433", Meta: map[string]string{"POSTGRES_ID": "primary"}}
	async := &discoverd.Instance{Addr: ":5434", Meta: map[string]string{"POSTGRES_ID": "async"}}
	peer := NewPeer(self, "sync", "POSTGRES_ID", false, &fakeDiscoverd{}, db, log15.New())
	peer.setPeers([]*discoverd.Instance{self, async})
	peer.setState(&State{Generation: 1, Primary: primary, Sync: self, Async: []*discoverd.Instance{async}, InitWAL: "0-1-1"})
	peer.generation = 1
	peer.setRole(RoleSync)

	if !peer.startTakeover("primary gone", "0-1-1") {
		t.Fatal("expected takeover attempt")
	}
	select {
	case <-peer.evalStateCh:
		t.Fatal("offline database must not re-trigger evaluation immediately")
	default:
	}
	select {
	case <-peer.evalStateCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a delayed re-evaluation")
	}
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

func TestPostgresSyncTakesOverWhenPrimaryGoneAfterDatabaseStarts(t *testing.T) {
	db := &reconfigureStartsDB{}
	dsd := &fakeDiscoverd{}

	self := &discoverd.Instance{Addr: ":5432", Meta: map[string]string{"POSTGRES_ID": "node1"}}
	primary := &discoverd.Instance{Addr: ":5433", Meta: map[string]string{"POSTGRES_ID": "primary"}}
	async := &discoverd.Instance{Addr: ":5434", Meta: map[string]string{"POSTGRES_ID": "async"}}

	peer := NewPeer(self, "node1", "POSTGRES_ID", false, dsd, db, log15.New())
	online := false
	peer.online = &online

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

	if err := peer.applyConfig(); err != nil {
		t.Fatalf("applyConfig: %v", err)
	}
	if !db.Running() {
		t.Fatal("expected database running after applyConfig")
	}

	peer.evalClusterState()

	if peer.Info().Role != RolePrimary {
		t.Fatalf("expected role primary after takeover, got %v", peer.Info().Role)
	}
	if dsd.state == nil || dsd.state.State == nil || dsd.state.State.Generation != 2 {
		t.Fatalf("expected generation 2 after takeover, got %+v", dsd.state)
	}
	if dsd.state.State.Primary == nil || dsd.state.State.Primary.Meta["POSTGRES_ID"] != "node1" {
		t.Fatalf("expected self to be recorded as new primary, got %+v", dsd.state.State.Primary)
	}
}

func TestPostgresSyncTakesOverWhenPrimaryGoneWithUnassignedPeer(t *testing.T) {
	db := &reconfigureStartsDB{}
	dsd := &fakeDiscoverd{}

	self := &discoverd.Instance{Addr: ":5432", Meta: map[string]string{"POSTGRES_ID": "node1"}}
	primary := &discoverd.Instance{Addr: ":5433", Meta: map[string]string{"POSTGRES_ID": "primary"}}
	deadAsync := &discoverd.Instance{Addr: ":5434", Meta: map[string]string{"POSTGRES_ID": "dead-async"}}
	deposed := &discoverd.Instance{Addr: ":5435", Meta: map[string]string{"POSTGRES_ID": "old-primary"}}
	unassigned := &discoverd.Instance{Addr: ":5436", Meta: map[string]string{"POSTGRES_ID": "node2"}}

	peer := NewPeer(self, "node1", "POSTGRES_ID", false, dsd, db, log15.New())
	online := false
	peer.online = &online

	// Crash-style reboot: listed async is gone, deposed primary is back at a
	// new address, and a live unassigned replica is present. Takeover must
	// use the unassigned peer as the new sync, not wait for a listed async.
	peer.setPeers([]*discoverd.Instance{self, deposed, unassigned})
	peer.setState(&State{
		Generation: 1,
		Primary:    primary,
		Sync:       self,
		Async:      []*discoverd.Instance{deadAsync},
		Deposed:    []*discoverd.Instance{deposed},
		InitWAL:    "0-1-1",
	})
	peer.generation = 1
	peer.setRole(RoleSync)
	peer.upstream = primary
	peer.downstream = deadAsync

	if err := peer.applyConfig(); err != nil {
		t.Fatalf("applyConfig: %v", err)
	}
	if !db.Running() {
		t.Fatal("expected database running after applyConfig")
	}

	peer.evalClusterState()

	if peer.Info().Role != RolePrimary {
		t.Fatalf("expected role primary after takeover, got %v", peer.Info().Role)
	}
	if dsd.state == nil || dsd.state.State == nil || dsd.state.State.Generation != 2 {
		t.Fatalf("expected generation 2 after takeover, got %+v", dsd.state)
	}
	if dsd.state.State.Primary == nil || dsd.state.State.Primary.Meta["POSTGRES_ID"] != "node1" {
		t.Fatalf("expected self to be recorded as new primary, got %+v", dsd.state.State.Primary)
	}
	if dsd.state.State.Sync == nil || dsd.state.State.Sync.Meta["POSTGRES_ID"] != "node2" {
		t.Fatalf("expected unassigned live peer as new sync, got %+v", dsd.state.State.Sync)
	}
}

func TestPostgresAsyncTakesOverWhenPrimaryAndSyncGone(t *testing.T) {
	db := &reconfigureStartsDB{}
	dsd := &fakeDiscoverd{}

	self := &discoverd.Instance{Addr: ":5432", Meta: map[string]string{"POSTGRES_ID": "async"}}
	primary := &discoverd.Instance{Addr: ":5433", Meta: map[string]string{"POSTGRES_ID": "primary"}}
	sync := &discoverd.Instance{Addr: ":5434", Meta: map[string]string{"POSTGRES_ID": "sync"}}
	unassigned := &discoverd.Instance{Addr: ":5435", Meta: map[string]string{"POSTGRES_ID": "node2"}}

	peer := NewPeer(self, "async", "POSTGRES_ID", false, dsd, db, log15.New())
	online := false
	peer.online = &online

	// Both primary and sync disappeared (same host reboot). The remaining
	// async must promote; an unassigned replica becomes the new sync.
	peer.setPeers([]*discoverd.Instance{self, unassigned})
	peer.setState(&State{
		Generation: 1,
		Primary:    primary,
		Sync:       sync,
		Async:      []*discoverd.Instance{self},
		InitWAL:    "0-1-1",
	})
	peer.generation = 1
	peer.setRole(RoleAsync)
	peer.upstream = sync
	peer.downstream = nil

	if err := peer.applyConfig(); err != nil {
		t.Fatalf("applyConfig: %v", err)
	}
	if !db.Running() {
		t.Fatal("expected database running after applyConfig")
	}

	peer.evalClusterState()

	if peer.Info().Role != RolePrimary {
		t.Fatalf("expected role primary after takeover, got %v", peer.Info().Role)
	}
	if dsd.state == nil || dsd.state.State == nil || dsd.state.State.Generation != 2 {
		t.Fatalf("expected generation 2 after takeover, got %+v", dsd.state)
	}
	if dsd.state.State.Primary == nil || dsd.state.State.Primary.Meta["POSTGRES_ID"] != "async" {
		t.Fatalf("expected self to be recorded as new primary, got %+v", dsd.state.State.Primary)
	}
	if dsd.state.State.Sync == nil || dsd.state.State.Sync.Meta["POSTGRES_ID"] != "node2" {
		t.Fatalf("expected unassigned live peer as new sync, got %+v", dsd.state.State.Sync)
	}
}

func TestPostgresAsyncTakesOverAsSolePrimaryWhenNoOtherPeers(t *testing.T) {
	db := &reconfigureStartsDB{}
	dsd := &fakeDiscoverd{}

	self := &discoverd.Instance{Addr: ":5432", Meta: map[string]string{"POSTGRES_ID": "async"}}
	primary := &discoverd.Instance{Addr: ":5433", Meta: map[string]string{"POSTGRES_ID": "primary"}}
	sync := &discoverd.Instance{Addr: ":5434", Meta: map[string]string{"POSTGRES_ID": "sync"}}

	peer := NewPeer(self, "async", "POSTGRES_ID", false, dsd, db, log15.New())
	online := false
	peer.online = &online

	peer.setPeers([]*discoverd.Instance{self})
	peer.setState(&State{
		Generation: 1,
		Primary:    primary,
		Sync:       sync,
		Async:      []*discoverd.Instance{self},
		InitWAL:    "0-1-1",
	})
	peer.generation = 1
	peer.setRole(RoleAsync)
	peer.upstream = sync

	if err := peer.applyConfig(); err != nil {
		t.Fatalf("applyConfig: %v", err)
	}

	peer.evalClusterState()

	if peer.Info().Role != RolePrimary {
		t.Fatalf("expected role primary after sole-replica takeover, got %v", peer.Info().Role)
	}
	if dsd.state == nil || dsd.state.State == nil || dsd.state.State.Generation != 2 {
		t.Fatalf("expected generation 2 after takeover, got %+v", dsd.state)
	}
	if dsd.state.State.Primary == nil || dsd.state.State.Primary.Meta["POSTGRES_ID"] != "async" {
		t.Fatalf("expected self as new primary, got %+v", dsd.state.State.Primary)
	}
	if dsd.state.State.Sync != nil {
		t.Fatalf("sole remaining replica must not appoint itself as sync, got %+v", dsd.state.State.Sync)
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

// upstreamWaitDB blocks in Reconfigure like a standby waiting for an
// unreachable upstream, until CancelUpstreamWait is called.
type upstreamWaitDB struct {
	reconfigureStartsDB
	waiting    chan struct{}
	canceled   chan struct{}
	cancelOnce sync.Once
}

func (d *upstreamWaitDB) Reconfigure(config *Config) error {
	close(d.waiting)
	select {
	case <-d.canceled:
		return errors.New("upstream wait canceled")
	case <-time.After(5 * time.Second):
		return errors.New("upstream is offline")
	}
}

func (d *upstreamWaitDB) CancelUpstreamWait() { d.cancelOnce.Do(func() { close(d.canceled) }) }

func newUpstreamWaitPeer(t *testing.T) (*Peer, *upstreamWaitDB, chan *DiscoverdEvent, *discoverd.Instance, *discoverd.Instance) {
	db := &upstreamWaitDB{waiting: make(chan struct{}), canceled: make(chan struct{})}
	self := &discoverd.Instance{Addr: ":5432", Meta: map[string]string{"POSTGRES_ID": "async"}}
	primary := &discoverd.Instance{Addr: ":5433", Meta: map[string]string{"POSTGRES_ID": "primary"}}
	sync := &discoverd.Instance{Addr: ":5434", Meta: map[string]string{"POSTGRES_ID": "sync"}}
	peer := NewPeer(self, "async", "POSTGRES_ID", false, &fakeDiscoverd{}, db, log15.New())
	events := make(chan *DiscoverdEvent, 4)
	peer.discoverdCh = events
	online := false
	peer.online = &online
	peer.setPeers([]*discoverd.Instance{primary, sync, self})
	peer.setState(&State{Generation: 1, Primary: primary, Sync: sync, Async: []*discoverd.Instance{self}})
	peer.stateIndex = 10
	peer.setRole(RoleAsync)
	peer.upstream = sync
	return peer, db, events, primary, self
}

func applyConfigAsync(peer *Peer) chan error {
	errCh := make(chan error, 1)
	go func() { errCh <- peer.applyConfig() }()
	return errCh
}

// A standby must stop waiting on an upstream that has left discoverd, and the
// discoverd events seen meanwhile must still be handled.
func TestApplyConfigCancelsUpstreamWaitWhenUpstreamLeaves(t *testing.T) {
	peer, db, events, primary, self := newUpstreamWaitPeer(t)
	errCh := applyConfigAsync(peer)
	<-db.waiting
	events <- &DiscoverdEvent{Kind: DiscoverdEventPeers, Peers: []*discoverd.Instance{primary, self}}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected canceled reconfigure to return an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("applyConfig still blocked on a departed upstream")
	}
	if n := len(peer.Info().Peers); n != 2 {
		t.Fatalf("buffered peers event not handled, peers=%d", n)
	}
}

// A newer state that makes the standby sync of the primary also cancels the
// wait on its old upstream.
func TestApplyConfigCancelsUpstreamWaitOnNewUpstream(t *testing.T) {
	peer, db, events, primary, self := newUpstreamWaitPeer(t)
	errCh := applyConfigAsync(peer)
	<-db.waiting
	events <- &DiscoverdEvent{Kind: DiscoverdEventState, State: &DiscoverdState{Index: 11, State: &State{Generation: 2, Primary: primary, Sync: self}}}
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("applyConfig still blocked on a replaced upstream")
	}
	if peer.stateIndex != 11 {
		t.Fatalf("buffered state event not handled, index=%d", peer.stateIndex)
	}
}

// Events that keep the same upstream must not cancel the wait.
func TestApplyConfigKeepsUpstreamWaitWhenUpstreamUnchanged(t *testing.T) {
	peer, db, events, _, _ := newUpstreamWaitPeer(t)
	errCh := applyConfigAsync(peer)
	<-db.waiting
	events <- &DiscoverdEvent{Kind: DiscoverdEventPeers, Peers: peer.Info().Peers}
	select {
	case <-db.canceled:
		t.Fatal("upstream wait canceled though upstream is still present")
	case <-time.After(300 * time.Millisecond):
	}
	db.CancelUpstreamWait()
	<-errCh
}

// An upstream already gone from discoverd when apply starts is not waited on.
func TestApplyConfigCancelsUpstreamWaitWhenUpstreamAlreadyGone(t *testing.T) {
	peer, db, _, primary, self := newUpstreamWaitPeer(t)
	peer.setPeers([]*discoverd.Instance{primary, self})
	errCh := applyConfigAsync(peer)
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected canceled reconfigure to return an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("applyConfig waited on an absent upstream")
	}
	<-db.waiting
}
