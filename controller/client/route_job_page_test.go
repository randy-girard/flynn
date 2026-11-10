package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	router "github.com/randy-girard/flynn/router/types"
)

func TestRouteListWalksPages(t *testing.T) {
	stamp := time.Date(2026, 3, 4, 5, 6, 7, 8, time.UTC)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if q.Get("count") != "1000" {
			t.Errorf("RouteList query %s", r.URL.RawQuery)
			_ = json.NewEncoder(w).Encode([]*router.Route{{ID: "unpaged", Type: "http"}})
			return
		}
		if q.Get("before_id") == "" {
			page := make([]*router.Route, 1000)
			for i := range page {
				page[i] = &router.Route{ID: fmt.Sprintf("r%04d", i), Type: "http", CreatedAt: stamp}
			}
			_ = json.NewEncoder(w).Encode(page)
			return
		}
		if q.Get("before_id") != "http/r0999" {
			t.Errorf("cursor id %s", q.Get("before_id"))
		}
		older := stamp.Add(-time.Second)
		_ = json.NewEncoder(w).Encode([]*router.Route{{ID: "tail", Type: "tcp", CreatedAt: older}})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	routes, err := c.RouteList()
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1001 || routes[0].ID != "r0000" || routes[1000].ID != "tail" {
		t.Fatalf("got %d routes, calls %d", len(routes), calls)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
}

func TestRouteListErrorsWhenLaterPageFails(t *testing.T) {
	stamp := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("before_id") != "" {
			http.Error(w, "page 2 down", http.StatusInternalServerError)
			return
		}
		page := make([]*router.Route, 1000)
		for i := range page {
			page[i] = &router.Route{ID: fmt.Sprintf("r%04d", i), Type: "http", CreatedAt: stamp}
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	routes, err := c.RouteList()
	if err == nil || routes != nil {
		t.Fatalf("partial routes=%d err=%v", len(routes), err)
	}
}

func TestRouteListTreatsOversizedBodyAsComplete(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		page := make([]*router.Route, 1001)
		for i := range page {
			page[i] = &router.Route{ID: fmt.Sprintf("r%04d", i), Type: "http", CreatedAt: time.Unix(100, 0).UTC()}
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	routes, err := c.RouteList()
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1001 || calls != 1 {
		t.Fatalf("len=%d calls=%d", len(routes), calls)
	}
}

func TestRouteListErrorsOnRepeatedID(t *testing.T) {
	stamp := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := make([]*router.Route, 1000)
		for i := range page {
			page[i] = &router.Route{ID: "same", Type: "http", CreatedAt: stamp}
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.RouteList()
	if err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatal(err)
	}
}

func TestJobListActiveWalksPagesAndErrorsOnLaterFailure(t *testing.T) {
	stamp := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/active-jobs" {
			t.Errorf("path %s", r.URL.Path)
		}
		calls++
		q := r.URL.Query()
		if q.Get("count") != "1000" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		if calls == 1 {
			page := make([]*ct.Job, 1000)
			for i := range page {
				ts := stamp
				page[i] = &ct.Job{UUID: fmt.Sprintf("j%04d", i), State: ct.JobStateUp, CreatedAt: &ts}
			}
			_ = json.NewEncoder(w).Encode(page)
			return
		}
		http.Error(w, "page 2 down", http.StatusBadGateway)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := c.JobListActive()
	if err == nil || jobs != nil {
		t.Fatalf("partial jobs=%d err=%v", len(jobs), err)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
}

func TestJobListActiveStopsOnShortPage(t *testing.T) {
	var calls int
	var cursor string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		cursor = r.URL.Query().Get("before_id")
		ts := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
		_ = json.NewEncoder(w).Encode([]*ct.Job{{UUID: "only", State: ct.JobStatePending, CreatedAt: &ts}})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := c.JobListActive()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].UUID != "only" || calls != 1 || cursor != "" {
		t.Fatalf("jobs=%v calls=%d cursor=%s", jobs, calls, cursor)
	}
}
