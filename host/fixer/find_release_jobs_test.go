package fixer

import (
	"testing"
	"time"

	host "github.com/randy-girard/flynn/host/types"
)

func TestRankAppReleaseJobsPrefersRunningCurrentOverNewerStale(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	releases := map[string]map[string]*host.ActiveJob{
		"stale-a2ed9855": {
			"devnode1": {
				StartedAt: now.Add(time.Hour),
				Status:    host.StatusFailed,
				Job:       &host.Job{ID: "stale-job"},
			},
		},
		"current-36b1d5d8": {
			"devnode2": {
				StartedAt: now,
				Status:    host.StatusRunning,
				Job:       &host.Job{ID: "current-job"},
			},
		},
	}
	ranked := rankAppReleaseJobs(releases)
	if len(ranked) != 2 {
		t.Fatalf("len=%d", len(ranked))
	}
	if ranked[0]["devnode2"].ID != "current-job" {
		t.Fatalf("want running current first, got %+v", ranked[0])
	}
	if ranked[1]["devnode1"].ID != "stale-job" {
		t.Fatalf("want stale second, got %+v", ranked[1])
	}
}

func TestPreferActiveJobKeepsRunningOverNewerFailedOnSameHost(t *testing.T) {
	now := time.Now()
	running := &host.ActiveJob{StartedAt: now, Status: host.StatusRunning, Job: &host.Job{ID: "run"}}
	failed := &host.ActiveJob{StartedAt: now.Add(time.Hour), Status: host.StatusFailed, Job: &host.Job{ID: "fail"}}
	if preferActiveJob(failed, running) {
		t.Fatal("failed newer job must not replace a running job")
	}
	if !preferActiveJob(running, failed) {
		t.Fatal("running job must replace a failed job")
	}
}

func TestPreferActiveJobKeepsNewerWhenBothRunning(t *testing.T) {
	now := time.Now()
	old := &host.ActiveJob{StartedAt: now, Status: host.StatusRunning, Job: &host.Job{ID: "old"}}
	newer := &host.ActiveJob{StartedAt: now.Add(time.Minute), Status: host.StatusRunning, Job: &host.Job{ID: "new"}}
	if !preferActiveJob(newer, old) {
		t.Fatal("newer running job should win")
	}
}
