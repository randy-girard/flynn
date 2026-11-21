package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	host "github.com/randy-girard/flynn/host/types"
)

func TestNewHostWithKeyAndStatus(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/host/status" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(host.HostStatus{ID: "node1"})
	}))
	defer srv.Close()

	h := NewHostWithKey("node1", srv.URL, srv.Client(), map[string]string{"disk": "ssd"}, "host-secret")
	if h.ID() != "node1" || h.Tags()["disk"] != "ssd" {
		t.Fatalf("%s %v", h.ID(), h.Tags())
	}
	st, err := h.GetStatus()
	if err != nil {
		t.Fatal(err)
	}
	if st.ID != "node1" {
		t.Fatalf("%+v", st)
	}
	req, _ := http.NewRequest("GET", "/", nil)
	req.SetBasicAuth("", "host-secret")
	if gotAuth != req.Header.Get("Authorization") {
		t.Fatalf("auth=%q", gotAuth)
	}

	bare := NewHostWithKey("n", "127.0.0.1:1113", nil, nil, "")
	if bare.Addr() != "127.0.0.1:1113" {
		t.Fatalf("addr=%q", bare.Addr())
	}
}

func TestListJobsContextCanceled(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()

	h := NewHost("node1", srv.URL, srv.Client(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := h.ListJobsContext(ctx)
		errc <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("server was not hit")
	}
	cancel()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("canceled ListJobsContext must fail")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListJobsContext did not honor cancel")
	}
}

// GetOverlayIP: 200 is the registration, 404 is nil with no error, and any
// other failure is an error, so discoverd DNS can tell "no such job" from
// "host unreachable" (both fail closed there).
func TestGetOverlayIP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/host/netpolicy/ips/100.100.1.5":
			_ = json.NewEncoder(w).Encode(host.OverlayIP{IP: "100.100.1.5", JobID: "j1", Meta: map[string]string{"class": "user"}})
		case "/host/netpolicy/ips/100.100.1.6":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":"object_not_found","message":"no job has this overlay ip"}`))
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	h := NewHostWithKey("n", srv.URL, srv.Client(), nil, "k")
	ctx := context.Background()

	got, err := h.GetOverlayIP(ctx, "100.100.1.5")
	if err != nil || got == nil || got.JobID != "j1" || got.Meta["class"] != "user" {
		t.Fatalf("found: %+v %v", got, err)
	}
	if got, err := h.GetOverlayIP(ctx, "100.100.1.6"); err != nil || got != nil {
		t.Fatalf("not found: %+v %v", got, err)
	}
	if _, err := h.GetOverlayIP(ctx, "100.100.1.7"); err == nil {
		t.Fatal("500 must be an error")
	}
}
