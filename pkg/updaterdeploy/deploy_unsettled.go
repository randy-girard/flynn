package updaterdeploy

import (
	"strings"
	"time"
)

const (
	maxTransientDeployUnsettledAttempts = 24
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
// controller's scale step did not finish before the app's deploy timeout.
// This is common after a cluster-wide host restart when the scheduler is
// still placing jobs.
func ShouldRetryAfterScaleTimeout(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "timed out waiting for scale to complete")
}

// MaxTransientDeployUnsettledAttempts is the retry budget for DeployAppRelease
// when ShouldRetryAfterUnsettledDiscoverdLeader matches.
func MaxTransientDeployUnsettledAttempts() int { return maxTransientDeployUnsettledAttempts }

// TransientDeployRetryDelay is the sleep between those retries.
func TransientDeployRetryDelay() time.Duration { return transientDeployRetryDelay }
