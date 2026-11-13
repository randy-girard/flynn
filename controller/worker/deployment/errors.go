package deployment

import (
	"errors"
	"fmt"

	"github.com/randy-girard/flynn/controller/worker/types"
	"github.com/randy-girard/flynn/discoverd/client"
)

// errSireniaDeployActive means another worker is already rolling this sirenia
// deployment. The que lock is stored in postgres, so stopping the primary
// drops it and a second worker can claim the same job. That second claim
// sees the new-release peers the first claim started.
var errSireniaDeployActive = errors.New("sirenia deployment already has new-release peers")

// sireniaStartupPeerError classifies a discoverd Up seen before the service
// watch reaches Current. A peer from the release being deployed is the
// in-progress attempt, not a corrupt cluster.
func sireniaStartupPeerError(newReleaseID string, inst *discoverd.Instance) error {
	if inst == nil || inst.Meta == nil || newReleaseID == "" {
		return nil
	}
	if inst.Meta["FLYNN_RELEASE_ID"] == newReleaseID {
		return errSireniaDeployActive
	}
	return nil
}

type deploymentOutcome int

const (
	outcomeComplete deploymentOutcome = iota
	outcomeRollback
	outcomeSkipRollback
	outcomeLeaveRunning
)

// deploymentOutcomeFor decides what HandleDeployment does with Perform's
// error. Leave-running keeps finished_at unset and does not restore or
// delete formations.
func deploymentOutcomeFor(err error) deploymentOutcome {
	switch {
	case err == nil:
		return outcomeComplete
	case err == worker.ErrStopped || errors.Is(err, errSireniaDeployActive):
		return outcomeLeaveRunning
	case IsSkipRollback(err):
		return outcomeSkipRollback
	default:
		return outcomeRollback
	}
}

type ErrSkipRollback struct {
	Err string
}

func (e ErrSkipRollback) Error() string {
	return e.Err
}

func IsSkipRollback(err error) bool {
	_, ok := err.(ErrSkipRollback)
	return ok
}

type UnknownStrategyError struct {
	Strategy string
}

func (e UnknownStrategyError) Error() string {
	return fmt.Sprintf("deployment: unknown strategy %q", e.Strategy)
}
