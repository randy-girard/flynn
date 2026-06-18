package updaterdeploy

import (
	"strings"
	"time"

	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/inconshreveable/log15"
)

const (
	// Poll discoverd until a sirenia service leader appears after redeploy.
	sireniaDiscoverdLeaderMaxAttempts = 60
	sireniaDiscoverdLeaderPollDelay   = 5 * time.Second

	maxTransientDeployUnsettledAttempts = 24
	transientDeployRetryDelay           = 10 * time.Second
)

// SireniaApplianceServices lists discoverd service names for sirenia-managed
// database appliances updated during cluster upgrades.
var SireniaApplianceServices = []string{"postgres", "mariadb", "mongodb"}

// WaitSireniaLeaderStable blocks until discoverd reports a leader for service,
// or retries are exhausted with a warning. After redeploying a sirenia appliance
// there is typically a gap where leader.<service>.discoverd returns NXDOMAIN;
// controller jobs and follow-on deploys rely on it, so callers should wait for
// the leader slot to repopulate before continuing.
func WaitSireniaLeaderStable(service string, log log15.Logger) {
	svc := discoverd.NewService(service)

	for attempt := 1; attempt <= sireniaDiscoverdLeaderMaxAttempts; attempt++ {
		inst, err := svc.Leader()
		if err == nil && inst != nil && inst.Addr != "" {
			if attempt > 1 {
				log.Info("sirenia discoverd leader is available again",
					"service", service, "addr", inst.Addr, "attempt", attempt)
			}
			return
		}
		if err != nil {
			log.Debug("sirenia discoverd leader not ready yet",
				"service", service, "attempt", attempt, "err", err)
		} else {
			log.Debug("sirenia discoverd leader not ready yet",
				"service", service, "attempt", attempt)
		}
		if attempt < sireniaDiscoverdLeaderMaxAttempts {
			time.Sleep(sireniaDiscoverdLeaderPollDelay)
		}
	}
	log.Warn("sirenia discoverd leader did not stabilize within timeout; downstream deploy may see transient DNS failures",
		"service", service)
}

// WaitPostgresDiscoverdLeaderStable is equivalent to WaitSireniaLeaderStable("postgres").
func WaitPostgresDiscoverdLeaderStable(log log15.Logger) {
	WaitSireniaLeaderStable("postgres", log)
}

// WaitSireniaApplianceLeadersStable waits for every sirenia appliance service
// leader slot to repopulate. Used between rolling host restarts when multiple
// database peers may have been disrupted on the previous host.
func WaitSireniaApplianceLeadersStable(log log15.Logger) {
	for _, service := range SireniaApplianceServices {
		WaitSireniaLeaderStable(service, log.New("service", service))
	}
}

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
	// Postgres / other sirenia appliance leader DNS propagation
	if strings.Contains(msg, "leader.postgres.discoverd") ||
		strings.Contains(msg, "postgres.discoverd") ||
		strings.Contains(msg, "leader.mariadb.discoverd") ||
		strings.Contains(msg, "leader.mongodb.discoverd") ||
		strings.Contains(msg, "leader.maria.discoverd") {
		return true
	}
	// e.g. "lookup leader.postgres.discoverd: no such host"
	if strings.Contains(msg, "no such host") && strings.Contains(msg, "postgres") {
		return true
	}
	return false
}

// MaxTransientDeployUnsettledAttempts is the retry budget for DeployAppRelease
// when ShouldRetryAfterUnsettledDiscoverdLeader matches.
func MaxTransientDeployUnsettledAttempts() int { return maxTransientDeployUnsettledAttempts }

// TransientDeployRetryDelay is the sleep between those retries.
func TransientDeployRetryDelay() time.Duration { return transientDeployRetryDelay }
