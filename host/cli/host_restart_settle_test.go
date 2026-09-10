package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/flynn/flynn/host/types"
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
	if updateClusterSizeTimeout < 2*updateInterHostDelay {
		t.Fatalf("updateClusterSizeTimeout=%s should be at least 2x inter-host delay (%s)", updateClusterSizeTimeout, updateInterHostDelay)
	}
	if updateRemoteDaemonTimeout < 3*time.Minute {
		t.Fatalf("updateRemoteDaemonTimeout=%s is too short for raft rejoin", updateRemoteDaemonTimeout)
	}
}

func TestIsControllerPlacedJob(t *testing.T) {
	tests := []struct {
		name string
		job  *host.Job
		want bool
	}{
		{"nil job", nil, false},
		{"empty job", &host.Job{}, false},
		{"controller metadata", &host.Job{Metadata: map[string]string{"flynn-controller.app": "app-id"}}, true},
		{"controller env", &host.Job{Config: host.ContainerConfig{Env: map[string]string{"FLYNN_APP_ID": "app-id"}}}, true},
		{"wrong metadata key", &host.Job{Metadata: map[string]string{"FLYNN_APP_ID": "app-id"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isControllerPlacedJob(tt.job); got != tt.want {
				t.Fatalf("isControllerPlacedJob() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSettleClusterSizeError(t *testing.T) {
	first := errors.New("size timeout")
	retry := errors.New("retry size timeout")
	health := errors.New("unhealthy")

	tests := []struct {
		name                 string
		first, health, retry error
		fatal                bool
		wantNil              bool
		want                 error
	}{
		{"first wait ok", nil, nil, nil, true, true, nil},
		{"fatal health ok retry ok", first, nil, nil, true, true, nil},
		{"fatal health ok retry fail", first, nil, retry, true, false, retry},
		{"fatal health fail keeps first error", first, health, retry, true, false, first},
		{"non-fatal continues", first, health, retry, false, true, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := settleClusterSizeError(tc.first, tc.fatal, tc.health, tc.retry)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
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

func TestDecideUpdateRolloutImagesOnlyWithAllNodes(t *testing.T) {
	// --images-only + --all-nodes must not push binaries but must roll images.
	plan := decideUpdateRollout(true, false, true, 3, true)
	if plan.UpdateRemotes {
		t.Fatal("images-only must not update remotes")
	}
	if !plan.RolloutImages {
		t.Fatal("images-only with all-nodes must roll out images")
	}
}

func TestValidateImagesOnlyFlagsSingleHostUnknownCount(t *testing.T) {
	// hostCount 0 with no error is treated as single-host by validateImagesOnlyFlags
	// (only hostCount > 1 is rejected).
	if err := validateImagesOnlyFlags(true, false, 0, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
