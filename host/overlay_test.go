package main

import (
	"fmt"
	"testing"
)

func TestCollapseConsecutiveLowerDirs(t *testing.T) {
	in := []string{"/a", "/b", "/b", "/c", "/c", "/c", "/d"}
	got := collapseConsecutiveLowerDirs(in)
	want := []string{"/a", "/b", "/c", "/d"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestDedupeLowerDirPaths(t *testing.T) {
	in := []string{"/a", "/b", "/c", "/b", "/d", "/a"}
	got := dedupeLowerDirPaths(in)
	want := []string{"/a", "/b", "/c", "/d"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestBuildOverlayLowerdirSingleLayer(t *testing.T) {
	got, err := buildOverlayLowerdir([]string{"/only"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != "/only" {
		t.Fatalf("got %q, want %q", got, "/only")
	}
}

func TestBuildOverlayLowerdirMaterializationSteps(t *testing.T) {
	lowers := make([]string, 23)
	for i := range lowers {
		lowers[i] = fmt.Sprintf("/layer-%d", i)
	}
	lowers[10] = lowers[9]
	lowers[14] = lowers[9]

	collapsed := dedupeLowerDirPaths(collapseConsecutiveLowerDirs(lowers))
	if len(collapsed) >= len(lowers) {
		t.Fatalf("expected dedupe to remove duplicates, got len %d", len(collapsed))
	}

	// Materializing n layers performs n-1 two-layer overlay merges.
	if steps := len(collapsed) - 1; steps != 20 {
		t.Fatalf("expected 20 materialization steps for 21 unique layers, got %d", steps)
	}
}
