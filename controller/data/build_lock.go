package data

import (
	"time"

	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// BuildLockRepo stores the per-app build lease (EDGE-09).
type BuildLockRepo struct {
	db *postgres.DB
}

func NewBuildLockRepo(db *postgres.DB) *BuildLockRepo {
	return &BuildLockRepo{db: db}
}

func scanBuildLock(s postgres.Scanner) (*ct.BuildLock, error) {
	l := &ct.BuildLock{}
	var acquired, expires time.Time
	if err := s.Scan(&l.AppID, &l.Holder, &l.Description, &acquired, &expires); err != nil {
		if err == pgx.ErrNoRows {
			err = ErrNotFound
		}
		return nil, err
	}
	l.AcquiredAt, l.ExpiresAt = &acquired, &expires
	return l, nil
}

// Acquire takes the app's lease for holder, or renews it if holder already
// has it. held is the other holder's live lease when the app is taken.
func (r *BuildLockRepo) Acquire(appID, holder, description string, ttl time.Duration) (lock, held *ct.BuildLock, err error) {
	secs := int(ttl / time.Second)
	if secs < 1 {
		secs = 1
	}
	// The insert and the read of the other holder are separate statements;
	// a lease that expires or is released in between is retried.
	for attempt := 0; attempt < 3; attempt++ {
		lock, err = scanBuildLock(r.db.QueryRow("app_build_lock_acquire", appID, holder, description, secs))
		if err == nil {
			return lock, nil, nil
		}
		if err != ErrNotFound {
			return nil, nil, err
		}
		held, err = scanBuildLock(r.db.QueryRow("app_build_lock_select", appID))
		if err == nil {
			return nil, held, nil
		}
		if err != ErrNotFound {
			return nil, nil, err
		}
	}
	return nil, nil, ErrNotFound
}

// Release drops the app's lease if holder has it.
func (r *BuildLockRepo) Release(appID, holder string) error {
	return r.db.Exec("app_build_lock_release", appID, holder)
}
