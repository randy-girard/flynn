package main

import (
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	router "github.com/randy-girard/flynn/router/types"
)

// httpConcurrency caps in-flight HTTP requests per ParentRef on this process.
// The counter is local. It is not a connection limit, a file-descriptor limit,
// or a cluster-wide lock. TCP accepts are not counted.
type httpConcurrency struct {
	mu     sync.Mutex
	apps   map[string]*httpAppCounter
	routes map[string]httpCapBinding
}

type httpCapBinding struct {
	key   string
	limit int64
}

type httpAppCounter struct {
	limit    atomic.Int64
	inFlight atomic.Int64
}

func newHTTPConcurrency() *httpConcurrency {
	return &httpConcurrency{
		apps:   make(map[string]*httpAppCounter),
		routes: make(map[string]httpCapBinding),
	}
}

// httpConcurrencyKey groups every HTTP route of one app. An empty ParentRef
// does not share a counter with other empty-ParentRef routes.
func httpConcurrencyKey(routeID, parent string) string {
	if parent != "" {
		return "app:" + parent
	}
	return "route:" + routeID
}

func normalizeHTTPConcurrency(n int) int64 {
	if n <= 0 {
		return router.DefaultMaxConcurrentRequests
	}
	if n > router.MaxMaxConcurrentRequests {
		return router.MaxMaxConcurrentRequests
	}
	return int64(n)
}

// setRoute records one route's limit and sets the app's enforced limit to the
// minimum of the routes currently synced for that key. A partial update cannot
// raise the cap. Lowering takes effect as soon as one route carries the lower
// value.
func (h *httpConcurrency) setRoute(id, parent string, limit int) {
	if h == nil || id == "" {
		return
	}
	key := httpConcurrencyKey(id, parent)
	next := httpCapBinding{key: key, limit: normalizeHTTPConcurrency(limit)}
	h.mu.Lock()
	defer h.mu.Unlock()
	prev, ok := h.routes[id]
	h.routes[id] = next
	if ok && prev.key != key {
		h.recomputeLocked(prev.key)
	}
	h.recomputeLocked(key)
}

func (h *httpConcurrency) removeRoute(id string) {
	if h == nil || id == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	prev, ok := h.routes[id]
	if !ok {
		return
	}
	delete(h.routes, id)
	h.recomputeLocked(prev.key)
}

func (h *httpConcurrency) recomputeLocked(key string) {
	var min int64
	found := false
	for _, b := range h.routes {
		if b.key != key {
			continue
		}
		if !found || b.limit < min {
			min = b.limit
			found = true
		}
	}
	app := h.apps[key]
	if app == nil {
		app = &httpAppCounter{}
		h.apps[key] = app
	}
	if !found {
		// No synced route. Leave a default so a stray request is still capped.
		app.limit.Store(router.DefaultMaxConcurrentRequests)
		return
	}
	app.limit.Store(min)
}

func (h *httpConcurrency) tryAcquire(routeID, parent string) (limit, inflight int64, ok bool) {
	if h == nil {
		return router.DefaultMaxConcurrentRequests, 0, true
	}
	key := httpConcurrencyKey(routeID, parent)
	h.mu.Lock()
	app := h.apps[key]
	if app == nil {
		app = &httpAppCounter{}
		app.limit.Store(router.DefaultMaxConcurrentRequests)
		h.apps[key] = app
	}
	h.mu.Unlock()
	return app.tryAcquire()
}

func (h *httpConcurrency) release(routeID, parent string) {
	if h == nil {
		return
	}
	key := httpConcurrencyKey(routeID, parent)
	h.mu.Lock()
	app := h.apps[key]
	h.mu.Unlock()
	if app == nil {
		return
	}
	app.inFlight.Add(-1)
}

func (h *httpConcurrency) limitFor(parent string) int64 {
	if h == nil || parent == "" {
		return 0
	}
	h.mu.Lock()
	app := h.apps[httpConcurrencyKey("", parent)]
	h.mu.Unlock()
	if app == nil {
		return 0
	}
	return app.limit.Load()
}

func (a *httpAppCounter) tryAcquire() (limit, inflight int64, ok bool) {
	for {
		limit = a.limit.Load()
		cur := a.inFlight.Load()
		if limit <= 0 || cur >= limit {
			return limit, cur, false
		}
		if a.inFlight.CompareAndSwap(cur, cur+1) {
			limit = a.limit.Load()
			if int64(cur+1) > limit {
				a.inFlight.Add(-1)
				return limit, cur, false
			}
			return limit, cur + 1, true
		}
	}
}

const httpConcurrencyLimitedBody = "App HTTP concurrency limit exceeded\n"

func writeHTTPConcurrencyLimited(w http.ResponseWriter) {
	body := []byte(httpConcurrencyLimitedBody)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Retry-After", "1")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write(body)
}
