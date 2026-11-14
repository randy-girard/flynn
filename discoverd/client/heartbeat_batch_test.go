package discoverd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNextHeartbeatIsIntervalMinusJitter(t *testing.T) {
	for i := 0; i < 1000; i++ {
		d := nextHeartbeat()
		if d <= heartbeatInterval-heartbeatJitter || d > heartbeatInterval {
			t.Fatalf("heartbeat %s is outside (3s, 5s]", d)
		}
	}
}

func TestSteadyHeartbeatsBatchIntoOneRequest(t *testing.T) {
	posts, puts := 0, 0
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/heartbeats" {
			posts++
			var body HeartbeatBatch
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode: %v", err)
			}
			if len(body.Instances) != 2 {
				t.Errorf("batch len %d", len(body.Instances))
			}
			_, _ = io.WriteString(w, `{}`)
			return
		}
		if r.Method == http.MethodPut {
			puts++
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := NewClientWithURL(srv.URL)
	restoreHeartbeatTiming(t)
	h1 := newHeartbeater(client, "web", &Instance{ID: "a", Addr: "10.0.0.1:1", Proto: "tcp"})
	h2 := newHeartbeater(client, "web", &Instance{ID: "b", Addr: "10.0.0.1:2", Proto: "tcp"})
	client.trackHeartbeat(h1)
	client.trackHeartbeat(h2)
	t.Cleanup(func() {
		client.untrackHeartbeat(h1)
		client.untrackHeartbeat(h2)
	})

	if failing := client.flushHeartbeats(); failing {
		t.Fatal("batch failed")
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 || puts != 0 {
		t.Fatalf("posts=%d puts=%d", posts, puts)
	}
}

func TestHeartbeatBatchFallsBackOnPlain404(t *testing.T) {
	var mu sync.Mutex
	posts, puts := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost && r.URL.Path == "/heartbeats" {
			posts++
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPut {
			puts++
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := NewClientWithURL(srv.URL)
	restoreHeartbeatTiming(t)
	h1 := newHeartbeater(client, "web", &Instance{ID: "a", Addr: "10.0.0.1:1", Proto: "tcp"})
	h2 := newHeartbeater(client, "web", &Instance{ID: "b", Addr: "10.0.0.1:2", Proto: "tcp"})
	client.trackHeartbeat(h1)
	client.trackHeartbeat(h2)
	t.Cleanup(func() {
		client.untrackHeartbeat(h1)
		client.untrackHeartbeat(h2)
	})

	if failing := client.flushHeartbeats(); failing {
		t.Fatal("fallback should succeed via PUT")
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 || puts != 2 {
		t.Fatalf("posts=%d puts=%d", posts, puts)
	}
}

func TestHeartbeatBatchMissingTriggersOnePut(t *testing.T) {
	var mu sync.Mutex
	var puts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/heartbeats" {
			_, _ = io.WriteString(w, `{"missing":[{"service":"web","id":"b"}]}`)
			return
		}
		if r.Method == http.MethodPut {
			puts = append(puts, r.URL.Path)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := NewClientWithURL(srv.URL)
	restoreHeartbeatTiming(t)
	h1 := newHeartbeater(client, "web", &Instance{ID: "a", Addr: "10.0.0.1:1", Proto: "tcp"})
	h2 := newHeartbeater(client, "web", &Instance{ID: "b", Addr: "10.0.0.1:2", Proto: "tcp"})
	client.trackHeartbeat(h1)
	client.trackHeartbeat(h2)
	t.Cleanup(func() {
		client.untrackHeartbeat(h1)
		client.untrackHeartbeat(h2)
	})
	if failing := client.flushHeartbeats(); failing {
		t.Fatal(failing)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(puts) != 1 || !strings.Contains(puts[0], "/services/web/instances/b") {
		t.Fatalf("puts %v", puts)
	}
}

func TestCloseDuringFlushDoesNotReregister(t *testing.T) {
	var mu sync.Mutex
	puts := 0
	var h *heartbeater
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			if h != nil {
				h.closing.Store(true)
			}
			_, _ = io.WriteString(w, `{"missing":[{"service":"web","id":"a"}]}`)
			return
		}
		if r.Method == http.MethodPut {
			puts++
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClientWithURL(srv.URL)
	restoreHeartbeatTiming(t)
	h = newHeartbeater(client, "web", &Instance{ID: "a", Addr: "10.0.0.1:1", Proto: "tcp"})
	client.trackHeartbeat(h)
	t.Cleanup(func() { client.untrackHeartbeat(h) })
	if failing := client.flushHeartbeats(); failing {
		t.Fatal(failing)
	}
	mu.Lock()
	defer mu.Unlock()
	if puts != 0 {
		t.Fatalf("closed heartbeater registered again: %d puts", puts)
	}
}

func TestHeartbeatBatchSendsAuthKeyThroughRedirect(t *testing.T) {
	var got string
	leader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Auth-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer leader.Close()
	follower := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Auth-Key") == "" {
			t.Errorf("follower missing Auth-Key")
		}
		http.Redirect(w, r, leader.URL+"/heartbeats", http.StatusTemporaryRedirect)
	}))
	defer follower.Close()

	client := NewClientWithURL(follower.URL)
	client.Key = "cluster-secret"
	restoreHeartbeatTiming(t)
	h := newHeartbeater(client, "web", &Instance{ID: "a", Addr: "10.0.0.1:1", Proto: "tcp"})
	client.trackHeartbeat(h)
	t.Cleanup(func() { client.untrackHeartbeat(h) })
	if failing := client.flushHeartbeats(); failing {
		t.Fatal(failing)
	}
	if got != "cluster-secret" {
		t.Fatalf("Auth-Key %q", got)
	}
}

func TestHeartbeatBatchSplitsAtLimit(t *testing.T) {
	var mu sync.Mutex
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body HeartbeatBatch
		_ = json.NewDecoder(r.Body).Decode(&body)
		sizes = append(sizes, len(body.Instances))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	prev := heartbeatBatchLimit
	heartbeatBatchLimit = 2
	t.Cleanup(func() { heartbeatBatchLimit = prev })

	client := NewClientWithURL(srv.URL)
	restoreHeartbeatTiming(t)
	var beats []*heartbeater
	for _, id := range []string{"a", "b", "c"} {
		h := newHeartbeater(client, "web", &Instance{ID: id, Addr: "10.0.0.1:" + id, Proto: "tcp"})
		beats = append(beats, h)
		client.trackHeartbeat(h)
	}
	t.Cleanup(func() {
		for _, h := range beats {
			client.untrackHeartbeat(h)
		}
	})
	if failing := client.flushHeartbeats(); failing {
		t.Fatal(failing)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sizes) != 2 || sizes[0] != 2 || sizes[1] != 1 {
		t.Fatalf("chunk sizes %v", sizes)
	}
}

func TestSetMetaPutsImmediately(t *testing.T) {
	var puts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	client := NewClientWithURL(srv.URL)
	h := newHeartbeater(client, "web", &Instance{ID: "a", Addr: "10.0.0.1:1", Proto: "tcp"})
	if err := h.SetMeta(map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if puts != 1 {
		t.Fatalf("puts %d", puts)
	}
}

func TestSetClientMovesHeartbeatToNewBatch(t *testing.T) {
	counts := map[string]int{}
	var mu sync.Mutex
	newSrv := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/heartbeats" {
				mu.Lock()
				counts[name]++
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{}`)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
	}
	a := newSrv("a")
	defer a.Close()
	b := newSrv("b")
	defer b.Close()

	clientA := NewClientWithURL(a.URL)
	clientB := NewClientWithURL(b.URL)
	restoreHeartbeatTiming(t)
	h := newHeartbeater(clientA, "web", &Instance{ID: "a", Addr: "10.0.0.1:1", Proto: "tcp"})
	h.registered.Store(true)
	clientA.trackHeartbeat(h)
	h.SetClient(clientB)
	t.Cleanup(func() {
		clientA.untrackHeartbeat(h)
		clientB.untrackHeartbeat(h)
	})
	if failing := clientA.flushHeartbeats(); failing {
		t.Fatal(failing)
	}
	if failing := clientB.flushHeartbeats(); failing {
		t.Fatal(failing)
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["a"] != 0 || counts["b"] != 1 {
		t.Fatalf("batch counts %#v", counts)
	}
}

func restoreHeartbeatTiming(t *testing.T) {
	t.Helper()
	prevDelay := heartbeatDelay
	prevRetry := heartbeatRetryDelay
	heartbeatDelay = func() time.Duration { return time.Hour }
	heartbeatRetryDelay = func() time.Duration { return time.Hour }
	t.Cleanup(func() {
		heartbeatDelay = prevDelay
		heartbeatRetryDelay = prevRetry
	})
}
