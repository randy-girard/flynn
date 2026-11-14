package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/host/logmux"
	hosttypes "github.com/randy-girard/flynn/host/types"
)

func TestGetStatusReportsLogDrops(t *testing.T) {
	mux := logmux.New("host-1", t.TempDir(), log15.New())
	mux.StoreDropsForTest(7)

	h := &Host{
		status: &hosttypes.HostStatus{ID: "host-1", URL: "http://10.0.0.8:1113"},
		state:  &State{logMux: mux},
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/host/status", nil)
	(&jobAPI{host: h}).GetStatus(rr, req, nil)
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var got hosttypes.HostStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.LogDrops != 7 {
		t.Fatalf("log_drops=%d body=%s", got.LogDrops, rr.Body.Bytes())
	}
	if !strings.Contains(rr.Body.String(), `"log_drops":7`) {
		t.Fatalf("body %s", rr.Body.Bytes())
	}

	h.authKey = "secret"
	rr = httptest.NewRecorder()
	(&jobAPI{host: h}).GetStatus(rr, req, nil)
	got = hosttypes.HostStatus{}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.LogDrops != 7 {
		t.Fatalf("liveness log_drops=%d body=%s", got.LogDrops, rr.Body.Bytes())
	}
	if strings.Contains(rr.Body.String(), `"discoverd"`) || strings.Contains(rr.Body.String(), `"network"`) {
		t.Fatalf("liveness leaked topology: %s", rr.Body.Bytes())
	}
}
