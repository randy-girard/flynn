package main

import (
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

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
