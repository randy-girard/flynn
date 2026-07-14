package bootstrap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	host "github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/cluster"
)

// statusServer is an httptest server that serves /host/status with a
// controllable Auth flag, mimicking a flynn-host daemon.
type statusServer struct {
	srv  *httptest.Server
	auth int32 // accessed atomically; non-zero means auth enabled
}

func newStatusServer(auth bool) *statusServer {
	s := &statusServer{}
	if auth {
		s.auth = 1
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/host/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&host.HostStatus{
			ID:   "test",
			Auth: atomic.LoadInt32(&s.auth) != 0,
		})
	})
	s.srv = httptest.NewServer(mux)
	return s
}

func (s *statusServer) setAuth(v bool) {
	if v {
		atomic.StoreInt32(&s.auth, 1)
	} else {
		atomic.StoreInt32(&s.auth, 0)
	}
}

func (s *statusServer) client() *cluster.Host {
	return cluster.NewHostWithKey("test", s.srv.URL, nil, nil, "")
}

func (s *statusServer) Close() { s.srv.Close() }

// TestWaitForHostAuthAllReady verifies that when every host already reports
// auth enabled, waitForHostAuth returns immediately without error.
func TestWaitForHostAuthAllReady(t *testing.T) {
	s1 := newStatusServer(true)
	defer s1.Close()
	s2 := newStatusServer(true)
	defer s2.Close()

	st := &State{
		Hosts:       []*cluster.Host{s1.client(), s2.client()},
		HostTimeout: 5 * time.Second,
	}

	done := make(chan error, 1)
	go func() { done <- waitForHostAuth(st) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForHostAuth did not return promptly when all hosts were ready")
	}
}

// TestWaitForHostAuthWaitsForRestart verifies that waitForHostAuth blocks while
// a host still reports auth disabled (pre-restart) and only returns once the
// host flips to auth enabled (post-restart). This guards the restart-race fix.
func TestWaitForHostAuthWaitsForRestart(t *testing.T) {
	ready := newStatusServer(true)
	defer ready.Close()
	pending := newStatusServer(false) // simulates pre-restart daemon
	defer pending.Close()

	st := &State{
		Hosts:       []*cluster.Host{ready.client(), pending.client()},
		HostTimeout: 5 * time.Second,
	}

	done := make(chan error, 1)
	go func() { done <- waitForHostAuth(st) }()

	// Should not have returned yet: the pending host is not auth-enabled.
	select {
	case err := <-done:
		t.Fatalf("waitForHostAuth returned before restart completed: %v", err)
	case <-time.After(600 * time.Millisecond):
	}

	// Simulate the daemon completing its restart with auth enabled.
	pending.setAuth(true)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil error after restart, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waitForHostAuth did not return after host reported auth enabled")
	}
}

// TestWaitForHostAuthTimeout verifies that waitForHostAuth returns an error if a
// host never reports auth enabled within HostTimeout.
func TestWaitForHostAuthTimeout(t *testing.T) {
	pending := newStatusServer(false)
	defer pending.Close()

	st := &State{
		Hosts:       []*cluster.Host{pending.client()},
		HostTimeout: 750 * time.Millisecond,
	}

	err := waitForHostAuth(st)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}
