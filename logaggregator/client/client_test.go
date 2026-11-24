package client

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNewUsesClusterKeyFromEnv: the controller and logaggregator itself call
// New and must send the cluster key the API now requires (EDGE-04).
func TestNewUsesClusterKeyFromEnv(t *testing.T) {
	t.Setenv("CONTROLLER_KEY", "")
	t.Setenv("AUTH_KEY", "first,second")
	c, err := New("http://x")
	if err != nil {
		t.Fatal(err)
	}
	if c.Key != "first" {
		t.Fatalf("Key=%q", c.Key)
	}
	t.Setenv("CONTROLLER_KEY", "ck")
	c, _ = New("")
	if c.Key != "ck" || c.URL != "http://logaggregator.discoverd" {
		t.Fatalf("Key=%q URL=%q", c.Key, c.URL)
	}
	c, _ = NewWithKey("http://x", "explicit")
	if c.Key != "explicit" {
		t.Fatalf("NewWithKey Key=%q", c.Key)
	}
}

// TestGetCursorsWithLimitsFromOlderAggregator (EDGE-85): an aggregator that
// does not advertise its limits reports none (zero), not an error.
func TestGetCursorsWithLimitsFromOlderAggregator(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"host1":{"Time":"2026-11-22T00:00:00Z","Seq":7}}`))
	}))
	defer api.Close()
	c, _ := NewWithKey(api.URL, "k")
	cursors, limits, err := c.GetCursorsWithLimits()
	if err != nil {
		t.Fatal(err)
	}
	if cursors["host1"].Seq != 7 {
		t.Fatalf("cursors=%v", cursors)
	}
	if limits.Lines != 0 || limits.TTL != 0 {
		t.Fatalf("limits=%+v, want none", limits)
	}
}
