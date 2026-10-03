package controller

import (
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestHeadReleaseIDPrefersNewestUnfinishedDeploy(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-time.Minute)
	deps := []*ct.Deployment{
		{NewReleaseID: "queued", CreatedAt: &now},
		{NewReleaseID: "running", CreatedAt: &earlier},
		{NewReleaseID: "done", FinishedAt: &now, CreatedAt: &earlier},
	}
	if got := HeadReleaseID("live", deps); got != "queued" {
		t.Fatalf("got %q", got)
	}
	if got := HeadReleaseID("live", []*ct.Deployment{{NewReleaseID: "done", FinishedAt: &now}}); got != "live" {
		t.Fatalf("live fallback got %q", got)
	}
}
