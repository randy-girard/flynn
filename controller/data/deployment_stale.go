package data

import (
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

type deployQueJob struct {
	Exists bool
	Locked bool
}

// openDeployIsStale is an unfinished deployments row that no worker is
// actively performing. isolate_deploys is WHERE finished_at IS NULL, so a
// lost que job or a complete/failed event without finished_at blocks
// resource:add / resource:remove even though nothing is rolling.
func openDeployIsStale(d *ct.Deployment, job deployQueJob, now time.Time) bool {
	if d == nil || d.FinishedAt != nil {
		return false
	}
	status := d.Status
	if status == "complete" || status == "failed" {
		return true
	}
	if !job.Exists {
		return true
	}
	if job.Locked {
		return false
	}
	timeout := time.Duration(d.DeployTimeout) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(ct.DefaultDeployTimeout) * time.Second
	}
	if d.CreatedAt != nil && !now.Before(d.CreatedAt.Add(timeout)) {
		return true
	}
	return false
}

func (r *DeploymentRepo) GetOpen(appID string) (*ct.Deployment, error) {
	d, err := scanDeployment(r.db.QueryRow("deployment_select_open", appID))
	if err == ErrNotFound {
		return nil, nil
	}
	return d, err
}

func (r *DeploymentRepo) deploymentJobState(id string) deployQueJob {
	var lockedUntil time.Time
	err := r.db.QueryRow("deployment_que_job", id).Scan(&lockedUntil)
	if err != nil {
		return deployQueJob{}
	}
	return deployQueJob{Exists: true, Locked: lockedUntil.After(time.Now())}
}

func (r *DeploymentRepo) releaseStaleOpenDeploy(appID string) bool {
	if r == nil {
		return false
	}
	d, err := r.GetOpen(appID)
	if err != nil || d == nil {
		return false
	}
	if !openDeployIsStale(d, r.deploymentJobState(d.ID), time.Now()) {
		return false
	}
	status := "failed"
	if d.Status == "complete" {
		status = "complete"
	}
	if r.appRepo != nil {
		if rel, err := r.appRepo.GetRelease(appID); err == nil && rel != nil && rel.ID == d.NewReleaseID {
			status = "complete"
		}
	}
	return r.finishOpen(d, status) == nil
}

func (r *DeploymentRepo) finishOpen(d *ct.Deployment, status string) error {
	if d == nil || d.ID == "" {
		return nil
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	if err := tx.Exec("deployment_update_finished_at_now", d.ID); err != nil {
		tx.Rollback()
		return err
	}
	if err := createDeploymentEvent(tx.Exec, d, status); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return r.StartNextQueued(d.AppID)
}
