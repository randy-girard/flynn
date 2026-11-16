package data

import (
	"github.com/jackc/pgx"

	"github.com/randy-girard/flynn/controller/placement"
)

// GetWorkloadPlacement reads cluster_settings.workload_placement.
// A missing row is mixed, which is also the column default.
func (r *TenancyRepo) GetWorkloadPlacement() (string, error) {
	if r == nil || r.db == nil {
		return "", ErrNotFound
	}
	var mode string
	err := r.db.QueryRow(`SELECT workload_placement FROM cluster_settings WHERE id = 1`).Scan(&mode)
	if err == pgx.ErrNoRows {
		return placement.ModeMixed, nil
	}
	return mode, err
}

// SetWorkloadPlacement writes cluster_settings.workload_placement only.
func (r *TenancyRepo) SetWorkloadPlacement(mode string) error {
	if r == nil || r.db == nil {
		return ErrNotFound
	}
	return r.db.Exec(`UPDATE cluster_settings SET workload_placement = $1 WHERE id = 1`, mode)
}
