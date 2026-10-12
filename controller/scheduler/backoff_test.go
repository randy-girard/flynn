package main

import (
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
)

func restartLimitTestScheduler(maxRestarts int, meta map[string]string) (*Scheduler, *Formation) {
	logger := log15.New()
	logger.SetHandler(log15.DiscardHandler())
	s := NewScheduler(nil, nil, nil, logger)
	s.maxRestarts = maxRestarts
	f := NewFormation(&ct.ExpandedFormation{
		App:       &ct.App{ID: "app-1", Meta: meta},
		Release:   &ct.Release{ID: "release-1"},
		Processes: map[string]int{"web": 1},
	})
	s.formations.Add(f)
	return s, f
}

func crashedJob(s *Scheduler, f *Formation, restarts uint) *Job {
	job := &Job{ID: s.generateJobUUID(), Type: "web", AppID: "app-1", ReleaseID: "release-1", Formation: f, State: JobStateStopped, Restarts: restarts, StartedAt: time.Now()}
	s.jobs.Add(job)
	return job
}

func TestRestartJobStopsAtClusterLimit(t *testing.T) {
	s, f := restartLimitTestScheduler(3, nil)
	s.restartJob(crashedJob(s, f, 3))
	if len(s.jobs) != 1 {
		t.Fatalf("scheduled a restart past the limit: %d jobs", len(s.jobs))
	}
	if n := s.restartsExhausted[f.key()]["web"]; n != 1 {
		t.Fatalf("exhausted web=%d, want 1", n)
	}
	if diff := s.formationDiff(f); diff["web"] != 0 {
		t.Fatalf("rectify would replace the stopped process: diff=%v", diff)
	}
}

func TestRestartJobBelowClusterLimit(t *testing.T) {
	s, f := restartLimitTestScheduler(10, nil)
	s.restartJob(crashedJob(s, f, 6))
	if len(s.jobs) != 2 {
		t.Fatalf("restart not scheduled: %d jobs", len(s.jobs))
	}
	for _, j := range s.jobs {
		if j.restartTimer != nil {
			j.restartTimer.Stop()
			if j.Restarts != 7 {
				t.Fatalf("restarts=%d, want 7", j.Restarts)
			}
		}
	}
}

func TestRestartJobUnlimitedAndSystemApps(t *testing.T) {
	s, f := restartLimitTestScheduler(0, nil)
	if s.restartLimitReached(crashedJob(s, f, 1000), 1000) {
		t.Fatal("unlimited (0) stopped restarting")
	}
	s, f = restartLimitTestScheduler(1, map[string]string{"flynn-system-app": "true"})
	if s.restartLimitReached(crashedJob(s, f, 50), 50) {
		t.Fatal("system app stopped restarting")
	}
}

func TestRestartLimitResetOnScaleAndSettingChange(t *testing.T) {
	s, f := restartLimitTestScheduler(3, nil)
	s.restartJob(crashedJob(s, f, 3))
	s.setMaxRestarts(5)
	if len(s.restartsExhausted) != 0 {
		t.Fatal("changing max_restarts kept exhausted processes")
	}
	if diff := s.formationDiff(f); diff["web"] != 1 {
		t.Fatalf("diff=%v, want web=1 after limit change", diff)
	}

	s.restartJob(crashedJob(s, f, 5))
	if len(s.restartsExhausted) != 1 {
		t.Fatal("process at the new limit kept restarting")
	}
	s.handleFormation(&ct.ExpandedFormation{App: f.App, Release: f.Release, Processes: map[string]int{"web": 2}})
	if len(s.restartsExhausted) != 0 {
		t.Fatal("scaling the formation kept exhausted processes")
	}
}

func TestBackoffDurationImmediateRestarts(t *testing.T) {
	for _, n := range []uint{0, 1, 2, 3, 4} {
		if d := backoffDuration(n, 0); d != 0 {
			t.Fatalf("restarts=%d jitter=0: got %s, want 0", n, d)
		}
		if d := backoffDuration(n, 1); d != 0 {
			t.Fatalf("restarts=%d jitter=1: got %s, want 0", n, d)
		}
	}
}

func TestBackoffDurationExponential(t *testing.T) {
	cases := []struct {
		restarts uint
		wantMax  time.Duration
	}{
		{5, 10 * time.Second},
		{6, 20 * time.Second},
		{7, 40 * time.Second},
		{8, 80 * time.Second},
		{9, 160 * time.Second},
		{10, 5 * time.Minute},
		{15, 5 * time.Minute},
		{100, 5 * time.Minute},
	}
	for _, tc := range cases {
		gotMax := backoffDuration(tc.restarts, 1)
		if gotMax != tc.wantMax {
			t.Errorf("restarts=%d jitter=1: got %s, want %s", tc.restarts, gotMax, tc.wantMax)
		}
		gotMin := backoffDuration(tc.restarts, 0)
		if gotMin != tc.wantMax/2 {
			t.Errorf("restarts=%d jitter=0: got %s, want %s", tc.restarts, gotMin, tc.wantMax/2)
		}
		gotMid := backoffDuration(tc.restarts, 0.5)
		if gotMid != tc.wantMax/2+tc.wantMax/4 {
			t.Errorf("restarts=%d jitter=0.5: got %s, want %s", tc.restarts, gotMid, tc.wantMax/2+tc.wantMax/4)
		}
	}
}

func TestBackoffDurationClampsJitter(t *testing.T) {
	d := backoffDuration(5, -1)
	if d != 5*time.Second {
		t.Fatalf("negative jitter: got %s, want 5s", d)
	}
	d = backoffDuration(5, 2)
	if d != 10*time.Second {
		t.Fatalf("jitter > 1: got %s, want 10s", d)
	}
}

func TestGetBackoffDurationBounds(t *testing.T) {
	s := &Scheduler{}
	for i := 0; i < 50; i++ {
		if d := s.getBackoffDuration(0); d != 0 {
			t.Fatalf("first crash delayed: %s", d)
		}
		if d := s.getBackoffDuration(4); d != 0 {
			t.Fatalf("deploy-window restart delayed: %s", d)
		}
		if d := s.getBackoffDuration(5); d < 5*time.Second || d > 10*time.Second {
			t.Fatalf("restarts=5 out of jitter range: %s", d)
		}
		if d := s.getBackoffDuration(100); d < 5*time.Minute/2 || d > 5*time.Minute {
			t.Fatalf("capped restart out of jitter range: %s", d)
		}
	}
}

func TestCrashLoopEventRestartsMatchesBackoffCap(t *testing.T) {
	if ct.CrashLoopEventRestarts < int32(backoffImmediateRestarts) {
		t.Fatalf("crash_loop fires at %d, before immediate-restart window %d", ct.CrashLoopEventRestarts, backoffImmediateRestarts)
	}
	if d := backoffDuration(uint(ct.CrashLoopEventRestarts), 1); d != backoffMax {
		t.Fatalf("crash_loop threshold backoff=%s, want cap %s", d, backoffMax)
	}
}
