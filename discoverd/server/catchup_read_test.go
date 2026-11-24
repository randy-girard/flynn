package server_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/discoverd/server"
	"github.com/randy-girard/flynn/pkg/keepalive"
)

// EDGE-69: a discoverd that was just replaced answers from its snapshot until
// it has re-applied its raft log. On the dev cluster a replaced peer answered
// GET /services/controller/instances with 404 "service not found" for about a
// second, and another answered with controller addresses from before the
// last controller deploy. flynn-host update dialled those and failed. While
// the store catches up, instance and leader reads must be a retryable 503 so
// clients fail over to a peer that is current.

func TestHandler_GetInstances_CatchingUpIsRetryable(t *testing.T) {
	h := NewHandler()
	h.Store.CatchingUpFn = func() bool { return true }
	h.Store.InstancesFn = func(service string) ([]*discoverd.Instance, error) {
		// What a restarted store answers before its log is re-applied:
		// nothing, or addresses from an old snapshot.
		return []*discoverd.Instance{{ID: "stale", Addr: "100.100.49.4:80"}}, nil
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, MustNewHTTPRequest("GET", "/services/controller/instances", nil))
	assertRetryable503(t, w)
}

func TestHandler_GetLeader_CatchingUpIsRetryable(t *testing.T) {
	h := NewHandler()
	h.Store.CatchingUpFn = func() bool { return true }
	h.Store.ServiceLeaderFn = func(service string) (*discoverd.Instance, error) {
		return &discoverd.Instance{ID: "stale", Addr: "100.100.49.4:80"}, nil
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, MustNewHTTPRequest("GET", "/services/controller/leader", nil))
	assertRetryable503(t, w)
}

func TestHandler_GetInstances_ServesOnceCaughtUp(t *testing.T) {
	h := NewHandler()
	h.Store.CatchingUpFn = func() bool { return false }
	h.Store.InstancesFn = func(service string) ([]*discoverd.Instance, error) {
		return []*discoverd.Instance{{ID: "a", Addr: "100.100.58.10:80"}}, nil
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, MustNewHTTPRequest("GET", "/services/controller/instances", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
}

// The client pins whichever peer answered last. A peer that answers 404 or a
// stale list keeps the pin, so every lookup kept hitting it. With a 503 the
// client moves to the next peer in the same call.
func TestClient_LookupFailsOverFromCatchingUpPeer(t *testing.T) {
	stale := NewHandler()
	stale.Store.CatchingUpFn = func() bool { return true }
	stale.Store.InstancesFn = func(service string) ([]*discoverd.Instance, error) { return nil, nil }
	staleSrv := httptest.NewServer(stale)
	defer staleSrv.Close()

	current := NewHandler()
	current.Store.InstancesFn = func(service string) ([]*discoverd.Instance, error) {
		return []*discoverd.Instance{{ID: "a", Addr: "100.100.58.10:80", Proto: "http"}}, nil
	}
	currentSrv := httptest.NewServer(current)
	defer currentSrv.Close()

	// The client orders unpinned peers by map iteration, so repeat with fresh
	// clients: about half of them try the catching-up peer first.
	for i := 0; i < 20; i++ {
		c := discoverd.NewClientWithURL(staleSrv.URL + "," + currentSrv.URL)
		addrs, err := c.Service("controller").Addrs()
		if err != nil {
			t.Fatalf("lookup %d: %v", i, err)
		}
		if len(addrs) != 1 || addrs[0] != "100.100.58.10:80" {
			t.Fatalf("lookup %d: addrs %v", i, addrs)
		}
	}
}

// A real store reopened on its old data must not serve its snapshot over HTTP
// before it has re-applied the log it had before the restart.
func TestStore_RestartedStoreRefusesInstanceReadsUntilCaughtUp(t *testing.T) {
	s := MustOpenStore()
	if err := s.AddService("controller", nil); err != nil {
		s.Close()
		t.Fatal(err)
	}
	for _, addr := range []string{"100.100.49.4:80", "100.100.66.4:80"} {
		if err := s.AddInstance("controller", &discoverd.Instance{ID: md5ID(addr), Proto: "http", Addr: addr}); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	if err := s.TriggerSnapshot(); err != nil {
		s.Close()
		t.Fatal(err)
	}
	// Written after the snapshot: only the log has these.
	for _, addr := range []string{"100.100.58.10:80", "100.100.66.23:80"} {
		if err := s.AddInstance("controller", &discoverd.Instance{ID: md5ID(addr), Proto: "http", Addr: addr}); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	s.Store.Close()

	_, port, _ := net.SplitHostPort(s.Listener.Addr().String())
	ln, err := keepalive.ReusableListen("tcp4", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	s = NewStoreWithConfig(StoreConfig{Path: s.Path(), Listener: ln})
	defer s.Close()
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	h := server.NewHandler(false, []string{""})
	h.Store = s.Store

	w := httptest.NewRecorder()
	h.ServeHTTP(w, MustNewHTTPRequest("GET", "/services/controller/instances", nil))
	if w.Code == http.StatusOK {
		var got []*discoverd.Instance
		json.Unmarshal(w.Body.Bytes(), &got)
		if len(got) != 4 {
			t.Fatalf("restarted store served %d instances before catching up: %s", len(got), w.Body.String())
		}
	} else {
		assertRetryable503(t, w)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, MustNewHTTPRequest("GET", "/services/controller/instances", nil))
		if w.Code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("store did not catch up: %d %s", w.Code, w.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	var got []*discoverd.Instance
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("caught-up store has %d instances, want 4: %s", len(got), w.Body.String())
	}
}

func md5ID(addr string) string { return "inst-" + addr }

func assertRetryable503(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d body %s, want 503 while the store catches up", w.Code, w.Body.String())
	}
	var e struct {
		Code  string `json:"code"`
		Retry bool   `json:"retry"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.Code != "service_unavailable" || !e.Retry {
		t.Fatalf("body %s, want a retryable service_unavailable", w.Body.String())
	}
}
