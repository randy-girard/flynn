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

