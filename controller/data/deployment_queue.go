package data

import (
	"encoding/json"
	"strings"

	"github.com/flynn/que-go"
	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
)

func (r *DeploymentRepo) txLatestUnfinished(tx rowQueryer, appID string) (*ct.Deployment, error) {
	d, err := scanDeployment(tx.QueryRow("deployment_select_latest_unfinished", appID))
	if err == ErrNotFound {
		return nil, nil
	}
	return d, err
}

// StartNextQueuedDeployment marks the oldest queued deploy as running and
// enqueues its worker job. Only one deploy per app has started_at set.
func StartNextQueuedDeployment(db *postgres.DB, appID string) error {
	if db == nil {
		return nil
	}
	r := &DeploymentRepo{db: db, q: que.NewClient(db.ConnPool)}
	return r.StartNextQueued(appID)
}

func (r *DeploymentRepo) StartNextQueued(appID string) error {
	if r == nil || r.db == nil || strings.TrimSpace(appID) == "" {
		return nil
	}
	if r.q == nil {
		r.q = que.NewClient(r.db.ConnPool)
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	if _, err := selectApp(tx, appID, true); err != nil && err != ErrNotFound {
		tx.Rollback()
		return err
	}
	var id string
	err = tx.QueryRow("deployment_start_next", appID).Scan(&id)
	if err == pgx.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		tx.Rollback()
		return err
	}
	d, err := scanDeployment(tx.QueryRow("deployment_select", id))
	if err != nil {
		tx.Rollback()
		return err
	}
	args, err := json.Marshal(ct.DeployID{ID: id})
	if err != nil {
		tx.Rollback()
		return err
	}
	if err := r.q.EnqueueInTx(&que.Job{Type: "deployment", Args: args}, tx.Tx); err != nil {
		tx.Rollback()
		return err
	}
	if err := createDeploymentEvent(tx.Exec, d, "pending"); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
