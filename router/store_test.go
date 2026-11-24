package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	router "github.com/randy-girard/flynn/router/types"
)

// previousControllerAPI serves only what a controller from before list paging
// (2026-11-10) served the router: GET /routes (count and cursor ignored, the
// whole list every time) and the GET /events stream. Any other path is a
// request the previous controller cannot answer.
func previousControllerAPI(t *testing.T, routes []*router.Route, event *router.Route) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/routes":
			_ = json.NewEncoder(w).Encode(routes)
		case "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			data, _ := json.Marshal(event)
			ev, _ := json.Marshal(&ct.Event{ID: 1, ObjectType: ct.EventTypeRoute, ObjectID: event.ID, Data: data})
			fmt.Fprintf(w, "data: %s\n\n", ev)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			t.Errorf("router asked the previous controller for %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// EDGE-30: during `flynn-host update` the router runs against the controller
// release before or after its own, so the route sync has to work against the
// previous controller API, including a list of exactly one page.
func TestControllerStoreWorksWithPreviousControllerAPI(t *testing.T) {
	stamp := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, n := range []int{3, 1000, 1001} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			routes := make([]*router.Route, n)
			for i := range routes {
				routes[i] = &router.Route{ID: fmt.Sprintf("r%04d", i), Type: "http", Domain: fmt.Sprintf("a%d.example", i), CreatedAt: stamp.Add(-time.Duration(i) * time.Second)}
			}
			added := &router.Route{ID: "new", Type: "http", Domain: "new.example", CreatedAt: stamp}
			srv := previousControllerAPI(t, routes, added)
			client, err := controller.NewClient(srv.URL, "test-key")
			if err != nil {
				t.Fatal(err)
			}
			store := &ControllerStore{client}
			got, err := store.List()
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(got) != n {
				t.Fatalf("List returned %d routes, want %d", len(got), n)
			}
			ch := make(chan *router.Event)
			stream, err := store.Watch(ch)
			if err != nil {
				t.Fatalf("Watch: %v", err)
			}
			defer stream.Close()
			select {
			case e := <-ch:
				if e.Event != router.EventTypeRouteSet || e.ID != "new" || e.Route.Domain != "new.example" {
					t.Fatalf("event %+v", e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no route event from the previous controller's stream")
			}
		})
	}
}

// eventController is a fake controller for the route sync: GET /routes lists
// the current routes, GET /events streams route events with ids and, like
// the real controller, replays the events after a positive Last-Event-Id
// and nothing for 0. With anchor set it also sends the stream start id the
// way the controller does since EDGE-94.
type eventController struct {
	t      *testing.T
	anchor bool

	mu        sync.Mutex
	routes    map[string]*router.Route
	log       []*ct.Event
	streams   map[int]chan *ct.Event
	nextConn  int
	asked     []string
	hold      chan struct{} // while set, new /events requests wait for it to close
	afterList func()        // runs once, after the first /routes page is written
}

func newEventController(t *testing.T, anchor bool, routes ...*router.Route) (*eventController, *ControllerStore) {
	f := &eventController{t: t, anchor: anchor, routes: map[string]*router.Route{}, streams: map[int]chan *ct.Event{}}
	for _, r := range routes {
		f.routes[r.ID] = r
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	client, err := controller.NewClient(srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	return f, &ControllerStore{client}
}

func (f *eventController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/routes":
		f.mu.Lock()
		list := make([]*router.Route, 0, len(f.routes))
		for _, route := range f.routes {
			list = append(list, route)
		}
		var hook func()
		if r.URL.Query().Get("before") == "" {
			hook, f.afterList = f.afterList, nil
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(list)
		w.(http.Flusher).Flush()
		if hook != nil {
			hook()
		}
	case "/events":
		f.serveEvents(w, r)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

func (f *eventController) serveEvents(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-r.Context().Done():
			return
		}
	}
	lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-Id"), 10, 64)
	f.mu.Lock()
	f.asked = append(f.asked, r.Header.Get("Last-Event-Id"))
	var replay []*ct.Event
	if lastID > 0 {
		for _, e := range f.log {
			if e.ID > lastID {
				replay = append(replay, e)
			}
		}
	}
	var start int64
	if n := len(f.log); n > 0 {
		start = f.log[n-1].ID
	}
	live := make(chan *ct.Event, 100)
	n := f.nextConn
	f.nextConn++
	f.streams[n] = live
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.streams, n)
		f.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	if f.anchor && lastID == 0 {
		w.Header().Set("X-Last-Event-Id", strconv.FormatInt(start, 10))
	}
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	send := func(e *ct.Event) {
		data, _ := json.Marshal(e)
		fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.ID, data)
		w.(http.Flusher).Flush()
	}
	for _, e := range replay {
		send(e)
	}
	for {
		select {
		case e, ok := <-live:
			if !ok {
				return // dropped by the test
			}
			send(e)
		case <-r.Context().Done():
			return
		}
	}
}

// event records a route change and sends it to every open stream.
func (f *eventController) event(typ ct.EventType, route *router.Route) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if typ == ct.EventTypeRouteDeletion {
		delete(f.routes, route.ID)
	} else {
		f.routes[route.ID] = route
	}
	data, _ := json.Marshal(route)
	e := &ct.Event{ID: int64(len(f.log) + 1), ObjectType: typ, ObjectID: route.ID, Data: data}
	f.log = append(f.log, e)
	for _, live := range f.streams {
		live <- e
	}
}

// dropStreams ends every open event stream, and makes new ones wait until
// the returned function is called.
func (f *eventController) dropStreams() (release func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hold := make(chan struct{})
	f.hold = hold
	for n, live := range f.streams {
		close(live)
		delete(f.streams, n)
	}
	return func() {
		f.mu.Lock()
		f.hold = nil
		f.mu.Unlock()
		close(hold)
	}
}

// syncedRoutes is a SyncHandler that keeps the routes it was given.
type syncedRoutes struct {
	mu     sync.Mutex
	routes map[string]*router.Route
	sets   int
}

func (h *syncedRoutes) Set(r *router.Route) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.routes[r.ID] = r
	h.sets++
	return nil
}

func (h *syncedRoutes) Remove(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.routes[id]; !ok {
		return ErrNotFound
	}
	delete(h.routes, id)
	return nil
}

func (h *syncedRoutes) Current() map[string]struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]struct{}, len(h.routes))
	for id := range h.routes {
		out[id] = struct{}{}
	}
	return out
}

func (h *syncedRoutes) ids() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.routes))
	for id := range h.routes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// runRouteSync runs the syncer the way HTTPListener.runSync does (sync, and
// on any error sync again after a delay), and returns once the first sync
// has started.
func runRouteSync(t *testing.T, store Store) *syncedRoutes {
	t.Helper()
	h := &syncedRoutes{routes: map[string]*router.Route{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	syncer := NewSyncer(store, "http")
	started := make(chan struct{})
	go func() {
		defer close(done)
		first := true
		for ctx.Err() == nil {
			startc := make(chan struct{})
			errc := make(chan error, 1)
			go func() { errc <- syncer.Sync(ctx, h, startc) }()
			var err error
			select {
			case <-startc:
				if first {
					close(started)
					first = false
				}
				err = <-errc
			case err = <-errc:
			}
			if err != nil {
				t.Logf("sync error: %v", err)
			} else if ctx.Err() == nil {
				t.Errorf("sync returned nil before shutdown, HTTPListener.runSync would stop syncing")
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first sync did not start")
	}
	return h
}

func waitRoutes(t *testing.T, h *syncedRoutes, want ...string) {
	t.Helper()
	sort.Strings(want)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := h.ids()
		if fmt.Sprint(got) == fmt.Sprint(want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("router has routes %v, want %v", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func syncRoute(id string) *router.Route {
	return &router.Route{ID: id, Type: "http", Domain: id + ".example", CreatedAt: time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)}
}

// EDGE-94: the router's controller event stream dropped before it had seen
// any event, a route was deleted while it was down, and the stream came back
// with Last-Event-Id 0, so the deletion was never delivered and the router
// served the deleted route for 16 minutes. After any reconnect the router
// must list again and drop routes that are gone, against the previous
// controller (no stream start id) and the current one.
func TestWatchResyncsAfterStreamDropsBeforeFirstEvent(t *testing.T) {
	for _, anchor := range []bool{false, true} {
		t.Run(fmt.Sprintf("anchor=%v", anchor), func(t *testing.T) {
			f, store := newEventController(t, anchor, syncRoute("keep"), syncRoute("gone"))
			h := runRouteSync(t, store)
			waitRoutes(t, h, "keep", "gone")

			release := f.dropStreams()
			f.event(ct.EventTypeRouteDeletion, syncRoute("gone"))
			f.event(ct.EventTypeRoute, syncRoute("added"))
			release()

			waitRoutes(t, h, "keep", "added")
		})
	}
}

// The same after the stream has seen events: whatever the cursor, a dropped
// stream ends in a full list and reconcile.
func TestWatchResyncsAfterStreamDropsAfterEvents(t *testing.T) {
	f, store := newEventController(t, false, syncRoute("keep"), syncRoute("gone"))
	h := runRouteSync(t, store)
	f.event(ct.EventTypeRoute, syncRoute("seen"))
	waitRoutes(t, h, "keep", "gone", "seen")

	release := f.dropStreams()
	f.event(ct.EventTypeRouteDeletion, syncRoute("gone"))
	release()

	waitRoutes(t, h, "keep", "seen")
}

// A change made after the list was read but before the router processed the
// stream must not be lost: the stream is opened before the list, so the
// change arrives on it.
func TestSyncKeepsChangesBetweenListAndStream(t *testing.T) {
	f, store := newEventController(t, true, syncRoute("keep"), syncRoute("gone"))
	f.afterList = func() {
		f.event(ct.EventTypeRouteDeletion, syncRoute("gone"))
		f.event(ct.EventTypeRoute, syncRoute("added"))
	}
	h := runRouteSync(t, store)
	waitRoutes(t, h, "keep", "added")
}

// Events the router has already applied (a replay after a resume, or a set
// for a listed route) and removals of routes it does not have are harmless.
func TestSyncAppliesDuplicateEventsIdempotently(t *testing.T) {
	f, store := newEventController(t, true, syncRoute("a"), syncRoute("b"))
	h := runRouteSync(t, store)
	waitRoutes(t, h, "a", "b")
	f.event(ct.EventTypeRoute, syncRoute("a"))
	f.event(ct.EventTypeRoute, syncRoute("a"))
	f.event(ct.EventTypeRouteDeletion, syncRoute("never-listed"))
	f.event(ct.EventTypeRouteDeletion, syncRoute("b"))
	f.event(ct.EventTypeRouteDeletion, syncRoute("b"))
	f.event(ct.EventTypeRoute, syncRoute("c"))
	waitRoutes(t, h, "a", "c")
	f.mu.Lock()
	conns := f.nextConn
	f.mu.Unlock()
	if conns != 1 {
		t.Fatalf("duplicate events caused %d stream connections, want 1 (no resync)", conns)
	}
}
