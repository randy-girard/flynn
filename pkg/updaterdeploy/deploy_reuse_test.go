package updaterdeploy

import (
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestReusableUpdateReleasePicksNewestMatchingImage(t *testing.T) {
	t1 := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	older := &ct.Release{ID: "r1", ArtifactIDs: []string{"img"}, CreatedAt: &t1}
	newer := &ct.Release{ID: "r2", ArtifactIDs: []string{"img"}, CreatedAt: &t2}
	got := ReusableUpdateRelease("current", "img", []*ct.Release{
		{ID: "current", ArtifactIDs: []string{"old"}},
		older,
		{ID: "other", ArtifactIDs: []string{"other"}},
		newer,
		nil,
	})
	if got == nil || got.ID != "r2" {
		t.Fatalf("got %#v, want r2", got)
	}
}

func TestReusableUpdateReleaseSkipsCurrentAndEmpty(t *testing.T) {
	if ReusableUpdateRelease("cur", "", []*ct.Release{{ID: "r", ArtifactIDs: []string{"img"}}}) != nil {
		t.Fatal("empty image id")
	}
	if ReusableUpdateRelease("cur", "img", []*ct.Release{{ID: "cur", ArtifactIDs: []string{"img"}}}) != nil {
		t.Fatal("current release is not reusable")
	}
	if ReusableUpdateRelease("cur", "img", nil) != nil {
		t.Fatal("nil list")
	}
}
