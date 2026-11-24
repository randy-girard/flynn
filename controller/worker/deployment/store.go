package deployment

import (
	"time"

	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// deploymentStore is what HandleDeployment writes to the controller
// database. Tests replace it with a fake.
type deploymentStore interface {
	// CreateEvent writes a non-terminal deployment event.
	CreateEvent(e ct.DeploymentEvent) error
	// MarkRollingBack records that this worker is about to roll the
	// deployment back. The deployment stays unfinished.
	MarkRollingBack(d *ct.Deployment, errMsg string) error
	// RollbackStarted says whether, and when, a worker marked the
	// deployment as rolling back, and the deploy error it recorded.
	RollbackStarted(deploymentID string) (time.Time, string, bool, error)
	// Finish sets finished_at and writes the terminal event (complete or
	// failed) in one write. false means the deployment was already
	// finished and nothing was written.
	Finish(deploymentID, status, errMsg string) (bool, error)
	// StartNextQueued starts the app's next queued deployment.
	StartNextQueued(appID string) error
}

type pgDeploymentStore struct {
	db *postgres.DB
}

func (s *pgDeploymentStore) CreateEvent(e ct.DeploymentEvent) error {
	return execAttempts.Run(func() error {
		return s.db.Exec("event_insert_op", e.AppID, e.DeploymentID, string(ct.EventTypeDeployment), e, ct.EventOpUpdate)
	})
}

func (s *pgDeploymentStore) MarkRollingBack(d *ct.Deployment, errMsg string) error {
	return doneAttempts.Run(func() error {
		return data.MarkDeploymentRollingBack(s.db, d, errMsg)
	})
}

func (s *pgDeploymentStore) RollbackStarted(id string) (at time.Time, msg string, ok bool, err error) {
	err = execAttempts.Run(func() error {
		var e error
		at, msg, ok, e = data.DeploymentRollbackStarted(s.db, id)
		return e
	})
	return
}

func (s *pgDeploymentStore) Finish(id, status, errMsg string) (finished bool, err error) {
	err = doneAttempts.Run(func() error {
		var e error
		finished, e = data.FinishDeployment(s.db, id, status, errMsg)
		return e
	})
	return
}

func (s *pgDeploymentStore) StartNextQueued(appID string) error {
	return data.StartNextQueuedDeployment(s.db, appID)
}
