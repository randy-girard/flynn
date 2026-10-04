package types

import (
	"reflect"
	"testing"
	"time"
)

func TestRunJobArgs(t *testing.T) {
	slug := &Release{Meta: map[string]string{"git": "true", "slugrunner.stack": "heroku-24"}}
	container := &Release{Meta: map[string]string{"git": "true", "slugrunner.stack": "container"}}
	got := slug.RunJobArgs([]string{"/bin/sh"})
	want := []string{"/runner/init", "/bin/sh"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("slug: got %#v, want %#v", got, want)
	}
	if got := container.RunJobArgs([]string{"/bin/sh"}); !reflect.DeepEqual(got, []string{"/bin/sh"}) {
		t.Fatalf("container: got %#v", got)
	}
}

func TestHasDeployableBlob(t *testing.T) {
	now := time.Now()
	git := &Release{
		ID:          "r1",
		ArtifactIDs: []string{"a1"},
		Meta:        map[string]string{"git": "true"},
		CreatedAt:   &now,
	}
	git.StampBlobAvailable()
	if !git.BlobAvailable {
		t.Fatal("git with artifacts should be available")
	}
	reaped := &Release{
		ID:   "r2",
		Meta: map[string]string{"git": "true", MetaGCBlobReaped: "true"},
	}
	reaped.StampBlobAvailable()
	if reaped.BlobAvailable {
		t.Fatal("reaped git should not be available")
	}
	emptyGit := &Release{Meta: map[string]string{"git": "true"}}
	emptyGit.StampBlobAvailable()
	if emptyGit.BlobAvailable {
		t.Fatal("git with no artifacts should not be available")
	}
}
