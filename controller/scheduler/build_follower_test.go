package main

import (
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	. "github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/random"
)

// EDGE-34: every scheduler, leader or not, runs SyncJobs every 30 seconds
// and adopts the pending compiles it finds in the controller. When a
// follower's sync lands between POST /builds (the row is pending with no
// host) and the leader placing the build, the follower holds the build with
// an empty HostID. The host's create event then looked like a job running
// on the wrong host, and the follower stopped it. gitreceive saw the job go
// down and declined the push with "build job stopped before it was placed".

func edge34Scheduler(t *testing.T, leader bool) (*Scheduler, *FakeHostClient) {
	t.Helper()
	s := NewScheduler(nil, NewFakeControllerClient(), nil, log15.New())
	s.isLeader = &leader
	h := NewFakeHostClient("h1", false)
	s.hosts["h1"] = &Host{ID: "h1", Healthy: true, client: h}
	return s, h
}

// pendingBuildRow is the controller row for a compile gitreceive just
// created: pending, no host, no cluster job id.
func pendingBuildRow(uuid string) *ct.Job {
	return &ct.Job{
		UUID:      uuid,
		Type:      "slugbuilder",
		State:     ct.JobStatePending,
		AppID:     "app",
		ReleaseID: "rel",
		Meta:      map[string]string{ct.MetaBuildOwner: "user:a"},
	}
}

// placeBuild puts the build on h1 as the leader's AddJob does and returns
// the host's create event for it.
func placeBuild(t *testing.T, h *FakeHostClient, uuid string) *host.ActiveJob {
	t.Helper()
	jobID := "h1-" + uuid
	hj := &host.Job{
		ID: jobID,
		Metadata: map[string]string{
			"flynn-controller.app":     "app",
			"flynn-controller.release": "rel",
			"flynn-controller.type":    "slugbuilder",
		},
	}
	if err := h.AddJob(hj); err != nil {
		t.Fatal(err)
	}
	job, err := h.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

// stopRequested waits briefly for the scheduler's asynchronous StopJob.
func stopRequested(h *FakeHostClient, jobID string) bool {
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if h.IsStopped(jobID) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestFollowerDoesNotStopBuildTheLeaderPlaced(t *testing.T) {
	s, h := edge34Scheduler(t, false)
	uuid := random.UUID()

	// The follower's periodic sync sees the build before it is placed.
	s.applyControllerJobList([]*ct.Job{pendingBuildRow(uuid)})

	// The leader places it on h1; the follower gets the host's event.
	active := placeBuild(t, h, uuid)
	s.HandleJobEvent(&host.Event{Event: host.JobEventCreate, JobID: active.Job.ID, Job: active})

	if stopRequested(h, active.Job.ID) {
		t.Fatal("a follower stopped the build the leader just placed")
	}
}

func TestBuildAdoptedUnplacedTakesTheHostPlacement(t *testing.T) {
	for _, leader := range []bool{true, false} {
		s, h := edge34Scheduler(t, leader)
		uuid := random.UUID()
		// A scheduler that adopted the build before another one placed it
		// (a follower, or a leader promoted mid-placement).
		if !s.adoptControllerBuild(pendingBuildRow(uuid)) {
			t.Fatal("pending build was not adopted")
		}
		active := placeBuild(t, h, uuid)
		s.HandleJobEvent(&host.Event{Event: host.JobEventCreate, JobID: active.Job.ID, Job: active})

		if stopRequested(h, active.Job.ID) {
			t.Fatalf("leader=%v: the placed build was stopped", leader)
		}
		job := s.jobs[uuid]
		if job == nil {
			t.Fatalf("leader=%v: build no longer tracked", leader)
		}
		if job.HostID != "h1" || job.JobID != active.Job.ID {
			t.Fatalf("leader=%v: host=%q job=%q, want the host's placement h1 / %s", leader, job.HostID, job.JobID, active.Job.ID)
		}
		if job.State != JobStateStarting {
			t.Fatalf("leader=%v: state=%q, want starting", leader, job.State)
		}
	}
}

// The leader still stops a copy on a host other than the one it placed the
// job on (an AddJob that timed out but started anyway). A follower leaves
// that to the leader.
func TestOnlyLeaderStopsJobOnWrongHost(t *testing.T) {
	for _, leader := range []bool{true, false} {
		s, h := edge34Scheduler(t, leader)
		uuid := random.UUID()
		s.jobs.Add(&Job{ID: uuid, Type: "slugbuilder", AppID: "app", ReleaseID: "rel", HostID: "h2", JobID: "h2-" + uuid, State: JobStateStarting})
		active := placeBuild(t, h, uuid)
		s.HandleJobEvent(&host.Event{Event: host.JobEventCreate, JobID: active.Job.ID, Job: active})
		if got := stopRequested(h, active.Job.ID); got != leader {
			t.Fatalf("leader=%v: stop requested=%v", leader, got)
		}
	}
}

// A controller row whose job id names another host than its host_id is a
// stray placement. The leader stops it; a follower leaves it alone.
func TestOnlyLeaderStopsForeignBuildFromSync(t *testing.T) {
	for _, leader := range []bool{true, false} {
		s, h := edge34Scheduler(t, leader)
		s.ClusterClient = newTestCluster(map[string]utils.HostClient{"h1": h})
		uuid := random.UUID()
		active := placeBuild(t, h, uuid)
		row := pendingBuildRow(uuid)
		row.ID = active.Job.ID
		row.HostID = "h2"
		row.State = ct.JobStateStarting
		s.applyControllerJobList([]*ct.Job{row})
		if got := stopRequested(h, active.Job.ID); got != leader {
			t.Fatalf("leader=%v: stop requested=%v", leader, got)
		}
	}
}
