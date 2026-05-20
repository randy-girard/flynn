package updaterdeploy

import (
	"strings"
	"time"

	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/inconshreveable/log15"
)

const (
	// Poll discoverd until the Postgres service leader appears after redeploy.
	postgresDiscoverdLeaderMaxAttempts = 60
	postgresDiscoverdLeaderPollDelay   = 5 * time.Second

	maxTransientDeployUnsettledAttempts = 24
	transientDeployRetryDelay           = 10 * time.Second
)

// WaitPostgresDiscoverdLeaderStable blocks until discoverd reports a Postgres
// service leader, or retries are exhausted with a warning.  After deploying the
// postgres appliance there is typically a gap where DNS for
// leader.postgres.discoverd returns NXDOMAIN; controller jobs configured with
// PGHOST rely on it, so follow-on deployments must wait for the leader slot
// to repopulate before continuing.
func WaitPostgresDiscoverdLeaderStable(log log15.Logger) {
	svc := discoverd.NewService("postgres")

	for attempt := 1; attempt <= postgresDiscoverdLeaderMaxAttempts; attempt++ {
		inst, err := svc.Leader()
		if err == nil && inst != nil && inst.Addr != "" {
			if attempt > 1 {
				log.Info("postgres discoverd leader is available again",
					"addr", inst.Addr, "attempt", attempt)
			}
			return
		}
		if err != nil {
			log.Debug("postgres discoverd leader not ready yet", "attempt", attempt, "err", err)
		} else {
			log.Debug("postgres discoverd leader not ready yet", "attempt", attempt)
		}
		if attempt < postgresDiscoverdLeaderMaxAttempts {
			time.Sleep(postgresDiscoverdLeaderPollDelay)
		}
	}
	log.Warn("postgres discoverd leader did not stabilize within timeout; downstream deploy may see transient Postgres/DNS failures")
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
