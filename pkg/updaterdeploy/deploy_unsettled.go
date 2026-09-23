package updaterdeploy

import (
	"strings"
	"time"
)

const (
	maxTransientDeployUnsettledAttempts = 24
	maxScaleTimeoutDeployAttempts       = 3
	transientDeployRetryDelay           = 10 * time.Second
)

// ShouldRetryAfterUnsettledDiscoverdLeader returns whether a failed system-app
// deploy looks like transient service-discovery / sirenia fallout (for
// example leader.postgres.discoverd not yet propagated) rather than a
// permanent scheduler error.
func ShouldRetryAfterUnsettledDiscoverdLeader(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "sirenia") {
		return true
	}
	if strings.Contains(msg, "leader.") && strings.Contains(msg, ".discoverd") {
		return true
	}
	if strings.Contains(msg, "no such host") && strings.Contains(msg, ".discoverd") {
		return true
	}
	if strings.Contains(msg, "postgres.discoverd") {
		return true
	}
	if strings.Contains(msg, "no such host") && strings.Contains(msg, "postgres") {
		return true
	}
	return false
}

// ShouldRetryAfterControllerUnavailable returns whether a system-app deploy
// failed because the controller could not reach postgres (or the HTTP helper
// collapsed that into unknown_error). After a singleton postgres swap the
// next GetAppRelease often fails this way until the replacement peer is up.
func ShouldRetryAfterControllerUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unknown_error"):
		return true
	case strings.Contains(msg, "something went wrong"):
		return true
	case strings.Contains(msg, "connection refused"):
		return true
	case strings.Contains(msg, "connection reset"):
		return true
	case strings.Contains(msg, "broken pipe"):
		return true
	case strings.Contains(msg, "i/o timeout"):
		return true
	case strings.Contains(msg, "unexpected eof"):
		return true
	default:
		return false
	}
}

// ShouldRetryTransientSystemDeploy is the combined matcher used by flynn-host
// update and the in-cluster updater.
func ShouldRetryTransientSystemDeploy(err error) bool {
	return ShouldRetryAfterUnsettledDiscoverdLeader(err) ||
		ShouldRetryAfterScaleTimeout(err) ||
		ShouldRetryAfterControllerUnavailable(err)
}

// ShouldRetryAfterScaleTimeout returns whether a deploy failed because the
// controller's scale step, a sirenia startInstance wait, or an omni
// one-down-one-up wait for old scheduler/router jobs did not finish before
// the app's deploy timeout. This is common after a cluster-wide host
// restart when the scheduler is still placing jobs. The HA sirenia wait
// error is "timed out waiting for new instance to come up" (no "sirenia"
// substring), so it is not covered by ShouldRetryAfterUnsettledDiscoverdLeader.
func ShouldRetryAfterScaleTimeout(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timed out waiting for scale to complete"):
		return true
	case strings.Contains(msg, "timed out waiting for new instance to come up"):
		return true
	case strings.Contains(msg, "timed out waiting for new sirenia peer to come up"):
		return true
	case strings.Contains(msg, "timed out waiting for old") && strings.Contains(msg, "jobs to stop"):
		return true
	default:
		return false
	}
}

// MaxTransientDeployUnsettledAttempts is the retry budget for DeployAppRelease
// when ShouldRetryAfterUnsettledDiscoverdLeader matches.
func MaxTransientDeployUnsettledAttempts() int { return maxTransientDeployUnsettledAttempts }

// MaxScaleTimeoutDeployAttempts is the retry budget for long deploy timeouts
// (scale complete / new instance). Each attempt can take the full app deploy
// timeout (up to 10 minutes for system apps), so this stays small.
func MaxScaleTimeoutDeployAttempts() int { return maxScaleTimeoutDeployAttempts }

// MaxTransientDeployAttempts returns the retry budget for a given transient
// deploy error. Long scale/instance waits use MaxScaleTimeoutDeployAttempts
// because each attempt can take the full app deploy timeout. Fast-failing
// discoverd/controller errors keep the larger settle budget.
func MaxTransientDeployAttempts(err error) int {
	if ShouldRetryAfterScaleTimeout(err) {
		return maxScaleTimeoutDeployAttempts
	}
	return maxTransientDeployUnsettledAttempts
}

// TransientDeployRetryDelay is the sleep between those retries.
func TransientDeployRetryDelay() time.Duration { return transientDeployRetryDelay }
