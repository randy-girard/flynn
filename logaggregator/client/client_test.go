package client

import "testing"

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
