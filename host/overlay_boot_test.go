package main

import (
	"os"
	"strings"
	"testing"
)

// A reboot with a stored overlay wider than /16 installs the user-overlay
// DROP before flynn-net-user exists, and flynn-host exits. Job isolation
// creates that set, so it has to run first.
func TestOverlayExclusionFollowsJobIsolation(t *testing.T) {
	data, err := os.ReadFile("libcontainer_backend.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	start := strings.Index(body, "func (l *LibcontainerBackend) ConfigureNetworking(")
	if start < 0 {
		t.Fatal("ConfigureNetworking is missing")
	}
	rest := body[start:]
	if i := strings.Index(rest[1:], "\nfunc "); i >= 0 {
		rest = rest[:i+1]
	}
	isolation := strings.Index(rest, "EnableJobIsolation(")
	exclusion := strings.Index(rest, "UseOverlayExclusion(")
	if isolation < 0 || exclusion < 0 {
		t.Fatalf("boot path missing isolation (%d) or explicit exclusion (%d)", isolation, exclusion)
	}
	if exclusion < isolation {
		t.Fatal("UseOverlayExclusion references flynn-net-user and must run after EnableJobIsolation creates that set")
	}
}
