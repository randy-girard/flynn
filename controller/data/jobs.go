package data

import (
	"strings"

	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/postgres"
)

/* Job Stuff */
type JobRepo struct {
	db *postgres.DB
}

func NewJobRepo(db *postgres.DB) *JobRepo {
	return &JobRepo{db}
}

func (r *JobRepo) Get(id string) (*ct.Job, error) {
	if !idPattern.MatchString(id) {
		var err error
		id, err = cluster.ExtractUUID(id)
		if err != nil {
			return nil, ErrNotFound
		}
	}
	row := r.db.QueryRow("job_select", id)
	return scanJob(row)
}

// GetInApp resolves a UUID, cluster ID, or short name (web.4821) for one app.
func (r *JobRepo) GetInApp(appID, id string) (*ct.Job, error) {
	if job, err := r.Get(id); err == nil {
		if appID == "" || job.AppID == appID {
			return job, nil
		}
	}
	jobs, err := r.List(appID)
	if err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	var named *ct.Job
	for _, job := range jobs {
		if job.UUID == id || job.ID == id {
			return job, nil
		}
		if JobDisplayOrStored(job) == id {
			if named == nil || !job.IsDown() {
				cp := *job
				named = &cp
				if !job.IsDown() {
					return named, nil
				}
			}
		}
	}
	if named != nil {
		return named, nil
	}
	return nil, ErrNotFound
}

func JobDisplayOrStored(job *ct.Job) string {
	if job == nil {
		return ""
	}
	if job.Name != "" {
		return job.Name
	}
	return ct.JobNameFromMeta(job.Meta)
}

func (r *JobRepo) Add(job *ct.Job) error {
	ct.EnsureJobName(job)
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}

	// TODO: actually validate
	err = tx.QueryRow(
		"job_insert",
		job.ID,
		job.UUID,
		job.HostID,
		job.AppID,
		job.ReleaseID,
		job.Type,
		string(job.State),
		job.Meta,
		job.ExitStatus,
		job.HostError,
		job.RunAt,
		job.Restarts,
		job.Args,
	).Scan(&job.CreatedAt, &job.UpdatedAt)
	if postgres.IsPostgresCode(err, postgres.CheckViolation) {
		tx.Rollback()
		return ct.ValidationError{Field: "state", Message: err.Error()}
	}
	if err != nil {
		tx.Rollback()
		return err
	}

	for i, volID := range job.VolumeIDs {
		if err := tx.Exec("job_volume_insert", job.UUID, volID, i); err != nil {
			tx.Rollback()
			return err
		}
	}

	// create a job event, ignoring possible duplications
	uniqueID := strings.Join([]string{job.UUID, string(job.State)}, "|")
	if err := tx.Exec("event_insert_unique", job.AppID, job.UUID, uniqueID, string(ct.EventTypeJob), job); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

func scanJob(s postgres.Scanner) (*ct.Job, error) {
	job := &ct.Job{}
	var state string
	var volumeIDs string
	err := s.Scan(
		&job.ID,
		&job.UUID,
		&job.HostID,
		&job.AppID,
		&job.ReleaseID,
		&job.Type,
		&state,
		&job.Meta,
		&job.ExitStatus,
		&job.HostError,
		&job.RunAt,
		&job.Restarts,
		&job.CreatedAt,
		&job.UpdatedAt,
		&job.Args,
		&volumeIDs,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			err = ErrNotFound
		}
		return nil, err
	}
	job.State = ct.JobState(state)
	if volumeIDs != "" {
		job.VolumeIDs = split(volumeIDs[1:len(volumeIDs)-1], ",")
	}
	ct.EnsureJobName(job)
	return job, nil
}

func (r *JobRepo) List(appID string) ([]*ct.Job, error) {
	rows, err := r.db.Query("job_list", appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []*ct.Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (r *JobRepo) ListActive() ([]*ct.Job, error) {
	rows, err := r.db.Query("job_list_active")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []*ct.Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}
