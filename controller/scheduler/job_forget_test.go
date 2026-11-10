package main

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	. "github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/attempt"
)

// startReasonForScheduler is the reason the scheduler would record for the
// next job of this type. It must stay scale after the stopped object is gone.
func startReasonForScheduler(s *Scheduler, appID, releaseID, typ string) string {
	return s.jobs.startReasonSeen(appID, releaseID, typ, s.seenProcessTypes)
}

func forgetTestScheduler(t *testing.T) *Scheduler {
	t.Helper()
	logger := log15.New()
	logger.SetHandler(log15.DiscardHandler())
	s := NewScheduler(nil, NewFakeControllerClient(), nil, logger)
	return s
}

func TestSyncJobsForgetsStoppedJobsAlreadyDown(t *testing.T) {
	s := forgetTestScheduler(t)
	for i := 0; i < 40; i++ {
		id := s.generateJobUUID()
		s.jobs.Add(&Job{
			ID:        id,
			AppID:     "app-1",
			ReleaseID: "rel-1",
			Type:      "web",
			State:     JobStateStopped,
		})
	}
	// Scale-to-zero: nothing of this type is still live, and there is no
	// replacement waiting to be classified.
	pending := &Job{ID: "pending", AppID: "app-1", ReleaseID: "rel-1", Type: "worker", State: JobStatePending}
	blocked := &Job{ID: "blocked", AppID: "app-1", ReleaseID: "rel-1", Type: "clock", State: JobStateBlocked}
	stopping := &Job{ID: "stopping", AppID: "app-1", ReleaseID: "rel-1", Type: "urgent", HostID: "host-1", JobID: "host-1-stopping", State: JobStateStopping}
	s.jobs.Add(pending)
	s.jobs.Add(blocked)
	s.jobs.Add(stopping)
	hc := NewFakeHostClient("host-1", false)
	s.hosts["host-1"] = NewHost(hc, s.logger)
	hc.Jobs[stopping.JobID] = host.ActiveJob{
		Job:    &host.Job{ID: stopping.JobID},
		HostID: "host-1",
		Status: host.StatusRunning,
	}
	volJob := stopping.ID
	s.volumes = map[string]*Volume{
		"vol": {Volume: ct.Volume{ID: "vol", JobID: &volJob}},
	}
	key := utils.FormationKey{AppID: "app-1", ReleaseID: "rel-1"}
	stopped := s.jobs.Add(&Job{ID: "formed", AppID: "app-1", ReleaseID: "rel-1", Type: "web", State: JobStateStopped})
	s.formationlessJobs[key] = map[string]*Job{stopped.ID: stopped}

	if err := s.SyncJobs(); err != nil {
		t.Fatal(err)
	}
	stoppedLeft := 0
	for _, job := range s.jobs {
		if job.State == JobStateStopped {
			stoppedLeft++
		}
	}
	if stoppedLeft != 0 {
		t.Fatalf("stopped jobs still in the map: %d", stoppedLeft)
	}
	if _, ok := s.jobs["pending"]; !ok || s.jobs["pending"].State != JobStatePending {
		t.Fatal("pending job was dropped")
	}
	if _, ok := s.jobs["blocked"]; !ok || s.jobs["blocked"].State != JobStateBlocked {
		t.Fatal("blocked job was dropped")
	}
	if _, ok := s.jobs["stopping"]; !ok || s.jobs["stopping"].State != JobStateStopping {
		t.Fatal("stopping job was dropped")
	}
	if !s.volumeHeldByLiveJob(s.volumes["vol"]) {
		t.Fatal("stopping job no longer holds its volume")
	}
	if !jobHoldsReservation(stopping) {
		t.Fatal("stopping job no longer reserves CPU and memory")
	}
	if _, ok := s.formationlessJobs[key]; ok {
		t.Fatal("formationless stopped job is still pinned")
	}
	if got := startReasonForScheduler(s, "app-1", "rel-new", "web"); got != host.JobReasonScale {
		t.Fatalf("scale-to-zero next web start is %s, want scale", got)
	}
	if got := startReasonForScheduler(s, "app-1", "rel-1", "clock"); got != host.JobReasonStart {
		t.Fatalf("blocked-only type is %s, want start", got)
	}
	if got := startReasonForScheduler(s, "other-app", "rel-1", "web"); got != host.JobReasonStart {
		t.Fatalf("other app is %s, want start", got)
	}

	// A later host ListJobs replay inserts a done job again. The next sync
	// drops it, and web is still a scale.
	s.handleActiveJob(&host.ActiveJob{
		Job: &host.Job{
			ID: "host1-replayed",
			Metadata: map[string]string{
				"flynn-controller.app":     "app-1",
				"flynn-controller.release": "rel-old",
				"flynn-controller.type":    "web",
			},
		},
		HostID: "host1",
		Status: host.StatusDone,
	})
	if _, ok := s.jobs["replayed"]; !ok {
		t.Fatal("host replay did not insert the done job")
	}
	if err := s.SyncJobs(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.jobs["replayed"]; ok {
		t.Fatal("replayed stopped job still in the map")
	}
	if got := startReasonForScheduler(s, "app-1", "rel-new", "web"); got != host.JobReasonScale {
		t.Fatalf("after replay, web start is %s, want scale", got)
	}
}

func TestInFlightDownPersistIsNotSwept(t *testing.T) {
	s := forgetTestScheduler(t)
	leader := true
	s.isLeader = &leader
	job := &Job{ID: "job-1", AppID: "app-1", ReleaseID: "rel-1", Type: "web", State: JobStateStopped}
	s.jobs.Add(job)
	// Queue the down write and do not finish it. The controller does not
	// list the job yet. The object has the exit status the PUT must store.
	s.persistJob(job)
	if err := s.SyncJobs(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.jobs["job-1"]; !ok {
		t.Fatal("in-flight down PUT dropped the stopped job")
	}
}

func TestControllerStoppingRowKeepsStoppedJob(t *testing.T) {
	s := forgetTestScheduler(t)
	fake := s.ControllerClient.(*FakeControllerClient)
	s.ControllerClient = &listedActiveClient{
		FakeControllerClient: fake,
		active: []*ct.Job{{
			UUID:  "job-1",
			State: ct.JobStateStopping,
		}},
	}
	s.jobs.Add(&Job{ID: "job-1", AppID: "app-1", ReleaseID: "rel-1", Type: "web", State: JobStateStopped})
	if err := s.SyncJobs(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.jobs["job-1"]; !ok {
		t.Fatal("controller row still stopping, but the stopped job was dropped")
	}
}

type listedActiveClient struct {
	*FakeControllerClient
	active []*ct.Job
}

func (c *listedActiveClient) JobListActive() ([]*ct.Job, error) {
	return c.active, nil
}

type failPutClient struct {
	*FakeControllerClient
	puts atomic.Int32
}

func (c *failPutClient) PutJob(*ct.Job) error {
	c.puts.Add(1)
	return errors.New("controller unavailable")
}

func TestFailedDownPersistKeepsStoppedJob(t *testing.T) {
	orig := controllerPersistAttempts
	controllerPersistAttempts = attempt.Strategy{Total: time.Millisecond, Delay: time.Hour}
	t.Cleanup(func() { controllerPersistAttempts = orig })

	s := forgetTestScheduler(t)
	leader := true
	s.isLeader = &leader
	fake := s.ControllerClient.(*FakeControllerClient)
	fail := &failPutClient{FakeControllerClient: fake}
	s.ControllerClient = fail
	job := &Job{ID: "job-1", AppID: "app-1", ReleaseID: "rel-1", Type: "web", State: JobStateStopped, exitStatus: intPtr(1)}
	s.jobs.Add(job)

	go s.ControllerPersistLoop()
	t.Cleanup(func() { close(s.controllerPersist) })
	s.persistJob(job)

	deadline := time.Now().Add(2 * time.Second)
	for fail.puts.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("down PUT was not attempted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The persist goroutine reports the failure after PutJob returns.
	// SyncJobs applies that report. The job must still be tracked when
	// the controller never accepted the down row.
	deadline = time.Now().Add(2 * time.Second)
	for {
		if err := s.SyncJobs(); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.jobs["job-1"]; !ok {
			t.Fatal("failed down PUT dropped the stopped job")
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSuccessfulDownPersistForgetsStoppedJob(t *testing.T) {
	s := forgetTestScheduler(t)
	s.jobs.Add(&Job{ID: "job-1", AppID: "app-1", ReleaseID: "rel-1", Type: "web", State: JobStateStopped})
	s.trackDownPersist("job-1")
	s.finishDownPersist("job-1", nil)
	if _, ok := s.jobs["job-1"]; ok {
		t.Fatal("successful down PUT left the stopped job in the map")
	}
	if got := startReasonForScheduler(s, "app-1", "rel-2", "web"); got != host.JobReasonScale {
		t.Fatalf("next web start is %s, want scale", got)
	}
}

func TestDownPersistResultDoesNotForgetRunningJob(t *testing.T) {
	s := forgetTestScheduler(t)
	job := &Job{ID: "job-1", AppID: "app-1", ReleaseID: "rel-1", Type: "web", State: JobStateRunning}
	s.jobs.Add(job)
	s.trackDownPersist("job-1")
	s.finishDownPersist("job-1", nil)
	if got := s.jobs["job-1"]; got == nil || got.State != JobStateRunning {
		t.Fatal("down PUT result removed a job that is running again")
	}
}

func TestRestartLimitSurvivesForgettingStoppedJob(t *testing.T) {
	s, f := restartLimitTestScheduler(3, nil)
	s.ControllerClient = NewFakeControllerClient()
	leader := true
	s.isLeader = &leader
	job := crashedJob(s, f, 3)
	s.restartJob(job)
	if err := s.SyncJobs(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.jobs[job.ID]; ok {
		t.Fatal("stopped job still tracked after it was persisted down")
	}
	if n := s.restartsExhausted[f.key()]["web"]; n != 1 {
		t.Fatalf("exhausted web=%d, want 1", n)
	}
	if diff := s.formationDiff(f); diff["web"] != 0 {
		t.Fatalf("rectify would replace the stopped process: diff=%v", diff)
	}
}

type activePageErrorClient struct {
	*FakeControllerClient
}

func (activePageErrorClient) JobListActive() ([]*ct.Job, error) {
	return []*ct.Job{{
		UUID:  "page-only",
		AppID: "app-1",
		State: ct.JobStateUp,
	}}, errors.New("active jobs page 2 failed")
}

// TestSyncJobsActivePageErrorDoesNotApplyPartial is the partial-read rule:
// a later page failure is an error, and SyncJobs must not treat the first
// page as the full set. Unknown rows in that page would be marked down.
func TestSyncJobsActivePageErrorDoesNotApplyPartial(t *testing.T) {
	s := forgetTestScheduler(t)
	leader := true
	s.isLeader = &leader
	s.controllerPersist = make(chan interface{}, 8)
	s.ControllerClient = activePageErrorClient{FakeControllerClient: NewFakeControllerClient()}
	s.jobs.Add(&Job{
		ID:        "keep",
		AppID:     "app-1",
		ReleaseID: "rel-1",
		Type:      "web",
		State:     JobStatePending,
	})
	err := s.SyncJobs()
	if err == nil || !strings.Contains(err.Error(), "page 2") {
		t.Fatalf("err = %v", err)
	}
	if got := s.jobs["keep"]; got == nil || got.State != JobStatePending {
		t.Fatalf("in-memory job = %#v", got)
	}
	select {
	case got := <-s.controllerPersist:
		t.Fatalf("applied a partial active-job page: %#v", got)
	default:
	}
}

func intPtr(n int) *int { return &n }
