package app_garbage_collection

import (
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

func ts(hoursAgo int) *time.Time {
	t := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC).Add(-time.Duration(hoursAgo) * time.Hour)
	return &t
}

func TestSelectReapReleaseIDs(t *testing.T) {
	uri := func(rel *ct.Release) (string, bool) {
		if rel.Meta["uri"] == "" {
			return "", false
		}
		return rel.Meta["uri"], true
	}
	releases := []*ct.Release{
		{ID: "current", Meta: map[string]string{"uri": "slug://a"}, CreatedAt: ts(1)},
		{ID: "keep1", Meta: map[string]string{"uri": "slug://b"}, CreatedAt: ts(2)},
		{ID: "keep2", Meta: map[string]string{"uri": "slug://c"}, CreatedAt: ts(3)},
		{ID: "old-git", Meta: map[string]string{"uri": "slug://d"}, CreatedAt: ts(4)},
		{ID: "old-docker", Meta: map[string]string{"uri": "docker://e"}, CreatedAt: ts(5)},
		{ID: "env-only", Meta: map[string]string{}, CreatedAt: ts(6)},
	}
	formations := []*ct.Formation{
		{ReleaseID: "current", Processes: map[string]int{"web": 1}},
	}

	got := SelectReapReleaseIDs(releases, formations, 2, 0, time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC), uri)
	want := []string{"old-git", "old-docker"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSelectReapReleaseIDsKeepZero(t *testing.T) {
	uri := func(rel *ct.Release) (string, bool) { return rel.Meta["uri"], true }
	releases := []*ct.Release{
		{ID: "live", Meta: map[string]string{"uri": "slug://a"}},
		{ID: "prev", Meta: map[string]string{"uri": "slug://b"}},
	}
	formations := []*ct.Formation{{ReleaseID: "live", Processes: map[string]int{"web": 2}}}
	got := SelectReapReleaseIDs(releases, formations, 0, 0, time.Now(), uri)
	if len(got) != 1 || got[0] != "prev" {
		t.Fatalf("got %v, want [prev]", got)
	}
}

func TestSelectReapReleaseIDsMaxAge(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	uri := func(rel *ct.Release) (string, bool) { return rel.Meta["uri"], true }
	releases := []*ct.Release{
		{ID: "live", Meta: map[string]string{"uri": "slug://a"}, CreatedAt: ts(1)},
		{ID: "recent", Meta: map[string]string{"uri": "slug://b"}, CreatedAt: ts(2)},
		{ID: "ancient", Meta: map[string]string{"uri": "slug://c"}, CreatedAt: ts(48)},
	}
	formations := []*ct.Formation{{ReleaseID: "live", Processes: map[string]int{"web": 1}}}
	got := SelectReapReleaseIDs(releases, formations, 10, 24*time.Hour, now, uri)
	if len(got) != 1 || got[0] != "ancient" {
		t.Fatalf("got %v, want [ancient]", got)
	}
}

func TestSelectReapSkipsAlreadyReaped(t *testing.T) {
	uri := func(rel *ct.Release) (string, bool) { return rel.Meta["uri"], true }
	releases := []*ct.Release{
		{ID: "live", Meta: map[string]string{"uri": "slug://a"}},
		{ID: "reaped", Meta: map[string]string{"uri": "slug://b", ct.MetaGCBlobReaped: "true"}},
		{ID: "prev", Meta: map[string]string{"uri": "slug://c"}},
	}
	formations := []*ct.Formation{{ReleaseID: "live", Processes: map[string]int{"web": 1}}}
	got := SelectReapReleaseIDs(releases, formations, 1, 0, time.Now(), uri)
	if len(got) != 0 {
		t.Fatalf("got %v, want none (prev is within keep)", got)
	}
}

func TestBlobstoreAppURISkipsSlugrunner(t *testing.T) {
	rel := &ct.Release{ArtifactIDs: []string{"runner", "slug"}}
	arts := map[string]*ct.Artifact{
		"runner": {ID: "runner", URI: "http://blobstore.discoverd/runner", Meta: map[string]string{"blobstore": "true", "flynn.component": "slugrunner"}},
		"slug":   {ID: "slug", URI: "http://blobstore.discoverd/slug", Meta: map[string]string{"blobstore": "true"}},
	}
	got, ok := BlobstoreAppURI(rel, arts)
	if !ok || got != "http://blobstore.discoverd/slug" {
		t.Fatalf("got %q %v, want slug URI", got, ok)
	}
}
