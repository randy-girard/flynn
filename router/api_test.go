package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/randy-girard/flynn/pkg/status"
	routerc "github.com/randy-girard/flynn/router/client"
)

// TestRouterAPIRequiresClusterKey: /events lists every route and backend
// address, /metrics every service, and /debug is pprof. Only the health
// check is public (EDGE-04).
func TestRouterAPIRequiresClusterKey(t *testing.T) {
	srv := httptest.NewServer(apiHandler(&Router{}, []string{"k1"}))
	defer srv.Close()

	get := func(path, key string) int {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if key != "" {
			req.SetBasicAuth("", key)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for _, path := range []string{"/events?types=set", "/metrics", "/debug/pprof/cmdline"} {
		if c := get(path, ""); c != http.StatusUnauthorized {
			t.Fatalf("GET %s without key = %d", path, c)
		}
		if c := get(path, "nope"); c != http.StatusUnauthorized {
			t.Fatalf("GET %s with wrong key = %d", path, c)
		}
	}
	if c := get("/metrics", "k1"); c != http.StatusOK {
		t.Fatalf("GET /metrics with key = %d", c)
	}
	// pprof is compiled in only with the pprof build tag (404 otherwise);
	// either way the key gets past the guard.
	if c := get("/debug/pprof/cmdline", "k1"); c == http.StatusUnauthorized {
		t.Fatalf("GET /debug/pprof/cmdline with key = %d", c)
	}
	if c := get(status.Path, ""); c != http.StatusOK {
		t.Fatalf("health check must stay public, got %d", c)
	}

	// The router client (controller scheduler and router-metrics) sends the
	// cluster key from its environment.
	t.Setenv("CONTROLLER_KEY", "")
	t.Setenv("AUTH_KEY", "k1,old")
	if _, err := routerc.NewWithAddr(srv.Listener.Addr().String()).GetMetrics(); err != nil {
		t.Fatalf("client with env key: %v", err)
	}
	if _, err := routerc.NewWithAddrKey(srv.Listener.Addr().String(), "").GetMetrics(); err == nil {
		t.Fatal("client without key must fail")
	}
}
