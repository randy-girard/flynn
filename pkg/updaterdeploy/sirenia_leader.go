package updaterdeploy

import (
	"time"

	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/inconshreveable/log15"
)

const (
	// Poll discoverd until a sirenia service leader appears after redeploy.
	sireniaDiscoverdLeaderMaxAttempts = 60
	sireniaDiscoverdLeaderPollDelay   = 5 * time.Second
)

// SireniaApplianceServices lists discoverd service names for sirenia-managed
// database appliances updated during cluster upgrades.
var SireniaApplianceServices = []string{"postgres", "mariadb", "mongodb"}

// optionalSireniaAppliances are database appliances that bootstrap with zero
// database peers (API-only) and may never register discoverd instances until
// an operator scales the formation up.
var optionalSireniaAppliances = map[string]bool{
	"mariadb": true,
	"mongodb": true,
}

// skipOptionalSireniaLeaderWait reports whether waiting for leader.<service>.discoverd
// can be skipped because the optional appliance has no database peers registered.
// Postgres is never skipped: a transient empty instance set during failover or
// rolling restart must still be waited out.
func skipOptionalSireniaLeaderWait(service string, instanceCount int, instancesErr error) bool {
	if !optionalSireniaAppliances[service] {
		return false
	}
	return instancesErr != nil || instanceCount == 0
}

// WaitSireniaLeaderStable blocks until discoverd reports a leader for service,
// or retries are exhausted with a warning. After redeploying a sirenia appliance
// there is typically a gap where leader.<service>.discoverd returns NXDOMAIN;
// controller jobs and follow-on deploys rely on it, so callers should wait for
// the leader slot to repopulate before continuing.
//
// Optional appliances (mariadb, mongodb) deployed with zero database processes
// never register peers in discoverd; waiting would always time out (~5 minutes).
func WaitSireniaLeaderStable(service string, log log15.Logger) {
	svc := discoverd.NewService(service)

	instances, err := svc.Instances()
	if skipOptionalSireniaLeaderWait(service, len(instances), err) {
		log.Info("skipping sirenia discoverd leader wait (optional appliance has no database peers)",
			"service", service)
		return
	}

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
