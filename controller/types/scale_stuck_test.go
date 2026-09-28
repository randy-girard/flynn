package types

import (
	"strings"
	"testing"
	"time"
)

func TestShouldProbeScaleStall(t *testing.T) {
	if ShouldProbeScaleStall(DefaultScaleTimeout) {
		t.Fatal("CLI DefaultScaleTimeout must not enable stall probes")
	}
	if ShouldProbeScaleStall(time.Duration(DefaultDeployTimeout) * time.Second) {
		t.Fatal("DefaultDeployTimeout must not enable stall probes")
	}
	if !ShouldProbeScaleStall(5 * time.Minute) {
		t.Fatal("plugin/bootstrap 5m waits must probe stuck starts")
	}
	if !ShouldProbeScaleStall(10 * time.Minute) {
		t.Fatal("system-app 10m ceiling must probe stuck starts")
	}
}

func TestScaleStartingStuckTimeoutIsNotADeployTimeout(t *testing.T) {
	ceiling := 10 * time.Minute
	if ScaleStartingStuckTimeout >= ceiling {
		t.Fatalf("ScaleStartingStuckTimeout=%s must be far below the 10m system deploy ceiling", ScaleStartingStuckTimeout)
	}
	if ScaleStartingStuckTimeout > time.Minute {
		t.Fatalf("ScaleStartingStuckTimeout=%s should fail in tens of seconds, not minutes", ScaleStartingStuckTimeout)
	}
	if ScaleJobListProbeTimeout >= ScaleStartingStuckTimeout {
		t.Fatalf("JobList probe %s must be shorter than stuck-starting %s", ScaleJobListProbeTimeout, ScaleStartingStuckTimeout)
	}
}

func TestErrJobsStuckStartingIgnoresPending(t *testing.T) {
	now := time.Now()
	seen := map[string]time.Time{}
	pending := &Job{
		UUID:      "pending-uuid",
		ReleaseID: "rel-1",
		Type:      "web",
		State:     JobStatePending,
	}
	if err := ErrJobsStuckStarting([]*Job{pending}, "rel-1", now.Add(time.Hour), seen); err != nil {
		t.Fatalf("pending jobs must wait for placement, got %v", err)
	}
}

func TestErrJobsStuckStartingFailsAfterTimeout(t *testing.T) {
	now := time.Now()
	seen := map[string]time.Time{}
	job := &Job{
		ID:        "node1-abc",
		UUID:      "abc",
		HostID:    "node1",
		ReleaseID: "rel-1",
		Type:      "app",
		State:     JobStateStarting,
	}
	if err := ErrJobsStuckStarting([]*Job{job}, "rel-1", now, seen); err != nil {
		t.Fatalf("first observation must start the clock, got %v", err)
	}
	if err := ErrJobsStuckStarting([]*Job{job}, "rel-1", now.Add(ScaleStartingStuckTimeout-time.Second), seen); err != nil {
		t.Fatalf("still inside the stuck window, got %v", err)
	}
	err := ErrJobsStuckStarting([]*Job{job}, "rel-1", now.Add(ScaleStartingStuckTimeout+time.Second), seen)
	if err == nil {
		t.Fatal("host-accepted starting job must fail fast")
	}
	if !strings.Contains(err.Error(), "jobs stuck in starting") {
		t.Fatalf("error %q must name stuck starting", err)
	}
	if !strings.Contains(err.Error(), "node1-abc") {
		t.Fatalf("error %q must list the stuck job", err)
	}
}

func TestErrJobsStuckStartingDoesNotUseCreatedAt(t *testing.T) {
	now := time.Now()
	seen := map[string]time.Time{}
	created := now.Add(-20 * time.Minute)
	job := &Job{
		ID:        "node1-abc",
		UUID:      "abc",
		HostID:    "node1",
		ReleaseID: "rel-1",
		Type:      "app",
		State:     JobStateStarting,
		CreatedAt: &created,
	}
	if err := ErrJobsStuckStarting([]*Job{job}, "rel-1", now, seen); err != nil {
		t.Fatalf("CreatedAt includes pending time; first starting observation must not fail, got %v", err)
	}
}

func TestErrJobsStuckStartingIgnoresOtherReleasesAndUpJobs(t *testing.T) {
	now := time.Now()
	seen := map[string]time.Time{
		"node1-old": now.Add(-time.Hour),
		"node1-up":  now.Add(-time.Hour),
	}
	jobs := []*Job{
		{ID: "node1-old", UUID: "old", HostID: "node1", ReleaseID: "rel-old", Type: "app", State: JobStateStarting},
		{ID: "node1-up", UUID: "up", HostID: "node1", ReleaseID: "rel-1", Type: "app", State: JobStateUp},
	}
	if err := ErrJobsStuckStarting(jobs, "rel-1", now, seen); err != nil {
		t.Fatalf("up jobs and other releases must not fail the scale, got %v", err)
	}
	if _, ok := seen["node1-old"]; ok {
		t.Fatal("seen entries for jobs not in the current starting set must be pruned")
	}
}

func TestErrJobsStuckStartingClearsClockWhenJobLeavesStarting(t *testing.T) {
	now := time.Now()
	seen := map[string]time.Time{}
	job := &Job{
		ID:        "node1-abc",
		UUID:      "abc",
		HostID:    "node1",
		ReleaseID: "rel-1",
		Type:      "app",
		State:     JobStateStarting,
	}
	if err := ErrJobsStuckStarting([]*Job{job}, "rel-1", now, seen); err != nil {
		t.Fatal(err)
	}
	up := *job
	up.State = JobStateUp
	if err := ErrJobsStuckStarting([]*Job{&up}, "rel-1", now.Add(time.Minute), seen); err != nil {
		t.Fatal(err)
	}
	if _, ok := seen["node1-abc"]; ok {
		t.Fatal("clock must reset when the job leaves starting")
	}
}

func TestErrScaleJobListHung(t *testing.T) {
	err := ErrScaleJobListHung(ScaleJobListProbeTimeout)
	if err == nil || !strings.Contains(err.Error(), "JobList did not return") {
		t.Fatalf("got %v", err)
	}
}
