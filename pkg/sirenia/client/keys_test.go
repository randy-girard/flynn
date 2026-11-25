package client

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/randy-girard/flynn/pkg/appliancehttp"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

// keyedPeer is a sirenia admin HTTP fake that, like an isolated database
// instance, accepts only its own APPLIANCE_KEY and records every key it was
// shown.
type keyedPeer struct {
	srv  *httptest.Server
	key  string
	mu   sync.Mutex
	seen []string
}

func newKeyedPeer(t *testing.T, key string, status Status) *keyedPeer {
	t.Helper()
	p := &keyedPeer{key: key}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.seen = append(p.seen, appliancehttp.RequestKey(r))
		p.mu.Unlock()
		if !appliancehttp.Authorized(r, p.key) {
			appliancehttp.Unauthorized(w)
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// addr is the sirenia peer address (the client adds one to the port).
func (p *keyedPeer) addr(t *testing.T) string {
	t.Helper()
	host, portStr, err := net.SplitHostPort(p.srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return net.JoinHostPort(host, strconv.Itoa(port-1))
}

func (p *keyedPeer) keysSeen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

func TestNewClientWithKeysSendsInstanceKeyNotClusterKey(t *testing.T) {
	t.Setenv("CONTROLLER_KEY", "cluster-secret")
	t.Setenv("AUTH_KEY", "")
	peer := newKeyedPeer(t, "instance-secret", Status{Database: &DatabaseInfo{ReadWrite: true}})

	if _, err := NewClient(peer.addr(t)).Status(); err == nil {
		t.Fatal("cluster-key client: want 401 from an instance that accepts only its APPLIANCE_KEY")
	}
	c := NewClientWithKeys(peer.addr(t), "instance-secret")
	if _, err := c.Status(); err != nil {
		t.Fatalf("Status with the instance key: %v", err)
	}
	if err := c.WaitForReadWrite(time.Second); err != nil {
		t.Fatalf("WaitForReadWrite with the instance key: %v", err)
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop with the instance key: %v", err)
	}
	seen := peer.keysSeen()
	for _, k := range seen[1:] {
		if k != "instance-secret" {
			t.Fatalf("keyed client presented %q to the instance; want only its APPLIANCE_KEY (all: %q)", k, seen)
		}
	}
}

func TestNewClientWithKeysFallsBackToNextKeyOn401(t *testing.T) {
	peer := newKeyedPeer(t, "new-secret", Status{Database: &DatabaseInfo{ReadWrite: true}})
	c := NewClientWithKeys(peer.addr(t), "old-secret", "new-secret")
	if _, err := c.Status(); err != nil {
		t.Fatalf("Status: want the second key to be tried after a 401: %v", err)
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: want the second key to be tried after a 401: %v", err)
	}
}

func TestNewClientWithKeysSurfacesUnauthorized(t *testing.T) {
	peer := newKeyedPeer(t, "instance-secret", Status{Database: &DatabaseInfo{ReadWrite: true}})
	c := NewClientWithKeys(peer.addr(t), "wrong-secret")
	start := time.Now()
	err := c.WaitForReadWrite(5 * time.Second)
	if err == nil {
		t.Fatal("WaitForReadWrite with a wrong key: want an error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("a 401 must fail at once, not wait out the timeout (%s)", time.Since(start))
	}
	if !IsUnauthorized(err) {
		t.Fatalf("want an unauthorized error, got %v", err)
	}
}

func TestWaitForReplSyncUsesTheSameKeysForTheDownstream(t *testing.T) {
	down := newKeyedPeer(t, "instance-secret", Status{Database: &DatabaseInfo{
		Running: true, XLog: "0/1",
		Config: &state.Config{Role: state.RoleSync, Upstream: mkInst("10.0.0.1:5432", "primary-1")},
	}})
	downInst := mkInst(down.addr(t), "sync-1")
	up := newKeyedPeer(t, "instance-secret", Status{
		Peer:     &state.PeerInfo{ID: "primary-1"},
		Database: &DatabaseInfo{Running: true, XLog: "0/1", SyncedDownstream: downInst},
	})
	if err := NewClientWithKeys(up.addr(t), "instance-secret").WaitForReplSync(downInst, "POSTGRES_ID", 2*time.Second); err != nil {
		t.Fatalf("WaitForReplSync: %v", err)
	}
	for _, k := range down.keysSeen() {
		if k != "instance-secret" {
			t.Fatalf("downstream saw key %q; want the upstream client's keys", k)
		}
	}
}
