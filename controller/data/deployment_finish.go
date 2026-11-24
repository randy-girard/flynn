package data

import (
	"time"

	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// A deployment's finished_at and its outcome are written together (EDGE-75).
// finished_at used to be set before the rollback and before the failed
// event, so a reader saw "finished, status running" for the whole rollback
// and flynn-host update took that for success.

// FinishDeployment sets finished_at and writes the terminal deployment event
// (status complete or failed, with errMsg) in one transaction, so no reader
// sees one without the other. It returns false, writing nothing, when the
// deployment is already finished (another worker or the stale-deploy repair
// got there first) or does not exist.
func FinishDeployment(db *postgres.DB, deploymentID, status, errMsg string) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	var appID, newReleaseID string
	err = tx.QueryRow("deployment_finish", deploymentID).Scan(&appID, &newReleaseID)
	if err == pgx.ErrNoRows {
		tx.Rollback()
		return false, nil
	}
	if err != nil {
		tx.Rollback()
		return false, err
	}
	e := ct.DeploymentEvent{
		AppID:        appID,
		DeploymentID: deploymentID,
		ReleaseID:    newReleaseID,
		Status:       status,
		Error:        errMsg,
	}
	if err := tx.Exec("event_insert_op", appID, deploymentID, string(ct.EventTypeDeployment), e, ct.EventOpUpdate); err != nil {
		tx.Rollback()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// rollbackProgressPrefix starts the Progress line of the event that marks a
// rollback as started; the rest is the deploy's error.
const rollbackProgressPrefix = "rolling back: "

// MarkDeploymentRollingBack records, before a failed deployment is rolled
// back, that its worker has started the rollback. The deployment stays
// unfinished (status running) until FinishDeployment. A worker that claims
// the job again in the meantime (the que lock is lost when the platform
// Postgres primary moves) must not run Perform again;
// DeploymentRollbackStarted tells it.
func MarkDeploymentRollingBack(db *postgres.DB, d *ct.Deployment, errMsg string) error {
	e := ct.DeploymentEvent{
		AppID:        d.AppID,
		DeploymentID: d.ID,
		ReleaseID:    d.NewReleaseID,
		Status:       "running",
		RollingBack:  true,
		Progress:     rollbackProgressPrefix + errMsg,
	}
	return db.Exec("event_insert_op", d.AppID, d.ID, string(ct.EventTypeDeployment), e, ct.EventOpUpdate)
}

// DeploymentRollbackStarted returns when a worker last marked the deployment
// as rolling back and the deploy error it recorded, or ok=false when no
// rollback was started.
func DeploymentRollbackStarted(db *postgres.DB, deploymentID string) (at time.Time, errMsg string, ok bool, err error) {
	var progress string
	err = db.QueryRow("deployment_rollback_started", deploymentID).Scan(&at, &progress)
	if err == pgx.ErrNoRows {
		return time.Time{}, "", false, nil
	}
	if err != nil {
		return time.Time{}, "", false, err
	}
	if len(progress) >= len(rollbackProgressPrefix) && progress[:len(rollbackProgressPrefix)] == rollbackProgressPrefix {
		progress = progress[len(rollbackProgressPrefix):]
	}
	return at, progress, true, nil
}
