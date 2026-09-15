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
