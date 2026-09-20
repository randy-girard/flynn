package client

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

func mkInst(addr, id string) *discoverd.Instance {
	inst := &discoverd.Instance{
		Addr:  addr,
		Proto: "tcp",
		Meta:  map[string]string{"POSTGRES_ID": id},
	}
	sum := md5.Sum([]byte(inst.Proto + "-" + inst.Addr))
	inst.ID = hex.EncodeToString(sum[:])
	return inst
}

func TestSyncedWithUsesMetaIdentity(t *testing.T) {
	oldSync := mkInst("10.0.0.2:5432", "old-sync")
	newSync := mkInst("10.0.0.2:5432", "new-sync")
	if oldSync.ID != newSync.ID {
		t.Fatalf("test precondition: peers should share discoverd ID")
	}

	check := SyncedWith(newSync, "POSTGRES_ID")

	statusOldID := &Status{
		Database: &DatabaseInfo{
			Running:          true,
			SyncedDownstream: oldSync,
		},
	}
	if check(statusOldID) {
		t.Fatal("expected false when Meta identity differs despite same discoverd ID")
	}

	statusNewID := &Status{
		Database: &DatabaseInfo{
			Running:          true,
			SyncedDownstream: newSync,
		},
	}
	if !check(statusNewID) {
		t.Fatal("expected true when Meta identity matches")
	}

	statusNil := &Status{Database: &DatabaseInfo{Running: true}}
	if check(statusNil) {
		t.Fatal("expected false when SyncedDownstream is nil")
	}
	if check(&Status{Database: &DatabaseInfo{Running: false, SyncedDownstream: newSync}}) {
		t.Fatal("expected false when upstream postgres is not running")
	}
}

func TestSyncedWithFallsBackToDiscoverdID(t *testing.T) {
	downstream := mkInst("10.0.0.3:5432", "async-1")
	check := SyncedWith(downstream, "")

	if !check(&Status{Database: &DatabaseInfo{Running: true, SyncedDownstream: downstream}}) {
		t.Fatal("expected true when discoverd IDs match and idKey is empty")
	}
	other := mkInst("10.0.0.4:5432", "async-2")
	if check(&Status{Database: &DatabaseInfo{Running: true, SyncedDownstream: other}}) {
		t.Fatal("expected false when discoverd IDs differ")
	}
}

func TestSyncedWithIgnoresEmptyMeta(t *testing.T) {
	expected := &discoverd.Instance{
		Addr:  "10.0.0.2:5432",
		Proto: "tcp",
		Meta:  map[string]string{"POSTGRES_ID": ""},
	}
	synced := mkInst("10.0.0.2:5432", "other")
	check := SyncedWith(expected, "POSTGRES_ID")
	if check(&Status{Database: &DatabaseInfo{Running: true, SyncedDownstream: synced}}) {
		t.Fatal("expected false when expected Meta id is empty")
	}
}

func sireniaTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	host, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return NewClient(net.JoinHostPort(host, strconv.Itoa(port-1)))
}

func TestWaitForReplSyncRequiresDownstreamRunning(t *testing.T) {
	var downRunning int32
	downSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Status{
			Database: &DatabaseInfo{
				Running: atomic.LoadInt32(&downRunning) == 1,
				XLog:    "0/1",
				Config: &state.Config{
					Role:     state.RoleAsync,
					Upstream: &discoverd.Instance{Meta: map[string]string{"POSTGRES_ID": "primary-1"}},
				},
			},
		})
	}))
	defer downSrv.Close()

	_, downPortStr, err := net.SplitHostPort(downSrv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	downPort, _ := strconv.Atoi(downPortStr)
	peer := mkInst(net.JoinHostPort("127.0.0.1", strconv.Itoa(downPort-1)), "new-async")

	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Status{
			Peer: &state.PeerInfo{ID: "primary-1"},
			Database: &DatabaseInfo{
				Running:          true,
				SyncedDownstream: peer,
				XLog:             "0/1",
			},
		})
	}))
	defer upSrv.Close()

	up := sireniaTestClient(t, upSrv)
	errCh := make(chan error, 1)
	go func() { errCh <- up.WaitForReplSync(peer, "POSTGRES_ID", 3*time.Second) }()

	select {
	case err := <-errCh:
		t.Fatalf("WaitForReplSync succeeded while downstream was down: %v", err)
	case <-time.After(400 * time.Millisecond):
	}

	atomic.StoreInt32(&downRunning, 1)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("WaitForReplSync: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WaitForReplSync did not succeed after downstream started")
	}
}

func TestIsTailAsync(t *testing.T) {
	sync := mkInst("10.0.0.2:5432", "sync")
	first := mkInst("10.0.0.3:5432", "async-0")
	tail := mkInst("10.0.0.4:5432", "async-1")
	s := &state.State{Sync: sync, Async: []*discoverd.Instance{first, tail}}
	if !IsTailAsync(s, tail, "POSTGRES_ID") {
		t.Fatal("expected tail async")
	}
	if IsTailAsync(s, first, "POSTGRES_ID") {
		t.Fatal("first async is not the tail")
	}
	if IsTailAsync(&state.State{}, tail, "POSTGRES_ID") {
		t.Fatal("empty async list is not a tail")
	}
}

func TestSyncReplaceTopologyReady(t *testing.T) {
	oldSync := mkInst("10.0.0.2:5432", "old-sync")
	first := mkInst("10.0.0.3:5432", "new-primary")
	newPeer := mkInst("10.0.0.4:5432", "new-sync")
	ready := SyncReplaceTopologyReady(oldSync, first, newPeer, "POSTGRES_ID")

	ok := &Status{Peer: &state.PeerInfo{State: &state.State{
		Sync:  oldSync,
		Async: []*discoverd.Instance{first, newPeer},
	}}}
	if !ready(ok) {
		t.Fatal("expected ready when new peer is tail async and old sync is still recorded")
	}

	insertedFirst := &Status{Peer: &state.PeerInfo{State: &state.State{
		Sync:  oldSync,
		Async: []*discoverd.Instance{newPeer, first},
	}}}
	if ready(insertedFirst) {
		t.Fatal("must not treat a new peer inserted at the front as ready")
	}

	tookOver := &Status{Peer: &state.PeerInfo{State: &state.State{
		Sync:  first,
		Async: []*discoverd.Instance{newPeer},
	}}}
	if ready(tookOver) {
		t.Fatal("must not proceed after the old sync has already been taken over")
	}

	onlyNew := &Status{Peer: &state.PeerInfo{State: &state.State{
		Sync:  oldSync,
		Async: []*discoverd.Instance{newPeer},
	}}}
	if ready(onlyNew) {
		t.Fatal("a single async means the existing first async was dropped")
	}

	if ready(&Status{}) || ready(nil) {
		t.Fatal("nil status is not ready")
	}
}

func TestReplicaOf(t *testing.T) {
	upstream := mkInst("10.0.0.2:5432", "async-0")
	check := ReplicaOf(upstream, "POSTGRES_ID")
	if !check(&Status{Database: &DatabaseInfo{
		Running: true,
		Config:  &state.Config{Upstream: mkInst("10.0.0.2:5432", "async-0")},
	}}) {
		t.Fatal("expected true when running database follows upstream")
	}
	if check(&Status{Database: &DatabaseInfo{
		Running: false,
		Config:  &state.Config{Upstream: mkInst("10.0.0.2:5432", "async-0")},
	}}) {
		t.Fatal("must wait until postgres is running (base backup finished)")
	}
	if check(&Status{Database: &DatabaseInfo{
		Running: true,
		Config:  &state.Config{Upstream: mkInst("10.0.0.9:5432", "other")},
	}}) {
		t.Fatal("must not treat a replica of a different peer as ready")
	}
	if check(&Status{}) || check(nil) {
		t.Fatal("nil status is not a replica")
	}
}

func TestAsyncReplaceTopologyReady(t *testing.T) {
	oldAsync := mkInst("10.0.0.3:5432", "old-async")
	newPeer := mkInst("10.0.0.4:5432", "new-async")
	sync := mkInst("10.0.0.2:5432", "sync")
	ready := AsyncReplaceTopologyReady(oldAsync, newPeer, "POSTGRES_ID")

	ok := &Status{Peer: &state.PeerInfo{State: &state.State{
		Sync:  sync,
		Async: []*discoverd.Instance{oldAsync, newPeer},
	}}}
	if !ready(ok) {
		t.Fatal("expected ready when new peer is tail and old async is still recorded")
	}

	front := &Status{Peer: &state.PeerInfo{State: &state.State{
		Sync:  sync,
		Async: []*discoverd.Instance{newPeer, oldAsync},
	}}}
	if ready(front) {
		t.Fatal("must not treat a new peer inserted at the front as ready")
	}

	dropped := &Status{Peer: &state.PeerInfo{State: &state.State{
		Sync:  sync,
		Async: []*discoverd.Instance{newPeer},
	}}}
	if ready(dropped) {
		t.Fatal("must not proceed after the old async has already been dropped")
	}
}

func TestWaitUntilSyncReplaceTopology(t *testing.T) {
	oldSync := mkInst("10.0.0.2:5432", "old-sync")
	first := mkInst("10.0.0.3:5432", "new-primary")
	newPeer := mkInst("10.0.0.4:5432", "new-sync")
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := &state.State{Sync: oldSync, Async: []*discoverd.Instance{first}}
		if atomic.AddInt32(&n, 1) >= 2 {
			st.Async = []*discoverd.Instance{first, newPeer}
		}
		_ = json.NewEncoder(w).Encode(Status{Peer: &state.PeerInfo{State: st}})
	}))
	defer srv.Close()

	c := sireniaTestClient(t, srv)
	if err := c.WaitUntil(SyncReplaceTopologyReady(oldSync, first, newPeer, "POSTGRES_ID"), 3*time.Second); err != nil {
		t.Fatalf("WaitUntil: %v (calls=%d)", err, n)
	}
	if n < 2 {
		t.Fatalf("expected to poll until the new peer appeared as tail async, calls=%d", n)
	}
}

func TestDownstreamFollowsUpstream(t *testing.T) {
	up := &Status{Peer: &state.PeerInfo{ID: "primary-1"}}
	follow := &Status{Database: &DatabaseInfo{Config: &state.Config{
		Upstream: &discoverd.Instance{Meta: map[string]string{"POSTGRES_ID": "primary-1"}},
	}}}
	if !downstreamFollowsUpstream(up, follow, "POSTGRES_ID") {
		t.Fatal("expected follow")
	}
	other := &Status{Database: &DatabaseInfo{Config: &state.Config{
		Upstream: &discoverd.Instance{Meta: map[string]string{"POSTGRES_ID": "other"}},
	}}}
	if downstreamFollowsUpstream(up, other, "POSTGRES_ID") {
		t.Fatal("must not treat a replica of a different peer as caught up")
	}
}

func TestWaitForReadWriteEventually(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		rw := calls >= 2
		_ = json.NewEncoder(w).Encode(Status{
			Database: &DatabaseInfo{ReadWrite: rw},
		})
	}))
	defer srv.Close()

	host, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	// Client maps postgres port N to HTTP N+1; use postgres port one below listener.
	pgPort := port - 1
	c := NewClient(net.JoinHostPort(host, strconv.Itoa(pgPort)))
	if err := c.WaitForReadWrite(5 * time.Second); err != nil {
		t.Fatalf("WaitForReadWrite: %v (calls=%d)", err, calls)
	}
	if calls < 2 {
		t.Fatalf("expected multiple status polls before read-write, got %d", calls)
	}
}

func TestWaitForRetriesHungStatus(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			time.Sleep(250 * time.Millisecond)
			return
		}
		_ = json.NewEncoder(w).Encode(Status{
			Database: &DatabaseInfo{ReadWrite: true},
		})
	}))
	defer srv.Close()

	host, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	c := NewClientWithHTTP(net.JoinHostPort(host, strconv.Itoa(port-1)), &http.Client{Timeout: 80 * time.Millisecond})
	if err := c.WaitForReadWrite(2 * time.Second); err != nil {
		t.Fatalf("WaitForReadWrite: %v (calls=%d)", err, n)
	}
	if n < 2 {
		t.Fatalf("expected hung status to be retried, calls=%d", n)
	}
}

func TestSamePeer(t *testing.T) {
	a := mkInst("10.0.0.1:5432", "peer-a")
	bMoved := mkInst("10.0.0.9:5432", "peer-a")
	bOther := mkInst("10.0.0.1:5432", "peer-b")
	noMeta := &discoverd.Instance{ID: a.ID, Addr: "10.0.0.1:5432"}

	tests := []struct {
		name  string
		key   string
		left  *discoverd.Instance
		right *discoverd.Instance
		want  bool
	}{
		{"nil left", "POSTGRES_ID", nil, a, false},
		{"nil right", "POSTGRES_ID", a, nil, false},
		{"same appliance id new address", "POSTGRES_ID", a, bMoved, true},
		{"same address different appliance id", "POSTGRES_ID", a, bOther, false},
		{"empty key falls back to discoverd ID", "", a, noMeta, true},
		{"empty key different discoverd ID", "", a, bMoved, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SamePeer(tc.key, tc.left, tc.right); got != tc.want {
				t.Fatalf("SamePeer() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsRecoverableStopError(t *testing.T) {
	if IsRecoverableStopError(nil) {
		t.Fatal("nil should not be recoverable")
	}
	if !IsRecoverableStopError(fmt.Errorf(`Post "http://10.0.0.1:3307/stop": context deadline exceeded`)) {
		t.Fatal("expected context deadline exceeded to be recoverable")
	}
	if !IsRecoverableStopError(fmt.Errorf("Client.Timeout exceeded while awaiting headers")) {
		t.Fatal("expected client timeout to be recoverable")
	}
	if IsRecoverableStopError(fmt.Errorf("connection refused")) {
		t.Fatal("connection refused should not be recoverable")
	}
}

func TestIsPeerUnreachableError(t *testing.T) {
	if IsPeerUnreachableError(nil) {
		t.Fatal("nil should not be unreachable")
	}
	unreachable := []string{
		`Post "http://100.100.65.11:3307/stop": dial tcp 100.100.65.11:3307: i/o timeout`,
		`Get "http://10.0.0.1:3307/status": dial tcp 10.0.0.1:3307: connect: connection refused`,
		`dial tcp 10.0.0.1:3307: connect: no route to host`,
		`dial tcp 10.0.0.1:3307: connect: network is unreachable`,
		`read tcp 10.0.0.1:3307: connection reset by peer`,
	}
	for _, msg := range unreachable {
		if !IsPeerUnreachableError(fmt.Errorf("%s", msg)) {
			t.Fatalf("expected %q to be unreachable", msg)
		}
	}
	if IsPeerUnreachableError(fmt.Errorf("context deadline exceeded")) {
		t.Fatal("mid-request timeout should not be treated as unreachable")
	}
}

func TestStopReturnsWithoutWaitingForSlowShutdown(t *testing.T) {
	stopped := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stop" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(200)
		close(stopped)
		go func() {
			time.Sleep(2 * time.Second)
		}()
	}))
	defer srv.Close()

	host, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	c := NewClient(net.JoinHostPort(host, strconv.Itoa(port-1)))

	done := make(chan error, 1)
	go func() { done <- c.Stop() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop should return promptly after HTTP response")
	}

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop handler was not invoked")
	}
}
