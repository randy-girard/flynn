package types

import (
	"fmt"
	"strings"
	"time"
)

// ScaleStartingStuckTimeout is how long a host-accepted job may stay in
// "starting" before ScaleAppRelease fails. The scheduler only marks a
// ScaleRequest complete when running counts match the formation; starting
// jobs never count, so without this check the client sits until Timeout
// (10m for system apps). Pending jobs (no host yet) are not stuck-starting.
var ScaleStartingStuckTimeout = 30 * time.Second

// ScaleJobListProbeTimeout is how long ScaleAppRelease waits for JobList
// while looking for stuck starts. A hung JobList means the controller HTTP
// path is wedged (for example logmux blocking Follow), not a slow placement.
var ScaleJobListProbeTimeout = 15 * time.Second

// ScaleStallPollInterval is how often ScaleAppRelease polls JobList during
// a long wait.
var ScaleStallPollInterval = 5 * time.Second

// ShouldProbeScaleStall is true when the caller asked for a wait longer
// than a normal app deploy. DefaultDeployTimeout (120s) and DefaultScaleTimeout
// (30s) already bound git-push and CLI scale. The 10m system-app ceiling is
// for pending placement after a host restart, not for wedged starts.
func ShouldProbeScaleStall(timeout time.Duration) bool {
	return timeout > time.Duration(DefaultDeployTimeout)*time.Second
}

func jobStuckKey(j *Job) string {
	if j.ID != "" {
		return j.ID
	}
	return j.UUID
}

func jobIsHostAcceptedStarting(j *Job) bool {
	if j == nil || j.State != JobStateStarting {
		return false
	}
	return j.HostID != "" || j.ID != ""
}

// ErrJobsStuckStarting returns a fail-fast error when formation jobs have
// been accepted by a host without becoming up. Age is measured from the
// first observation in seen, not CreatedAt: a job can be pending for minutes
// before starting, and CreatedAt includes that wait.
func ErrJobsStuckStarting(jobs []*Job, releaseID string, now time.Time, seen map[string]time.Time) error {
	if seen == nil {
		return fmt.Errorf("scale stall detection requires a seen map")
	}
	current := make(map[string]struct{})
	var stuck []*Job
	for _, j := range jobs {
		if j == nil {
			continue
		}
		if releaseID != "" && j.ReleaseID != releaseID {
			continue
		}
		if !jobIsHostAcceptedStarting(j) {
			continue
		}
		key := jobStuckKey(j)
		if key == "" {
			continue
		}
		current[key] = struct{}{}
		first, ok := seen[key]
		if !ok {
			seen[key] = now
			continue
		}
		if now.Sub(first) < ScaleStartingStuckTimeout {
			continue
		}
		stuck = append(stuck, j)
	}
	for k := range seen {
		if _, ok := current[k]; !ok {
			delete(seen, k)
		}
	}
	if len(stuck) == 0 {
		return nil
	}
	parts := make([]string, 0, len(stuck))
	for _, j := range stuck {
		id := j.ID
		if id == "" {
			id = j.UUID
		}
		parts = append(parts, fmt.Sprintf("%s %s host=%s", j.Type, id, j.HostID))
	}
	return fmt.Errorf("scale failed: jobs stuck in starting for >%s (not waiting for the deploy timeout): %s", ScaleStartingStuckTimeout, strings.Join(parts, "; "))
}

// ErrScaleJobListHung is returned when JobList does not return in time
// during stall detection.
func ErrScaleJobListHung(waited time.Duration) error {
	return fmt.Errorf("scale failed: JobList did not return in %s (controller HTTP is wedged, not a slow scale)", waited)
}
