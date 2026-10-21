package data

import "testing"

func TestValidAppDomain(t *testing.T) {
	ok := []string{"flynnapps.com", "1.localflynn.com", "localhost", " Apps.Example.com "}
	for _, s := range ok {
		if !ValidAppDomain(s) {
			t.Fatalf("want valid %q", s)
		}
	}
	bad := []string{"", "https://flynnapps.com", "flynnapps.com/path", "*.flynnapps.com", "has space.com", "not a domain"}
	for _, s := range bad {
		if ValidAppDomain(s) {
			t.Fatalf("want invalid %q", s)
		}
	}
}

func TestEffectiveAppDomain(t *testing.T) {
	if got := EffectiveAppDomain("flynnapps.com", "flynndashboard.com"); got != "flynnapps.com" {
		t.Fatalf("override %q", got)
	}
	if got := EffectiveAppDomain("", "flynndashboard.com"); got != "flynndashboard.com" {
		t.Fatalf("fallback %q", got)
	}
	if got := EffectiveAppDomain("  ", "FlynnApps.com"); got != "flynnapps.com" {
		t.Fatalf("empty override %q", got)
	}
}
