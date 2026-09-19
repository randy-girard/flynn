package main

import (
	"testing"

	host "github.com/randy-girard/flynn/host/types"
)

func TestJobsStartReason(t *testing.T) {
	jobs := Jobs{}
	if got := jobs.startReason("app", "rel-new", "web"); got != host.JobReasonStart {
		t.Fatalf("empty: %s", got)
	}
	jobs.Add(&Job{ID: "a", AppID: "app", ReleaseID: "rel-new", Type: "web", State: JobStateRunning})
	if got := jobs.startReason("app", "rel-new", "web"); got != host.JobReasonScale {
		t.Fatalf("same release running: %s", got)
	}
	jobs = Jobs{}
	jobs.Add(&Job{ID: "b", AppID: "app", ReleaseID: "rel-old", Type: "web", State: JobStateRunning})
	if got := jobs.startReason("app", "rel-new", "web"); got != host.JobReasonReplace {
		t.Fatalf("other release running: %s", got)
	}
	jobs.Add(&Job{ID: "c", AppID: "app", ReleaseID: "rel-old", Type: "web", State: JobStateStopped})
	if got := jobs.startReason("app", "rel-new", "worker"); got != host.JobReasonStart {
		t.Fatalf("other type: %s", got)
	}
}
