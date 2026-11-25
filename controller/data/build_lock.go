package data

import (
	"time"

	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// BuildLockRepo stores the per-app build lease (EDGE-09) and the keyed
// leases that are not one app's (EDGE-40).
type BuildLockRepo struct {
	db *postgres.DB
}

func NewBuildLockRepo(db *postgres.DB) *BuildLockRepo {
	return &BuildLockRepo{db: db}
}

// scanBuildLock reads a lease row; the first column is the app id or, for
// a keyed lease, the key.
func scanBuildLock(s postgres.Scanner, keyed bool) (*ct.BuildLock, error) {
	l := &ct.BuildLock{}
	var id string
	var acquired, expires time.Time
	if err := s.Scan(&id, &l.Holder, &l.Description, &acquired, &expires); err != nil {
		if err == pgx.ErrNoRows {
			err = ErrNotFound
		}
		return nil, err
	}
	if keyed {
		l.Key = id
	} else {
		l.AppID = id
	}
	l.AcquiredAt, l.ExpiresAt = &acquired, &expires
	return l, nil
}

// Acquire takes the app's lease for holder, or renews it if holder already
// has it. held is the other holder's live lease when the app is taken.
func (r *BuildLockRepo) Acquire(appID, holder, description string, ttl time.Duration) (lock, held *ct.BuildLock, err error) {
	return r.acquire("app_build_lock_acquire", "app_build_lock_select", false, appID, holder, description, ttl)
}

// Release drops the app's lease if holder has it.
func (r *BuildLockRepo) Release(appID, holder string) error {
	return r.db.Exec("app_build_lock_release", appID, holder)
}

// AcquireKey is Acquire for a keyed lease such as "pipeline:shop" (EDGE-40).
func (r *BuildLockRepo) AcquireKey(key, holder, description string, ttl time.Duration) (lock, held *ct.BuildLock, err error) {
	return r.acquire("build_lock_acquire", "build_lock_select", true, key, holder, description, ttl)
}

// ReleaseKey drops a keyed lease if holder has it.
func (r *BuildLockRepo) ReleaseKey(key, holder string) error {
	return r.db.Exec("build_lock_release", key, holder)
}

func (r *BuildLockRepo) acquire(acquireQuery, selectQuery string, keyed bool, id, holder, description string, ttl time.Duration) (lock, held *ct.BuildLock, err error) {
	secs := int(ttl / time.Second)
	if secs < 1 {
		secs = 1
	}
	// The insert and the read of the other holder are separate statements;
	// a lease that expires or is released in between is retried.
	for attempt := 0; attempt < 3; attempt++ {
		lock, err = scanBuildLock(r.db.QueryRow(acquireQuery, id, holder, description, secs), keyed)
		if err == nil {
			return lock, nil, nil
		}
		if err != ErrNotFound {
			return nil, nil, err
		}
		held, err = scanBuildLock(r.db.QueryRow(selectQuery, id), keyed)
		if err == nil {
			return nil, held, nil
		}
		if err != ErrNotFound {
			return nil, nil, err
		}
	}
	return nil, nil, ErrNotFound
}
