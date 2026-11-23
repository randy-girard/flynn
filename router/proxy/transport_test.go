package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	router "github.com/randy-girard/flynn/router/types"
)

// A route update replaces its proxy while a request may still hold a backend
// connection. That connection goes back to the old transport's pool after
// Close, so the pool must reap idle connections on its own.
func TestHTTPTransportReapsIdleConnections(t *testing.T) {
	for _, disable := range []bool{false, true} {
		tr := newHTTPTransport(disable)
		if tr.IdleConnTimeout <= 0 || tr.IdleConnTimeout > 2*time.Minute {
			t.Fatalf("disableKeepAlives=%v: IdleConnTimeout = %s, want (0, 2m]", disable, tr.IdleConnTimeout)
		}
	}
}

func TestReverseProxyCloseClosesIdleBackendConnections(t *testing.T) {
	var mtx sync.Mutex
	open := map[net.Conn]struct{}{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	}))
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		mtx.Lock()
		defer mtx.Unlock()
		switch s {
		case http.StateNew:
			open[c] = struct{}{}
		case http.StateClosed, http.StateHijacked:
			delete(open, c)
		}
	}
	srv.Start()
	defer srv.Close()
	count := func() int {
		mtx.Lock()
		defer mtx.Unlock()
		return len(open)
	}
	waitFor := func(n int) int {
		deadline := time.Now().Add(5 * time.Second)
		for count() != n && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		return count()
	}

	addr := srv.Listener.Addr().String()
	p := NewReverseProxy(ReverseProxyConfig{
		BackendListFunc: func() []*router.Backend {
			return []*router.Backend{{Service: "test", Addr: addr}}
		},
		RequestTracker: nopTracker{},
		Logger:         log15.New(),
		Service:        "test",
	})
	req := httptest.NewRequest("GET", "http://example.com/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := waitFor(1); got != 1 {
		t.Fatalf("open backend conns before Close = %d, want 1", got)
	}

	p.Close()
	if got := waitFor(0); got != 0 {
		t.Fatalf("open backend conns after Close = %d, want 0", got)
	}
	p.Close() // a second Close is harmless
}

type nopTracker struct{}

func (nopTracker) TrackRequestStart(string) {}
func (nopTracker) TrackRequestDone(string)  {}
