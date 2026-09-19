package plugin

import (
	"strings"
	"testing"
)

func TestParsePluginCalVer(t *testing.T) {
	got, ok := ParsePluginCalVer("v20260919.2")
	if !ok || got != (PluginCalVer{Date: 20260919, N: 2, P: 0}) {
		t.Fatalf("two-part: %+v ok=%v", got, ok)
	}
	got, ok = ParsePluginCalVer(" v20260919.2.10 ")
	if !ok || got != (PluginCalVer{Date: 20260919, N: 2, P: 10}) {
		t.Fatalf("three-part: %+v ok=%v", got, ok)
	}
	if _, ok := ParsePluginCalVer("v20260919"); ok {
		t.Fatal("date-only must not parse")
	}
	if _, ok := ParsePluginCalVer("v20260919.2.1-smoke"); ok {
		t.Fatal("suffix must not parse")
	}
}

func TestParseFlynnCalVer(t *testing.T) {
	got, ok := ParseFlynnCalVer("v20260919.0")
	if !ok || got != (PluginCalVer{Date: 20260919, N: 0, P: 0}) {
		t.Fatalf("two-part: %+v ok=%v", got, ok)
	}
	got, ok = ParseFlynnCalVer("v20260919.0-abc123")
	if !ok || got.FlynnLine() != "v20260919.0" {
		t.Fatalf("commit suffix: %+v ok=%v", got, ok)
	}
	if _, ok := ParseFlynnCalVer("v20260919.0.1"); ok {
		t.Fatal("plugin three-part must not parse as Flynn")
	}
	if _, ok := ParseFlynnCalVer("dev"); ok {
		t.Fatal("dev must not parse as Flynn calver")
	}
	if _, ok := ParseFlynnCalVer("v20260919"); ok {
		t.Fatal("date-only must not parse")
	}
}

func TestPluginMatchesFlynn(t *testing.T) {
	if !PluginMatchesFlynn("v20260919.0", "v20260919.0") {
		t.Fatal("two-part plugin matches Flynn")
	}
	if !PluginMatchesFlynn("v20260919.0.3", "v20260919.0") {
		t.Fatal("plugin patch B matches same Flynn date.N")
	}
	if !PluginMatchesFlynn("v20260919.0.0", "v20260919.0-deadbeef") {
		t.Fatal("Flynn commit suffix must still match")
	}
	if PluginMatchesFlynn("v20260919.1.0", "v20260919.0") {
		t.Fatal("newer Flynn N must not match")
	}
	if PluginMatchesFlynn("v20260920.0.1", "v20260919.0") {
		t.Fatal("newer Flynn date must not match")
	}
	if PluginMatchesFlynn("v1", "v20260919.0") {
		t.Fatal("non-calver plugin must not match")
	}
	if PluginMatchesFlynn("v20260919.0.1", "dev") {
		t.Fatal("dev Flynn has no calver line to match")
	}
}

func TestHighestCompatiblePluginTag(t *testing.T) {
	tags := []string{
		"v20260919.0",
		"v20260919.0.2",
		"v20260919.0.10",
		"v20260919.1.0",
		"v20260920.0.0",
		"not-a-calver",
	}
	got, ok := HighestCompatiblePluginTag(tags, "v20260919.0")
	if !ok || got != "v20260919.0.10" {
		t.Fatalf("got %q ok=%v, want highest B for v20260919.0", got, ok)
	}
	if _, ok := HighestCompatiblePluginTag(tags, "v20260918.0"); ok {
		t.Fatal("no tags for an older Flynn date.N")
	}
	if _, ok := HighestCompatiblePluginTag(tags, "dev"); ok {
		t.Fatal("dev Flynn has no compatible calver")
	}
}

func TestComparePluginCalVer(t *testing.T) {
	eq := [][2]string{
		{"v20260919.2", "v20260919.2.0"},
		{"v20260919.2.0", "v20260919.2"},
	}
	for _, c := range eq {
		if ComparePluginCalVer(c[0], c[1]) != 0 {
			t.Errorf("%s vs %s: want equal", c[0], c[1])
		}
	}
	if ComparePluginCalVer("v20260919.2.1", "v20260919.2") <= 0 {
		t.Fatal("plugin patch must sort above the Flynn-aligned .0 / two-part tag")
	}
	if ComparePluginCalVer("v20260919.2.10", "v20260919.2.9") <= 0 {
		t.Fatal("numeric patch order")
	}
	if ComparePluginCalVer("v20260920.0.0", "v20260919.9.9") <= 0 {
		t.Fatal("newer date wins")
	}
	if ComparePluginCalVer("v20260919.2.0", "v1") <= 0 {
		t.Fatal("calver sorts above non-calver")
	}
}

func TestIncompatiblePluginError(t *testing.T) {
	flynn, ok := ParseFlynnCalVer("v20260919.0")
	if !ok {
		t.Fatal("parse Flynn")
	}
	err := incompatiblePluginError("v20260920.0.1", flynn)
	if err == nil || !containsAll(err.Error(), "v20260920.0.1", "v20260920.0", "v20260919.0") {
		t.Fatalf("calver mismatch: %v", err)
	}
	err = incompatiblePluginError("v1", flynn)
	if err == nil || !containsAll(err.Error(), "v1", "v20260919.0") {
		t.Fatalf("non-calver: %v", err)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
