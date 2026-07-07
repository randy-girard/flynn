package fixer

import (
	"io/fs"
	"sort"
	"testing"

	host "github.com/flynn/flynn/host/types"
)

// fakeDirEntry is a minimal os.DirEntry for testing selection helpers.
type fakeDirEntry struct {
	name string
	dir  bool
}

func (f fakeDirEntry) Name() string               { return f.name }
func (f fakeDirEntry) IsDir() bool                { return f.dir }
func (f fakeDirEntry) Type() fs.FileMode          { return 0 }
func (f fakeDirEntry) Info() (fs.FileInfo, error) { return nil, nil }

func TestKeepJobIDs(t *testing.T) {
	jobs := map[string]host.ActiveJob{
		"running":  {Status: host.StatusRunning},
		"starting": {Status: host.StatusStarting},
		"done":     {Status: host.StatusDone},
		"crashed":  {Status: host.StatusCrashed},
	}
	keep := keepJobIDs(jobs)
	if _, ok := keep["running"]; !ok {
		t.Fatal("running job should be kept")
	}
	if _, ok := keep["starting"]; !ok {
		t.Fatal("starting job should be kept")
	}
	if _, ok := keep["done"]; ok {
		t.Fatal("done job should not be kept")
	}
	if _, ok := keep["crashed"]; ok {
		t.Fatal("crashed job should not be kept")
	}
}

func TestOrphanImageDirs(t *testing.T) {
	entries := []fs.DirEntry{
		fakeDirEntry{name: "keep-me", dir: true},
		fakeDirEntry{name: "orphan-1", dir: true},
		fakeDirEntry{name: "orphan-2", dir: true},
		fakeDirEntry{name: "a-file", dir: false}, // ignored: not a dir
	}
	keep := map[string]struct{}{"keep-me": {}}
	got := orphanImageDirs(entries, keep)
	sort.Strings(got)
	want := []string{"orphan-1", "orphan-2"}
	if len(got) != len(want) {
		t.Fatalf("orphanImageDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("orphanImageDirs = %v, want %v", got, want)
		}
	}
}

func TestUnreferencedLayerIDs(t *testing.T) {
	entries := []fs.DirEntry{
		fakeDirEntry{name: "referenced.squashfs"},
		fakeDirEntry{name: "orphan.squashfs"},
		fakeDirEntry{name: "referenced.json"}, // ignored: not .squashfs
		fakeDirEntry{name: "notes.txt"},       // ignored
	}
	keep := map[string]struct{}{"referenced": {}}
	got := unreferencedLayerIDs(entries, keep)
	if len(got) != 1 || got[0] != "orphan" {
		t.Fatalf("unreferencedLayerIDs = %v, want [orphan]", got)
	}
}
