package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestAppListWalksPages(t *testing.T) {
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if q.Get("all") != "true" {
			t.Errorf("AppList query %s", r.URL.RawQuery)
		}
		if q.Get("count") != "1000" {
			_ = json.NewEncoder(w).Encode([]*ct.App{{ID: "unpaged", Name: "unpaged"}})
			return
		}
		if q.Get("before_id") == "" {
			page := make([]*ct.App, 1000)
			for i := range page {
				ts := stamp
				page[i] = &ct.App{ID: fmt.Sprintf("a%04d", i), Name: "n", CreatedAt: &ts}
			}
			_ = json.NewEncoder(w).Encode(page)
			return
		}
		if q.Get("before_id") != "a0999" {
			t.Errorf("cursor id %s", q.Get("before_id"))
		}
		older := stamp.Add(-time.Second)
		_ = json.NewEncoder(w).Encode([]*ct.App{{ID: "tail", Name: "tail", CreatedAt: &older}})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	apps, err := c.AppList()
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1001 || apps[0].ID != "a0000" || apps[1000].ID != "tail" {
		t.Fatalf("got %d apps, first %s last %s, calls %d", len(apps), idOf(apps, 0), idOf(apps, len(apps)-1), calls)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
}

func TestAppListVisibleStopsOnShortPage(t *testing.T) {
	var query string
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		query = r.URL.RawQuery
		if r.URL.Query().Get("all") != "" {
			t.Errorf("visible list must not request the operator catalog: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode([]*ct.App{{ID: "one", Name: "one"}})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	apps, err := c.AppListVisible()
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0].ID != "one" {
		t.Fatalf("%+v", apps)
	}
	if calls != 1 || !containsCount(query) {
		t.Fatalf("calls=%d query=%s", calls, query)
	}
}

func containsCount(q string) bool {
	return len(q) > 0 && (q == "count=1000" || len(q) > len("count=1000") && (q[:len("count=1000")] == "count=1000" || containsParam(q, "count=1000")))
}

func containsParam(q, want string) bool {
	for i := 0; i+len(want) <= len(q); i++ {
		if q[i:i+len(want)] == want {
			return true
		}
	}
	return false
}

func idOf(apps []*ct.App, i int) string {
	if i < 0 || i >= len(apps) || apps[i] == nil {
		return ""
	}
	return apps[i].ID
}
