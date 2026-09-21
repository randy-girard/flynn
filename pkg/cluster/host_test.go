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
