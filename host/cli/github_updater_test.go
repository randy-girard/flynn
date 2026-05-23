package cli

import (
	"testing"

	"github.com/flynn/flynn/pkg/cluster"
	"github.com/inconshreveable/log15"
)

func TestNormalizeHostname(t *testing.T) {
	if got, want := normalizeHostname("Flynn-Test_Node-1"), "flynntestnode1"; got != want {
		t.Fatalf("normalizeHostname: got %q, want %q", got, want)
	}
}

func TestFindLocalHostPrefersDaemonID(t *testing.T) {
	log := log15.New()
	h1 := cluster.NewHost("h1", "10.0.0.1:1113", nil, nil)
	h2 := cluster.NewHost("daemon", "10.0.0.2:1113", nil, nil)
	h := findLocalHost([]*cluster.Host{h1, h2}, "flynn-test-node-1", "daemon", map[string]struct{}{"10.0.0.1": {}}, log)
	if h == nil || h.ID() != "daemon" {
		t.Fatalf("expected daemon host, got %#v", h)
	}
}

func TestFindLocalHostMatchesIP(t *testing.T) {
	log := log15.New()
	h1 := cluster.NewHost("h1", "10.0.0.1:1113", nil, nil)
	h2 := cluster.NewHost("h2", "10.0.0.2:1113", nil, nil)
	h := findLocalHost([]*cluster.Host{h1, h2}, "irrelevant", "", map[string]struct{}{"10.0.0.2": {}}, log)
	if h == nil || h.ID() != "h2" {
		t.Fatalf("expected h2, got %#v", h)
	}
}

func TestFindLocalHostMatchesNormalizedHostname(t *testing.T) {
	log := log15.New()
	h1 := cluster.NewHost("flynntestnode1", "10.0.0.1:1113", nil, nil)
	h := findLocalHost([]*cluster.Host{h1}, "flynn-test-node-1", "", nil, log)
	if h == nil || h.ID() != "flynntestnode1" {
		t.Fatalf("expected flynntestnode1, got %#v", h)
	}
}

func TestFindLocalHostSingleHostFallback(t *testing.T) {
	log := log15.New()
	h1 := cluster.NewHost("only", "10.0.0.1:1113", nil, nil)
	h := findLocalHost([]*cluster.Host{h1}, "no-match", "", nil, log)
	if h == nil || h.ID() != "only" {
		t.Fatalf("expected only host, got %#v", h)
	}
}

func TestFindLocalHostMultipleIPMatchesPicksFirst(t *testing.T) {
	log := log15.New()
	h1 := cluster.NewHost("h1", "10.0.0.1:1113", nil, nil)
	h2 := cluster.NewHost("h2", "10.0.0.1:2222", nil, nil)
	h := findLocalHost([]*cluster.Host{h1, h2}, "no-match", "", map[string]struct{}{"10.0.0.1": {}}, log)
	if h == nil || h.ID() != "h1" {
		t.Fatalf("expected h1, got %#v", h)
	}
}

func TestFindLocalHostNoMatch(t *testing.T) {
	log := log15.New()
	h1 := cluster.NewHost("h1", "10.0.0.1:1113", nil, nil)
	h2 := cluster.NewHost("h2", "10.0.0.2:1113", nil, nil)
	h := findLocalHost([]*cluster.Host{h1, h2}, "no-match", "", map[string]struct{}{"10.0.0.99": {}}, log)
	if h != nil {
		t.Fatalf("expected nil, got %#v", h)
	}
}

// parseHostFromURL drives the coordinator-IP fallback in getCoordinatorIP:
// when discoverd hasn't seen the local daemon re-register yet, we use the
// daemon's own status.URL (e.g. "http://192.168.56.20:1113") to determine
// the cluster-routable IP rather than scanning local interfaces (which
// can return a hypervisor NAT address that peers can't reach).
func TestParseHostFromURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"http://192.168.56.20:1113", "192.168.56.20"},
		{"http://10.0.0.1:1113", "10.0.0.1"},
		{"http://[fd17:625c:f037:2::1]:1113", "fd17:625c:f037:2::1"},
		{"http://example.host:1113", "example.host"},
		{"http://192.168.56.20", "192.168.56.20"},
		{"", ""},
		{"::not a url::", ""},
	}
	for _, c := range cases {
		if got := parseHostFromURL(c.in); got != c.want {
			t.Errorf("parseHostFromURL(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

