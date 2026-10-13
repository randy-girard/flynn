package main

import (
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

type restartEventClient struct {
	*testutils.FakeControllerClient
	events chan *ct.JobRestartEvent
}

func (c *restartEventClient) CreateJobRestartEvent(appID string, ev *ct.JobRestartEvent) error {
	c.events <- ev
	return nil
}

// ageTestScheduler returns a leader scheduler with a 24h interval and one
// formation per entry in apps (app id -> web process count) whose jobs are all
// running and "age" old.
func ageTestScheduler(apps map[string]int, age time.Duration) (*Scheduler, *restartEventClient) {
	logger := log15.New()
	logger.SetHandler(log15.DiscardHandler())
	s := NewScheduler(nil, nil, nil, logger)
	s.isLeader = typeconv.BoolPtr(true)
	s.jobRestartInterval = 24 * time.Hour
	cc := &restartEventClient{events: make(chan *ct.JobRestartEvent, 16)}
	s.ControllerClient = cc
	s.hosts["h1"] = NewHost(testutils.NewFakeHostClient("h1", false), logger)

	for appID, n := range apps {
		f := NewFormation(&ct.ExpandedFormation{
			App:       &ct.App{ID: appID, Name: appID + "-name"},
			Release:   &ct.Release{ID: "rel-" + appID},
			Processes: map[string]int{"web": n},
		})
		s.formations.Add(f)
		for i := 0; i < n; i++ {
			// older jobs get a smaller index so ordering is deterministic
			addAgedJob(s, f, appID, i, age+time.Duration(n-i)*time.Minute)
		}
	}
	return s, cc
}

func addAgedJob(s *Scheduler, f *Formation, appID string, i int, age time.Duration) *Job {
	id := appID + "-job-" + string(rune('a'+i))
	job := &Job{
		ID:        id,
		JobID:     "h1-" + id,
		HostID:    "h1",
		Type:      "web",
		AppID:     appID,
		ReleaseID: f.Release.ID,
		Formation: f,
		State:     JobStateRunning,
		StartedAt: time.Now().Add(-age),
	}
	s.jobs.Add(job)
	return job
}

func countState(s *Scheduler, appID string, state JobState) int {
	n := 0
	for _, job := range s.jobs {
		if job.AppID == appID && job.State == state {
			n++
		}
	}
	return n
}

func TestRestartAgedJobsPicksOldestAndEmitsEvent(t *testing.T) {
	s, cc := ageTestScheduler(map[string]int{"app-1": 3}, 25*time.Hour)
	s.restartAgedJobs(time.Now())

	if n := countState(s, "app-1", JobStateStopping); n != 1 {
		t.Fatalf("stopping=%d, want exactly 1", n)
	}
	// "a" has the largest age (n-i is biggest for i=0)
	if s.jobs["app-1-job-a"].State != JobStateStopping || !s.jobs["app-1-job-a"].plannedRestart {
		t.Fatalf("oldest job not marked for planned restart: %+v", s.jobs["app-1-job-a"])
	}
	select {
	case ev := <-cc.events:
		if ev.AppID != "app-1" || ev.ProcessType != "web" || ev.JobID != "h1-app-1-job-a" || ev.Reason != ct.JobRestartReasonMaxAge || ev.AgeSeconds < int64((25*time.Hour).Seconds()) || ev.Age == "" || ev.AppName != "app-1-name" {
			t.Fatalf("unexpected event %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no job restart event emitted")
	}
}

func TestRestartAgedJobsSkipsYoungJobs(t *testing.T) {
	s, cc := ageTestScheduler(map[string]int{"app-1": 3}, time.Hour)
	s.restartAgedJobs(time.Now())
	if n := countState(s, "app-1", JobStateStopping); n != 0 {
		t.Fatalf("stopping=%d, want 0 for jobs younger than the interval", n)
	}
	if len(cc.events) != 0 {
		t.Fatal("event emitted for a job younger than the interval")
	}
}

func TestRestartAgedJobsDisabled(t *testing.T) {
	s, cc := ageTestScheduler(map[string]int{"app-1": 3}, 100*time.Hour)
	s.setJobRestartInterval(0)
	s.restartAgedJobs(time.Now())
	if n := countState(s, "app-1", JobStateStopping); n != 0 || len(cc.events) != 0 {
		t.Fatalf("restarted with the setting off: stopping=%d events=%d", n, len(cc.events))
	}
}

func TestRestartAgedJobsNotLeader(t *testing.T) {
	s, _ := ageTestScheduler(map[string]int{"app-1": 3}, 100*time.Hour)
	s.isLeader = typeconv.BoolPtr(false)
	s.restartAgedJobs(time.Now())
	if n := countState(s, "app-1", JobStateStopping); n != 0 {
		t.Fatalf("follower scheduler restarted %d jobs", n)
	}
}

func TestRestartAgedJobsOneAtATimePerApp(t *testing.T) {
	s, _ := ageTestScheduler(map[string]int{"app-1": 3}, 25*time.Hour)
	s.restartAgedJobs(time.Now())
	s.restartAgedJobs(time.Now())
	if n := countState(s, "app-1", JobStateStopping); n != 1 {
		t.Fatalf("stopping=%d after two passes, want 1 while the first is in flight", n)
	}

	// the old job stopped and its replacement is pending: still not settled
	s.jobs["app-1-job-a"].State = JobStateStopped
	s.jobs["replacement"] = &Job{ID: "replacement", AppID: "app-1", Type: "web", ReleaseID: "rel-app-1", Formation: s.jobs["app-1-job-b"].Formation, State: JobStatePending}
	s.restartAgedJobs(time.Now())
	if n := countState(s, "app-1", JobStateStopping); n != 0 {
		t.Fatalf("restarted another job while the replacement was pending (stopping=%d)", n)
	}

	// replacement is running: the next aged job may go
	s.jobs["replacement"].State = JobStateRunning
	s.jobs["replacement"].StartedAt = time.Now()
	s.restartAgedJobs(time.Now())
	if n := countState(s, "app-1", JobStateStopping); n != 1 {
		t.Fatalf("stopping=%d once the formation was back at full count, want 1", n)
	}
	if s.jobs["app-1-job-b"].State != JobStateStopping {
		t.Fatal("expected the next oldest job to be restarted")
	}
}

func TestRestartAgedJobsSkipsSingleInstance(t *testing.T) {
	s, cc := ageTestScheduler(map[string]int{"app-1": 1}, 100*time.Hour)
	s.restartAgedJobs(time.Now())
	if n := countState(s, "app-1", JobStateStopping); n != 0 || len(cc.events) != 0 {
		t.Fatalf("restarted a single instance process (would cause downtime): stopping=%d", n)
	}
}

func TestRestartAgedJobsWaitsForFullCount(t *testing.T) {
	s, _ := ageTestScheduler(map[string]int{"app-1": 3}, 25*time.Hour)
	// one instance is missing from the cluster (e.g. crashed, not replaced yet)
	s.jobs["app-1-job-c"].State = JobStateStopped
	s.restartAgedJobs(time.Now())
	if n := countState(s, "app-1", JobStateStopping); n != 0 {
		t.Fatalf("restarted a job while the formation was below its full count (stopping=%d)", n)
	}
}

func TestRestartAgedJobsStaggersAppsAndCapsPerPass(t *testing.T) {
	apps := map[string]int{}
	for _, id := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7"} {
		apps[id] = 2
	}
	s, _ := ageTestScheduler(apps, 25*time.Hour)
	s.restartAgedJobs(time.Now())
	stopping := 0
	for id := range apps {
		n := countState(s, id, JobStateStopping)
		if n > 1 {
			t.Fatalf("app %s has %d jobs restarting at once", id, n)
		}
		stopping += n
	}
	if stopping != maxAgeRestartsPerSync {
		t.Fatalf("restarted %d jobs in one pass, want cap of %d", stopping, maxAgeRestartsPerSync)
	}
}

func TestPlannedRestartDoesNotCountAsCrash(t *testing.T) {
	s, f := restartLimitTestScheduler(1, nil)
	job := crashedJob(s, f, 7)
	job.plannedRestart = true
	s.restartJob(job)

	if n := s.restartsExhausted[f.key()]["web"]; n != 0 {
		t.Fatalf("planned restart counted against the restart limit: exhausted=%d", n)
	}
	var replacement *Job
	for _, j := range s.jobs {
		if j.ID != job.ID {
			replacement = j
		}
	}
	if replacement == nil {
		t.Fatal("no replacement job scheduled")
	}
	defer replacement.restartTimer.Stop()
	if replacement.Restarts != 0 || replacement.RunAt == nil || replacement.RunAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("replacement should start immediately without crash accounting: restarts=%d runAt=%v", replacement.Restarts, replacement.RunAt)
	}
}
