package postgresql

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/inconshreveable/log15"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/status"
)

type hbStub struct {
	closed atomic.Bool
}

func (h *hbStub) SetMeta(map[string]string) error { return nil }
func (h *hbStub) Close() error                    { h.closed.Store(true); return nil }
func (h *hbStub) Addr() string                    { return "127.0.0.1:1" }
func (h *hbStub) SetClient(*discoverd.Client)     {}

func TestHandlerStatusAndStopWithoutPeer(t *testing.T) {
	h := NewHandler()
	h.Logger = log15.New()
	h.AuthKey = "test-appliance-key"
	if h.healthStatus() != status.Unhealthy {
		t.Fatal("missing peer must be unhealthy")
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.SetBasicAuth("", h.AuthKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}

	hb := &hbStub{}
	h.Heartbeater = hb
	stop := httptest.NewRequest(http.MethodPost, "/stop", nil)
	stop.SetBasicAuth("", h.AuthKey)
	stopRec := httptest.NewRecorder()
	h.ServeHTTP(stopRec, stop)
	if stopRec.Code != 200 {
		t.Fatalf("stop status=%d", stopRec.Code)
	}
	if !hb.closed.Load() {
		t.Fatal("stop must close the heartbeater before returning")
	}
}

func TestHandlerAdminHTTPRequiresAuth(t *testing.T) {
	h := NewHandler()
	h.Logger = log15.New()
	h.AuthKey = "cluster-secret"

	unauth := httptest.NewRequest(http.MethodPost, "/stop", nil)
	unauthRec := httptest.NewRecorder()
	h.ServeHTTP(unauthRec, unauth)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated stop=%d", unauthRec.Code)
	}

	adminStatus := httptest.NewRequest(http.MethodGet, "/status", nil)
	statusRec := httptest.NewRecorder()
	h.ServeHTTP(statusRec, adminStatus)
	if statusRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /status=%d", statusRec.Code)
	}

	health := httptest.NewRequest(http.MethodGet, status.Path, nil)
	healthRec := httptest.NewRecorder()
	h.ServeHTTP(healthRec, health)
	if healthRec.Code == http.StatusUnauthorized {
		t.Fatal("public health must stay unauthenticated")
	}

	bearer := httptest.NewRequest(http.MethodGet, "/status", nil)
	bearer.Header.Set("Authorization", "Bearer cluster-secret")
	bearerRec := httptest.NewRecorder()
	h.ServeHTTP(bearerRec, bearer)
	if bearerRec.Code != 200 {
		t.Fatalf("bearer status=%d", bearerRec.Code)
	}
}

// /status is polled every few seconds; while postgres is offline the same
// Info error must be logged once, not on every poll.
func TestHandlerStatusLogsInfoErrorOnce(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	h := NewHandler()
	h.Logger = log15.New()
	h.Logger.SetHandler(log15.FuncHandler(func(r *log15.Record) error {
		mu.Lock()
		msgs = append(msgs, r.Msg)
		mu.Unlock()
		return nil
	}))
	h.AuthKey = "test-appliance-key"
	h.Process = NewProcess(Config{Logger: log15.New()})

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/status", nil)
		req.SetBasicAuth("", h.AuthKey)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status=%d", rec.Code)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	n := 0
	for _, m := range msgs {
		if m == "error getting postgres info" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected 1 info error log, got %d (%v)", n, msgs)
	}
}

func TestParseMetricsSnapshot(t *testing.T) {
	got, ok := ParseMetricsSnapshot("8192|3|2|0|100|0.99|0.98|42|4096|777|0.5|128|1\n")
	if !ok {
		t.Fatal("parse failed")
	}
	for k, want := range map[string]float64{
		"db_size_bytes": 8192, "tables": 3, "active_connections": 2, "max_connections": 100,
		"xact_commit": 42, "current_transaction": 777, "follower_lag_bytes": 128, "slow_query_count": 1,
	} {
		if got[k] != want {
			t.Fatalf("%s=%v want %v", k, got[k], want)
		}
	}
	for _, bad := range []string{"", "1|2|3", "x|3|2|0|100|0.99|0.98|42|4096|777|0.5|128|1"} {
		if _, ok := ParseMetricsSnapshot(bad); ok {
			t.Fatalf("%q must not parse", bad)
		}
	}
}

func TestFormatMetricsLineOmitsMissingSamples(t *testing.T) {
	line := FormatMetricsLine("postgres", map[string]float64{"service_available": 0})
	if line != "flynn-postgres source=postgres addon=postgres sample#service-available=0" {
		t.Fatalf("line=%q", line)
	}
}

// flynn-host appends this fragment to the platform postgres job's minute
// metrics line, the same as for isolated postgres resources.
func TestMetricsHandlerServesFragmentToFlynnHost(t *testing.T) {
	srv := httptest.NewServer(MetricsHandler("postgres", func() map[string]float64 {
		return map[string]float64{"service_available": 1, "db_size_bytes": 8192, "tables": 3, "replay_lag_seconds": 0.25}
	}))
	defer srv.Close()
	ip, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	got := host.FetchMetricsFragment(srv.Client(), ip, port)
	want := "flynn-postgres source=postgres addon=postgres sample#service-available=1 sample#db_size=8192bytes sample#tables=3 sample#replay-lag-seconds=0.250"
	if got != want {
		t.Fatalf("fragment\n got %q\nwant %q", got, want)
	}

	res, err := http.Post(srv.URL+host.MetricsLogPath, "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d", res.StatusCode)
	}
	res, err = http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("metrics listener must not expose /status, got %d", res.StatusCode)
	}
}
