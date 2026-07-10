package cleanup

import (
	"io/fs"
	"sort"
	"testing"

	host "github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/host/volume"
)

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
		fakeDirEntry{name: "node1-keep-me", dir: true},
		fakeDirEntry{name: "node1-orphan-1", dir: true},
		fakeDirEntry{name: "node1-orphan-2", dir: true},
		fakeDirEntry{name: "node2-other-host", dir: true},
		fakeDirEntry{name: "a-file", dir: false},
	}
	keep := map[string]struct{}{"node1-keep-me": {}}
	got := orphanImageDirs(entries, keep, "node1")
	sort.Strings(got)
	want := []string{"node1-orphan-1", "node1-orphan-2"}
	if len(got) != len(want) {
		t.Fatalf("orphanImageDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("orphanImageDirs = %v, want %v", got, want)
		}
	}
}

func TestOrphanImageDirsSkipsOtherHosts(t *testing.T) {
	entries := []fs.DirEntry{
		fakeDirEntry{name: "node1-keep", dir: true},
		fakeDirEntry{name: "node2-remote", dir: true},
		fakeDirEntry{name: "node1-orphan", dir: true},
	}
	keep := map[string]struct{}{"node1-keep": {}}
	got := orphanImageDirs(entries, keep, "node1")
	if len(got) != 1 || got[0] != "node1-orphan" {
		t.Fatalf("orphanImageDirs = %v, want [node1-orphan]", got)
	}
}

func TestKeepLayerIDsIncludesMountspecs(t *testing.T) {
	jobs := map[string]host.ActiveJob{
		"node1-abc": {
			Status: host.StatusRunning,
			Job: &host.Job{
				Mountspecs: []*host.Mountspec{
					{Type: host.MountspecTypeSquashfs, ID: "layer-a"},
					{Type: host.MountspecTypeSquashfs, ID: "layer-b"},
				},
			},
		},
		"node1-done": {
			Status: host.StatusDone,
			Job: &host.Job{
				Mountspecs: []*host.Mountspec{
					{Type: host.MountspecTypeSquashfs, ID: "layer-stale"},
				},
			},
		},
	}
	vols := []*volume.Info{
		{ID: "layer-zvol", Type: volume.VolumeTypeSquashfs},
		{ID: "data-vol", Type: volume.VolumeTypeData},
	}
	keep := keepLayerIDs(jobs, vols)
	for _, id := range []string{"layer-a", "layer-b", "layer-zvol"} {
		if _, ok := keep[id]; !ok {
			t.Fatalf("keepLayerIDs missing %q: %v", id, keep)
		}
	}
	if _, ok := keep["layer-stale"]; ok {
		t.Fatal("stopped job mountspec layer should not be kept")
	}
	if _, ok := keep["data-vol"]; ok {
		t.Fatal("data volume ID should not be kept for layer-cache")
	}
}

func TestKeepLayerIDsIncludesFailedJobs(t *testing.T) {
	jobs := map[string]host.ActiveJob{
		"node1-failed": {
			Status: host.StatusFailed,
			Job: &host.Job{
				Mountspecs: []*host.Mountspec{
					{Type: host.MountspecTypeSquashfs, ID: "layer-retry"},
				},
			},
		},
	}
	keep := keepLayerIDs(jobs, nil)
	if _, ok := keep["layer-retry"]; !ok {
		t.Fatal("failed job mountspec layer should be kept for retry")
	}
}

func TestUnreferencedLayerIDs(t *testing.T) {
	entries := []fs.DirEntry{
		fakeDirEntry{name: "referenced.squashfs"},
		fakeDirEntry{name: "orphan.squashfs"},
		fakeDirEntry{name: "referenced.json"},
		fakeDirEntry{name: "notes.txt"},
	}
	keep := map[string]struct{}{"referenced": {}}
	got := unreferencedLayerIDs(entries, keep)
	if len(got) != 1 || got[0] != "orphan" {
		t.Fatalf("unreferencedLayerIDs = %v, want [orphan]", got)
	}
}
