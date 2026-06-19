package cli

import (
	"testing"
	"time"
)

func TestRollingRestartTimingDefaults(t *testing.T) {
	if updateHealthTimeout < 5*time.Minute {
		t.Fatalf("updateHealthTimeout=%s is too short for multi-node sirenia recovery", updateHealthTimeout)
	}
	if updateInterHostDelay < 10*time.Second {
		t.Fatalf("updateInterHostDelay=%s is too short between host restarts", updateInterHostDelay)
	}
	if updateWaitJobsTimeout <= 0 {
		t.Fatalf("updateWaitJobsTimeout must be positive")
	}
}

func TestHostRestartSettleOptionsDefaults(t *testing.T) {
	opts := hostRestartSettleOptions{}
	if opts.FatalClusterSize {
		t.Fatal("FatalClusterSize should default to false")
	}
	if opts.InterHostDelay {
		t.Fatal("InterHostDelay should default to false")
	}
}
