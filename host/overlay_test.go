package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// TestOverlayLowerdirDirectStack verifies that a modest number of layers is
// stacked directly via a native overlayfs lowerdir (colon-separated join) with
// no materialization/copying. This is the fast path that avoids the per-layer
// recursive copy regression.
func TestOverlayLowerdirDirectStack(t *testing.T) {
	lowers := make([]string, maxDirectOverlayLayers)
	for i := range lowers {
		lowers[i] = fmt.Sprintf("/layer-%d", i)
	}
	got, err := overlayLowerdir(lowers, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(lowers, ":")
	if got != want {
		t.Fatalf("expected direct lowerdir join for %d layers\n got: %q\nwant: %q", len(lowers), got, want)
	}
}

// TestOverlayLowerdirDirectStackAfterDedupe verifies the threshold is applied to
// the deduplicated layer count: a list with duplicates that collapses to within
// the direct-stack limit still uses the fast direct-join path.
func TestOverlayLowerdirDirectStackAfterDedupe(t *testing.T) {
	lowers := make([]string, maxDirectOverlayLayers+4)
	for i := range lowers {
		lowers[i] = fmt.Sprintf("/layer-%d", i%maxDirectOverlayLayers)
	}
	got, err := overlayLowerdir(lowers, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, ".materialized") {
		t.Fatalf("expected direct lowerdir join after dedupe, got materialized path %q", got)
	}
	if want := len(strings.Split(got, ":")); want != maxDirectOverlayLayers {
		t.Fatalf("expected %d deduped layers in lowerdir, got %d (%q)", maxDirectOverlayLayers, want, got)
	}
}

// TestOverlayLowerdirMaterializesDeepStack verifies that once the distinct layer
// count exceeds the direct-stack threshold, overlayLowerdir takes the
// materialization fallback instead of returning a plain colon-joined string.
// The materialize path first copies the base layer, then attempts an overlay
// mount; the mount is expected to fail under the unit-test environment, which
// confirms the fallback branch was taken (a direct join would have returned a
// ":"-joined string with no error).
func TestOverlayLowerdirMaterializesDeepStack(t *testing.T) {
	scratch := t.TempDir()
	lowers := make([]string, maxDirectOverlayLayers+1)
	for i := range lowers {
		dir := filepath.Join(scratch, fmt.Sprintf("src-%d", i))
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		lowers[i] = dir
	}
	got, err := overlayLowerdir(lowers, filepath.Join(scratch, "work"))
	if err == nil {
		t.Fatalf("expected materialization to be attempted (and mount to fail) for %d layers, got lowerdir %q", len(lowers), got)
	}
	if strings.Contains(err.Error(), "no overlay layers") {
		t.Fatalf("unexpected dedupe error: %v", err)
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
