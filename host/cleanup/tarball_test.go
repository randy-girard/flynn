package cleanup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveStaleTarballExtractDirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	keep := filepath.Join(tmp, tarballExtractPrefix+"current")
	stale := filepath.Join(tmp, tarballExtractPrefix+"old")
	for _, d := range []string{keep, stale} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	if err := RemoveStaleTarballExtractDirs(keep); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("keep dir removed: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale dir still exists: %v", err)
	}
}

func TestFSFreeBytes(t *testing.T) {
	free, err := FSFreeBytes(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if free == 0 {
		t.Fatal("expected non-zero free space")
	}
}
