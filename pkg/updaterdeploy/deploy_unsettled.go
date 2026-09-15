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
