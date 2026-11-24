package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
