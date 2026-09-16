package httphelper

import (
	"fmt"
	"strings"
	"testing"
)

func TestResolveDiscoverdAddr(t *testing.T) {
	got, err := ResolveDiscoverdAddr("dashboard.discoverd:80", func(service string) ([]string, error) {
		if service != "dashboard" {
			t.Fatalf("service=%s", service)
		}
		return []string{"100.64.61.21:80", "100.64.61.22:80"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "100.64.61.21:80" {
		t.Fatalf("got %s", got)
	}

	passthrough, err := ResolveDiscoverdAddr("example.com:443", func(string) ([]string, error) {
		t.Fatal("lookup must not run for non-discoverd hosts")
		return nil, nil
	})
	if err != nil || passthrough != "example.com:443" {
		t.Fatalf("passthrough=%s err=%v", passthrough, err)
	}

	_, err = ResolveDiscoverdAddr("missing.discoverd:80", func(string) ([]string, error) {
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "no such host") {
		t.Fatalf("empty lookup: %v", err)
	}

	_, err = ResolveDiscoverdAddr("down.discoverd:80", func(string) ([]string, error) {
		return nil, fmt.Errorf("discoverd unavailable")
	})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("lookup error: %v", err)
	}

	_, err = ResolveDiscoverdAddr("not-a-host-port", func(string) ([]string, error) { return nil, nil })
	if err == nil {
		t.Fatal("SplitHostPort must fail")
	}

	_, err = ResolveDiscoverdAddr("x.discoverd:80", nil)
	if err == nil || !strings.Contains(err.Error(), "no discoverd resolver") {
		t.Fatalf("nil lookup: %v", err)
	}
}
