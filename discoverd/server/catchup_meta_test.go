package server_test

import (
	"net/http/httptest"
	"testing"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
)

// EDGE-119: a sirenia peer of a v20261114.0 cluster asks its local discoverd
// for the service meta after the upgrade replaces that discoverd. A store still
// re-applying its log has no postgres meta yet and answered 404 "service meta
// not found". The old sirenia took that as "meta gone", the primary republished
// at index 0, every later state write failed with object_exists, and the system
// Postgres deploy waited for an async that was never added. While the store
// catches up, meta reads must be a retryable 503 like instance reads.
func TestHandler_GetServiceMeta_CatchingUpIsRetryable(t *testing.T) {
	h := NewHandler()
	h.Store.CatchingUpFn = func() bool { return true }
	h.Store.ServiceMetaFn = func(service string) *discoverd.ServiceMeta { return nil }

	w := httptest.NewRecorder()
	h.ServeHTTP(w, MustNewHTTPRequest("GET", "/services/postgres/meta", nil))
	assertRetryable503(t, w)
}

// With a 503 the client asks the next peer in the same call and gets the meta.
func TestClient_GetMetaFailsOverFromCatchingUpPeer(t *testing.T) {
	stale := NewHandler()
	stale.Store.CatchingUpFn = func() bool { return true }
	stale.Store.ServiceMetaFn = func(service string) *discoverd.ServiceMeta { return nil }
	staleSrv := httptest.NewServer(stale)
	defer staleSrv.Close()

	current := NewHandler()
	current.Store.ServiceMetaFn = func(service string) *discoverd.ServiceMeta {
		return &discoverd.ServiceMeta{Index: 42, Data: []byte(`{"primary":{}}`)}
	}
	currentSrv := httptest.NewServer(current)
	defer currentSrv.Close()

	for i := 0; i < 20; i++ {
		c := discoverd.NewClientWithURL(staleSrv.URL + "," + currentSrv.URL)
		meta, err := c.Service("postgres").GetMeta()
		if err != nil {
			t.Fatalf("get meta %d: %v", i, err)
		}
		if meta.Index != 42 {
			t.Fatalf("get meta %d: index %d, want 42", i, meta.Index)
		}
	}
}
