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
	"testing"
	"time"

	discoverd "github.com/flynn/flynn/discoverd/client"
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
			SyncedDownstream: oldSync,
		},
	}
	if check(statusOldID) {
		t.Fatal("expected false when Meta identity differs despite same discoverd ID")
	}

	statusNewID := &Status{
		Database: &DatabaseInfo{
			SyncedDownstream: newSync,
		},
	}
	if !check(statusNewID) {
		t.Fatal("expected true when Meta identity matches")
	}

	statusNil := &Status{Database: &DatabaseInfo{}}
	if check(statusNil) {
		t.Fatal("expected false when SyncedDownstream is nil")
	}
}

func TestSyncedWithFallsBackToDiscoverdID(t *testing.T) {
	downstream := mkInst("10.0.0.3:5432", "async-1")
	check := SyncedWith(downstream, "")

	if !check(&Status{Database: &DatabaseInfo{SyncedDownstream: downstream}}) {
		t.Fatal("expected true when discoverd IDs match and idKey is empty")
	}
	other := mkInst("10.0.0.4:5432", "async-2")
	if check(&Status{Database: &DatabaseInfo{SyncedDownstream: other}}) {
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
	if check(&Status{Database: &DatabaseInfo{SyncedDownstream: synced}}) {
		t.Fatal("expected false when expected Meta id is empty")
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
