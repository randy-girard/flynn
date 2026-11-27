package data

import (
	"time"

	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/volume"
	"github.com/randy-girard/flynn/pkg/postgres"
)

type VolumeRepo struct {
	db *postgres.DB
}

func NewVolumeRepo(db *postgres.DB) *VolumeRepo {
	return &VolumeRepo{db}
}

func (r *VolumeRepo) Get(appID, volID string) (*ct.Volume, error) {
	row := r.db.QueryRow("volume_select", appID, volID)
	return scanVolume(row)
}

func (r *VolumeRepo) Add(vol *ct.Volume) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}

	err = tx.QueryRow("volume_insert",
		vol.ID,
		vol.HostID,
		string(vol.Type),
		string(vol.State),
		vol.AppID,
		vol.ReleaseID,
		vol.JobID,
		vol.JobType,
		vol.Path,
		vol.DeleteOnStop,
		vol.Meta,
		vol.Size,
	).Scan(&vol.CreatedAt, &vol.UpdatedAt)
	if err != nil {
		tx.Rollback()
		return err
	}

	if err := CreateEvent(tx.Exec, &ct.Event{
		AppID:      vol.AppID,
		ObjectID:   vol.ID,
		ObjectType: ct.EventTypeVolume,
	}, vol); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

func scanVolume(s postgres.Scanner) (*ct.Volume, error) {
	vol := &ct.Volume{}
	var typ, state string
	err := s.Scan(
		&vol.ID,
		&vol.HostID,
		&typ,
		&state,
		&vol.AppID,
		&vol.ReleaseID,
		&vol.JobID,
		&vol.JobType,
		&vol.Path,
		&vol.DeleteOnStop,
		&vol.Meta,
		&vol.Size,
		&vol.CreatedAt,
		&vol.UpdatedAt,
		&vol.DecommissionedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			err = ErrNotFound
		}
		return nil, err
	}
	vol.Type = volume.VolumeType(typ)
	vol.State = ct.VolumeState(state)
	return vol, nil
}

func (r *VolumeRepo) List() ([]*ct.Volume, error) {
	rows, err := r.db.Query("volume_list")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVolumes(rows)
}

func (r *VolumeRepo) AppList(appID string) ([]*ct.Volume, error) {
	rows, err := r.db.Query("volume_app_list", appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVolumes(rows)
}

// ListSince is the first list of the scheduler's volume stream, bounded on
// the server by streamListTimeout.
func (r *VolumeRepo) ListSince(since time.Time) ([]*ct.Volume, error) {
	tx, err := beginBoundedRead(r.db, streamListTimeout)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query("volume_list_since", since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVolumes(rows)
}

func scanVolumes(rows *pgx.Rows) ([]*ct.Volume, error) {
	var volumes []*ct.Volume
	for rows.Next() {
		volume, err := scanVolume(rows)
		if err != nil {
			return nil, err
		}
		volumes = append(volumes, volume)
	}
	return volumes, rows.Err()
}

func (r *VolumeRepo) Decommission(appID string, vol *ct.Volume) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}

	err = tx.QueryRow("volume_decommission", appID, vol.ID).Scan(&vol.UpdatedAt, &vol.DecommissionedAt)
	if err != nil {
		tx.Rollback()
		return err
	}

	if err := CreateEvent(tx.Exec, &ct.Event{
		AppID:      appID,
		ObjectID:   vol.ID,
		ObjectType: ct.EventTypeVolume,
	}, vol); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

// DecommissionAppVolumes marks every volume of appID that is neither
// destroyed nor already decommissioned as decommissioned, in tx, and writes a
// volume event for each so the scheduler learns of it from the stream. The
// scheduler then destroys each one on its host once no job holds it
// (NEW-35). It returns the volumes it changed.
func DecommissionAppVolumes(tx *postgres.DBTx, appID string) ([]*ct.Volume, error) {
	rows, err := tx.Query("volume_decommission_app", appID)
	if err != nil {
		return nil, err
	}
	vols, err := scanVolumes(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, vol := range vols {
		if err := CreateEvent(tx.Exec, &ct.Event{
			AppID:      appID,
			ObjectID:   vol.ID,
			ObjectType: ct.EventTypeVolume,
		}, vol); err != nil {
			return nil, err
		}
	}
	return vols, nil
}

// ListOrphaned lists volumes of apps deleted at least grace ago that were
// never decommissioned or destroyed, leaving out apps with the keep-volumes
// meta (ct.AppMetaKeepVolumes).
func (r *VolumeRepo) ListOrphaned(grace time.Duration) ([]*ct.Volume, error) {
	if grace < 0 {
		grace = 0
	}
	rows, err := r.db.Query("volume_list_orphaned", int64(grace/time.Second))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVolumes(rows)
}
