package plugin

import (
	"testing"

	ct "github.com/flynn/flynn/controller/types"
)

func TestRestoreImagePrefersTarball(t *testing.T) {
	tarball := &ct.Artifact{URI: "https://example/tarball"}
	backup := &ct.ExpandedFormation{Artifacts: []*ct.Artifact{{URI: "https://example/backup"}}}
	got := RestoreImage(tarball, backup)
	if got != tarball {
		t.Fatalf("got %+v", got)
	}
}

func TestRestoreImageKeepsBackupWhenTarballMissing(t *testing.T) {
	backup := &ct.ExpandedFormation{Artifacts: []*ct.Artifact{{URI: "https://example/plugin"}}}
	got := RestoreImage(nil, backup)
	if got == nil || got.URI != "https://example/plugin" {
		t.Fatalf("got %+v", got)
	}
}

func TestRestoreImageNilWhenNeitherPresent(t *testing.T) {
	if RestoreImage(nil, nil) != nil {
		t.Fatal("expected nil")
	}
	if RestoreImage(nil, &ct.ExpandedFormation{}) != nil {
		t.Fatal("empty formation must not invent an image")
	}
}

func TestRestoreProcessesScalesDumpJob(t *testing.T) {
	p := Installed{Name: "mongodb", Backup: &BackupSpec{Process: "mongodb"}}
	got := RestoreProcesses(p, &ct.ExpandedFormation{Processes: map[string]int{"mongodb": 0, "web": 1}})
	if got["mongodb"] != 1 {
		t.Fatalf("dump process must be scaled to 1, got %v", got)
	}
	if got["web"] != 1 {
		t.Fatalf("other processes must be kept, got %v", got)
	}
	if RestoreProcesses(p, nil)["mongodb"] != 1 {
		t.Fatal("nil formation must still start the dump process")
	}
}
