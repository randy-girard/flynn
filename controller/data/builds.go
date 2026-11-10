package data

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/controller/buildadmit"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/random"
)

const (
	defaultBuildTimeoutSeconds = 15 * 60
	maxBuildTimeoutSeconds     = 30 * 60
)

// CreateBuild inserts a pending compile and its private spec in one
// transaction. The advisory lock and the in-flight count run before the
// insert, so two creates cannot both pass a full cap.
func (r *JobRepo) CreateBuild(app *ct.App, req *ct.BuildRequest, limits buildadmit.Limits) (*ct.Job, error) {
	if app == nil || req == nil {
		return nil, ct.ValidationError{Field: "build", Message: "is required"}
	}
	if !ct.IsBuilderProcessType(req.Type) {
		return nil, ct.ValidationError{Field: "type", Message: "must be slugbuilder or dockerbuilder"}
	}
	if strings.TrimSpace(req.ReleaseID) == "" {
		return nil, ct.ValidationError{Field: "release", Message: "is required"}
	}
	if req.Artifact == nil || req.Artifact.ID == "" {
		return nil, ct.ValidationError{Field: "artifact", Message: "is required"}
	}
	owner := buildadmit.NormalizeOwner(app.OwnerAccount)
	env := map[string]string{}
	for k, v := range req.Env {
		if k == "CONTROLLER_KEY" {
			continue
		}
		env[k] = v
	}
	timeout := req.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultBuildTimeoutSeconds
	}
	if timeout > maxBuildTimeoutSeconds {
		timeout = maxBuildTimeoutSeconds
	}
	args := req.Args
	if len(args) == 0 {
		args = []string{"/builder/build.sh"}
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	artifactJSON, err := json.Marshal(req.Artifact)
	if err != nil {
		return nil, err
	}
	resourcesJSON, err := json.Marshal(req.Resources)
	if err != nil {
		return nil, err
	}
	if req.Resources == nil {
		resourcesJSON = []byte("{}")
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}

	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if err := tx.QueryRow("SELECT pg_advisory_xact_lock($1)::text", buildadmit.CreateLockID).Scan(new(string)); err != nil {
		return nil, err
	}
	var raw string
	if err := tx.QueryRow(`
SELECT COALESCE((
  SELECT json_object_agg(owner, n)
  FROM (
    SELECT COALESCE(NULLIF(btrim(a.owner_account), ''), '') AS owner, COUNT(*)::int AS n
    FROM job_cache j
    JOIN apps a ON a.app_id = j.app_id AND a.deleted_at IS NULL
    WHERE j.state IN ('pending', 'blocked', 'starting', 'up')
      AND (
        lower(j.process_type) = 'slugbuilder' OR lower(j.process_type) = 'dockerbuilder'
        OR lower(j.process_type) LIKE 'slugbuilder-%' OR lower(j.process_type) LIKE 'dockerbuilder-%'
      )
    GROUP BY 1
  ) counts
), '{}'::json)::text`).Scan(&raw); err != nil {
		return nil, err
	}
	counts := map[string]int{}
	if err := json.Unmarshal([]byte(raw), &counts); err != nil {
		return nil, err
	}
	var existing []buildadmit.Job
	for ownerKey, n := range counts {
		for i := 0; i < n; i++ {
			existing = append(existing, buildadmit.Job{
				ID:    ownerKey + "-" + random.UUID(),
				Type:  "slugbuilder",
				Owner: ownerKey,
				State: buildadmit.StatePending,
			})
		}
	}
	if err := buildadmit.CanCreate(existing, owner, limits); err != nil {
		return nil, err
	}

	job := &ct.Job{
		UUID:      random.UUID(),
		AppID:     app.ID,
		ReleaseID: req.ReleaseID,
		Type:      req.Type,
		State:     ct.JobStatePending,
		Args:      args,
		Meta: map[string]string{
			ct.MetaBuildOwner: owner,
		},
	}
	ct.EnsureJobName(job)
	if err := tx.QueryRow(
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
	).Scan(&job.CreatedAt, &job.UpdatedAt); err != nil {
		return nil, err
	}
	if err := tx.Exec(`
INSERT INTO build_specs (job_id, env, artifact, resources, args, app_name, timeout_seconds)
VALUES ($1, $2::jsonb, $3, $4, $5, $6, $7)`,
		job.UUID, string(envJSON), string(artifactJSON), string(resourcesJSON), string(argsJSON), app.Name, timeout,
	); err != nil {
		return nil, err
	}
	uniqueID := strings.Join([]string{job.UUID, string(job.State)}, "|")
	if err := tx.Exec("event_insert_unique", job.AppID, job.UUID, uniqueID, string(ct.EventTypeJob), job); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return job, nil
}

// GetBuildSpec returns the private compile payload for one job.
func (r *JobRepo) GetBuildSpec(jobID string) (*ct.BuildSpec, error) {
	var envRaw, artifact, resources, args, appName string
	var timeout int
	err := r.db.QueryRow("build_spec_select", jobID).Scan(&envRaw, &artifact, &resources, &args, &appName, &timeout)
	if err != nil {
		if err == pgx.ErrNoRows || err == ErrNotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}
	spec := &ct.BuildSpec{
		JobID:          jobID,
		AppName:        appName,
		Env:            map[string]string{},
		Args:           []string{},
		TimeoutSeconds: timeout,
	}
	if envRaw != "" && envRaw != "null" {
		if err := json.Unmarshal([]byte(envRaw), &spec.Env); err != nil {
			return nil, err
		}
	}
	if artifact != "" {
		spec.Artifact = &ct.Artifact{}
		if err := json.Unmarshal([]byte(artifact), spec.Artifact); err != nil {
			return nil, err
		}
	}
	if resources != "" && resources != "{}" {
		if err := json.Unmarshal([]byte(resources), &spec.Resources); err != nil {
			return nil, err
		}
	}
	if args != "" {
		if err := json.Unmarshal([]byte(args), &spec.Args); err != nil {
			return nil, err
		}
	}
	return spec, nil
}

// ListPendingBuilds returns in-flight compiles, oldest first. Running and
// blocked rows are included so a scheduler restart cannot admit past the cap.
func (r *JobRepo) ListPendingBuilds() ([]*ct.Job, error) {
	rows, err := r.db.Query("build_pending_list")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobRows(rows)
}

// MarkUnplacedBuildDown marks a compile down when it has no host yet.
func (r *JobRepo) MarkUnplacedBuildDown(job *ct.Job) error {
	if job == nil {
		return ErrNotFound
	}
	job.State = ct.JobStateDown
	now := time.Now()
	job.UpdatedAt = &now
	return r.Add(job)
}
