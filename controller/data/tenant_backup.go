package data

import (
	"errors"
	"time"

	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// ErrTenantBackupLock is returned when a heartbeat or finish does not match
// the running attempt. The caller lost the lock.
var ErrTenantBackupLock = errors.New("tenant backup lock lost")

// TenantBackupRepo stores dump status. Object bytes stay in the blobstore.
type TenantBackupRepo struct {
	db *postgres.DB
}

func NewTenantBackupRepo(db *postgres.DB) *TenantBackupRepo {
	return &TenantBackupRepo{db: db}
}

const tenantBackupCols = `resource_id::text, attempt_state, attempt_started_at, attempt_finished_at, attempt_error, attempt_timestamp, heartbeat_at, last_success_at, last_success_timestamp, updated_at`

func scanTenantBackup(s postgres.Scanner) (*ct.TenantBackup, error) {
	b := &ct.TenantBackup{}
	err := s.Scan(
		&b.ResourceID, &b.AttemptState, &b.AttemptStartedAt, &b.AttemptFinishedAt,
		&b.AttemptError, &b.AttemptTimestamp, &b.HeartbeatAt, &b.LastSuccessAt,
		&b.LastSuccessTimestamp, &b.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

func mapBackupWriteErr(err error) error {
	if err == nil {
		return nil
	}
	if postgres.IsPostgresCode(err, postgres.ForeignKeyViolation) {
		return ErrNotFound
	}
	return err
}

// Begin takes the running lock unless a heartbeat newer than 2 minutes
// already holds it. The 2 minutes are server now(), not the caller's clock.
func (r *TenantBackupRepo) Begin(resourceID, timestamp string) (*ct.TenantBackup, bool, error) {
	row := r.db.QueryRow(`
		INSERT INTO tenant_backup_status (
			resource_id, attempt_state, attempt_started_at, attempt_finished_at,
			attempt_error, attempt_timestamp, heartbeat_at, updated_at
		) VALUES ($1, 'running', now(), NULL, '', $2, now(), now())
		ON CONFLICT (resource_id) DO UPDATE SET
			attempt_state = 'running',
			attempt_started_at = now(),
			attempt_finished_at = NULL,
			attempt_error = '',
			attempt_timestamp = EXCLUDED.attempt_timestamp,
			heartbeat_at = now(),
			updated_at = now()
		WHERE tenant_backup_status.attempt_state IS DISTINCT FROM 'running'
			OR tenant_backup_status.heartbeat_at IS NULL
			OR tenant_backup_status.heartbeat_at < now() - interval '2 minutes'
		RETURNING `+tenantBackupCols,
		resourceID, timestamp,
	)
	st, err := scanTenantBackup(row)
	if err == ErrNotFound {
		cur, getErr := r.Get(resourceID)
		if getErr != nil {
			return nil, false, mapBackupWriteErr(getErr)
		}
		return cur, false, nil
	}
	if err != nil {
		return nil, false, mapBackupWriteErr(err)
	}
	return st, true, nil
}

// Heartbeat refreshes the lock for the running timestamp.
func (r *TenantBackupRepo) Heartbeat(resourceID, timestamp string) (*ct.TenantBackup, error) {
	row := r.db.QueryRow(`
		UPDATE tenant_backup_status
		SET heartbeat_at = now(), updated_at = now()
		WHERE resource_id = $1 AND attempt_state = 'running' AND attempt_timestamp = $2
		RETURNING `+tenantBackupCols,
		resourceID, timestamp,
	)
	st, err := scanTenantBackup(row)
	if err == ErrNotFound {
		return nil, ErrTenantBackupLock
	}
	return st, err
}

// Finish closes the running attempt. Success inserts the catalog rows in the
// same transaction and sets last_success. Failure leaves last_success as it was.
func (r *TenantBackupRepo) Finish(resourceID, timestamp, state, errMsg string, databases []string) (*ct.TenantBackup, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var row postgres.Scanner
	if state == "succeeded" {
		row = tx.QueryRow(`
			UPDATE tenant_backup_status SET
				attempt_state = 'succeeded',
				attempt_finished_at = now(),
				attempt_error = '',
				heartbeat_at = now(),
				last_success_at = now(),
				last_success_timestamp = $2,
				updated_at = now()
			WHERE resource_id = $1 AND attempt_state = 'running' AND attempt_timestamp = $2
			RETURNING `+tenantBackupCols,
			resourceID, timestamp,
		)
	} else {
		row = tx.QueryRow(`
			UPDATE tenant_backup_status SET
				attempt_state = 'failed',
				attempt_finished_at = now(),
				attempt_error = $3,
				heartbeat_at = now(),
				updated_at = now()
			WHERE resource_id = $1 AND attempt_state = 'running' AND attempt_timestamp = $2
			RETURNING `+tenantBackupCols,
			resourceID, timestamp, errMsg,
		)
	}
	st, err := scanTenantBackup(row)
	if err == ErrNotFound {
		return nil, ErrTenantBackupLock
	}
	if err != nil {
		return nil, err
	}
	if state == "succeeded" {
		if err := tx.Exec(`DELETE FROM tenant_backup_dumps WHERE resource_id = $1 AND dump_timestamp = $2`, resourceID, timestamp); err != nil {
			return nil, err
		}
		for _, db := range databases {
			if err := tx.Exec(`INSERT INTO tenant_backup_dumps (resource_id, dump_timestamp, database_name) VALUES ($1, $2, $3)`, resourceID, timestamp, db); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return st, nil
}

// Fail records a refusal when no fresh running attempt holds the lock.
// A fresh running row is left alone. last_success is not cleared.
func (r *TenantBackupRepo) Fail(resourceID, errMsg string) error {
	var id string
	err := r.db.QueryRow(`
		INSERT INTO tenant_backup_status (
			resource_id, attempt_state, attempt_finished_at, attempt_error,
			attempt_timestamp, heartbeat_at, updated_at
		) VALUES ($1, 'failed', now(), $2, '', NULL, now())
		ON CONFLICT (resource_id) DO UPDATE SET
			attempt_state = 'failed',
			attempt_finished_at = now(),
			attempt_error = EXCLUDED.attempt_error,
			attempt_timestamp = '',
			heartbeat_at = NULL,
			updated_at = now()
		WHERE tenant_backup_status.attempt_state IS DISTINCT FROM 'running'
			OR tenant_backup_status.heartbeat_at IS NULL
			OR tenant_backup_status.heartbeat_at < now() - interval '2 minutes'
		RETURNING resource_id::text`,
		resourceID, errMsg,
	).Scan(&id)
	if err == pgx.ErrNoRows {
		return nil
	}
	return mapBackupWriteErr(err)
}

// Get returns one status row.
func (r *TenantBackupRepo) Get(resourceID string) (*ct.TenantBackup, error) {
	return scanTenantBackup(r.db.QueryRow(`SELECT `+tenantBackupCols+` FROM tenant_backup_status WHERE resource_id = $1`, resourceID))
}

// List returns every status row. An empty catalog is an empty slice.
func (r *TenantBackupRepo) List() ([]*ct.TenantBackup, error) {
	rows, err := r.db.Query(`SELECT ` + tenantBackupCols + ` FROM tenant_backup_status ORDER BY resource_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ct.TenantBackup
	for rows.Next() {
		st, err := scanTenantBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// ListDumps groups catalog rows for one resource, newest timestamp first.
func (r *TenantBackupRepo) ListDumps(resourceID string) ([]ct.TenantBackupDump, error) {
	rows, err := r.db.Query(`
		SELECT dump_timestamp, database_name
		FROM tenant_backup_dumps
		WHERE resource_id = $1
		ORDER BY dump_timestamp DESC, database_name`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ct.TenantBackupDump
	for rows.Next() {
		var ts, db string
		if err := rows.Scan(&ts, &db); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].Timestamp == ts {
			out[n-1].Databases = append(out[n-1].Databases, db)
			continue
		}
		out = append(out, ct.TenantBackupDump{Timestamp: ts, Databases: []string{db}})
	}
	return out, rows.Err()
}

// DeleteDump removes one timestamp from the catalog. Blob objects are deleted
// by the plugin before this row goes away.
func (r *TenantBackupRepo) DeleteDump(resourceID, timestamp string) error {
	return r.db.Exec(`DELETE FROM tenant_backup_dumps WHERE resource_id = $1 AND dump_timestamp = $2`, resourceID, timestamp)
}

// TouchHeartbeat sets heartbeat_at. Tests use it to age a lock without waiting.
func (r *TenantBackupRepo) TouchHeartbeat(resourceID string, at time.Time) error {
	return r.db.Exec(`UPDATE tenant_backup_status SET heartbeat_at = $2 WHERE resource_id = $1`, resourceID, at)
}
