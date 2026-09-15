package postgresql

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/status"
	"github.com/inconshreveable/log15"
)

type hbStub struct {
	closed bool
}

func (h *hbStub) SetMeta(map[string]string) error { return nil }
func (h *hbStub) Close() error                    { h.closed = true; return nil }
func (h *hbStub) Addr() string                    { return "127.0.0.1:1" }
func (h *hbStub) SetClient(*discoverd.Client)     {}

func TestHandlerStatusAndStopWithoutPeer(t *testing.T) {
	h := NewHandler()
	h.Logger = log15.New()
	if h.healthStatus() != status.Unhealthy {
		t.Fatal("missing peer must be unhealthy")
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}

	hb := &hbStub{}
	h.Heartbeater = hb
	stop := httptest.NewRequest(http.MethodPost, "/stop", nil)
	stopRec := httptest.NewRecorder()
	h.ServeHTTP(stopRec, stop)
	if stopRec.Code != 200 {
		t.Fatalf("stop status=%d", stopRec.Code)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !hb.closed {
		time.Sleep(10 * time.Millisecond)
	}
	if !hb.closed {
		t.Fatal("stop must close the heartbeater")
	}
}
