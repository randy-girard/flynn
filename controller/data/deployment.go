package data

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/flynn/que-go"
	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/controller/schema"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/random"
)

type DeploymentRepo struct {
	db            *postgres.DB
	q             *que.Client
	appRepo       *AppRepo
	releaseRepo   *ReleaseRepo
	formationRepo *FormationRepo
}

func NewDeploymentRepo(db *postgres.DB, appRepo *AppRepo, releaseRepo *ReleaseRepo, formationRepo *FormationRepo) *DeploymentRepo {
	q := que.NewClient(db.ConnPool)
	return &DeploymentRepo{db: db, q: q, appRepo: appRepo, releaseRepo: releaseRepo, formationRepo: formationRepo}
}

func (r *DeploymentRepo) Add(appID, releaseID string) (*ct.Deployment, error) {
	ed, err := r.AddExpanded(appID, releaseID)
	if err != nil {
		return nil, err
	}
	var oldReleaseID string
	if ed.OldRelease != nil {
		oldReleaseID = ed.OldRelease.ID
	}
	return &ct.Deployment{
		ID:            ed.ID,
		AppID:         ed.AppID,
		OldReleaseID:  oldReleaseID,
		NewReleaseID:  ed.NewRelease.ID,
		Type:          ed.Type,
		Strategy:      ed.Strategy,
		Status:        ed.Status,
		Processes:     ed.Processes,
		Tags:          ed.Tags,
		DeployTimeout: ed.DeployTimeout,
		CreatedAt:     ed.CreatedAt,
		FinishedAt:    ed.FinishedAt,
	}, nil
}

func (r *DeploymentRepo) AddExpanded(appID, releaseID string) (*ct.ExpandedDeployment, error) {
	return r.addExpanded(appID, releaseID, false)
}

func (r *DeploymentRepo) addExpanded(appID, releaseID string, retried bool) (*ct.ExpandedDeployment, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}

	app, err := selectApp(tx, appID, true)
	if err != nil {
		if err == ErrNotFound {
			err = ct.ValidationError{
				Message: fmt.Sprintf("could not find app with ID %s", appID),
			}
		}
		tx.Rollback()
		return nil, err
	}

	release, err := r.releaseRepo.TxGet(tx, releaseID)
	if err != nil {
		if err == ErrNotFound {
			err = ct.ValidationError{
				Message: fmt.Sprintf("could not find release with ID %s", releaseID),
			}
		}
		tx.Rollback()
		return nil, err
	}
	if !release.HasDeployableBlob() {
		tx.Rollback()
		return nil, ct.ValidationError{Message: "release blob was garbage-collected"}
	}

	oldRelease, err := r.appRepo.TxGetRelease(tx, app.ID)
	if err == ErrNotFound {
		oldRelease = nil
	} else if err != nil {
		tx.Rollback()
		return nil, err
	}

	oldFormation := &ct.Formation{}
	if oldRelease != nil {
		f, err := r.formationRepo.TxGet(tx, app.ID, oldRelease.ID)
		if err == nil {
			oldFormation = f
		} else if err != ErrNotFound {
			tx.Rollback()
			return nil, err
		}
	}
	head, err := r.txLatestUnfinished(tx, app.ID)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	if head != nil {
		headRelease, err := r.releaseRepo.TxGet(tx, head.NewReleaseID)
		if err != nil && err != ErrNotFound {
			tx.Rollback()
			return nil, err
		}
		if headRelease != nil {
			oldRelease = headRelease
		}
		if len(head.Processes) > 0 {
			oldFormation.Processes = head.Processes
		}
		if len(head.Tags) > 0 && oldFormation.Tags == nil {
			oldFormation.Tags = head.Tags
		}
	}
	procCount := 0
	for _, i := range oldFormation.Processes {
		procCount += i
	}

	releaseType := ct.ReleaseTypeCode
	if oldRelease != nil {
		if artifactIDsEqual(oldRelease.ArtifactIDs, release.ArtifactIDs) {
			releaseType = ct.ReleaseTypeConfig
		}
	} else if len(release.ArtifactIDs) == 0 {
		releaseType = ct.ReleaseTypeConfig
	}

	ed := &ct.ExpandedDeployment{
		AppID:         app.ID,
		NewRelease:    release,
		Type:          releaseType,
		Strategy:      app.Strategy,
		OldRelease:    oldRelease,
		Processes:     oldFormation.Processes,
		Tags:          oldFormation.Tags,
		DeployTimeout: app.DeployTimeout,
	}

	d := &ct.Deployment{
		AppID:           app.ID,
		NewReleaseID:    release.ID,
		Strategy:        app.Strategy,
		Processes:       oldFormation.Processes,
		Tags:            oldFormation.Tags,
		DeployTimeout:   app.DeployTimeout,
		DeployBatchSize: app.DeployBatchSize(),
	}
	if oldRelease != nil {
		d.OldReleaseID = oldRelease.ID
	}

	if err := schema.Validate(d); err != nil {
		tx.Rollback()
		return nil, err
	}
	if procCount == 0 && head == nil {
		// immediately set app release
		if err := r.appRepo.TxSetRelease(tx, app, release.ID); err != nil {
			tx.Rollback()
			return nil, err
		}
		now := time.Now().Truncate(time.Microsecond) // postgres only has microsecond precision
		d.FinishedAt = &now
		ed.FinishedAt = &now
	}

	var oldReleaseID *string
	if oldRelease != nil {
		oldReleaseID = &oldRelease.ID
	}
	if d.ID == "" {
		d.ID = random.UUID()
	}
	ed.ID = d.ID
	queue := head != nil && d.FinishedAt == nil
	var startedAt *time.Time
	if !queue && d.FinishedAt == nil {
		now := time.Now()
		startedAt = &now
	}
	if err := tx.QueryRow("deployment_insert", d.ID, d.AppID, oldReleaseID, d.NewReleaseID, string(releaseType), d.Strategy, d.Processes, d.Tags, d.DeployTimeout, d.DeployBatchSize, startedAt).Scan(&d.CreatedAt); err != nil {
		tx.Rollback()
		if postgres.IsUniquenessError(err, "isolate_deploys") {
			if !retried {
				_ = r.releaseStaleOpenDeploy(appID)
				return r.addExpanded(appID, releaseID, true)
			}
			return nil, ct.ValidationError{Message: ct.DeployInProgressMessage}
		}
		return nil, err
	}
	ed.CreatedAt = d.CreatedAt

	// fake initial deployment
	if d.FinishedAt != nil {
		if err := tx.Exec("deployment_update_finished_at", d.ID, d.FinishedAt); err != nil {
			tx.Rollback()
			return nil, err
		}
		if err = createDeploymentEvent(tx.Exec, d, "complete"); err != nil {
			tx.Rollback()
			return nil, err
		}
		ed.Status = "complete"
		return ed, tx.Commit()
	}

	args, err := json.Marshal(ct.DeployID{ID: d.ID})
	if err != nil {
		tx.Rollback()
		return nil, err
	}

	status := "pending"
	if queue {
		status = "queued"
	}
	if err = createDeploymentEvent(tx.Exec, d, status); err != nil {
		tx.Rollback()
		return nil, err
	}
	ed.Status = status
	if queue {
		return ed, tx.Commit()
	}

	job := &que.Job{Type: "deployment", Args: args}
	if err := r.q.EnqueueInTx(job, tx.Tx); err != nil {
		tx.Rollback()
		return nil, err
	}
	return ed, tx.Commit()
}

func (r *DeploymentRepo) Get(id string) (*ct.Deployment, error) {
	row := r.db.QueryRow("deployment_select", id)
	return scanDeployment(row)
}

func (r *DeploymentRepo) GetExpanded(id string) (*ct.ExpandedDeployment, error) {
	row := r.db.QueryRow("deployment_select_expanded", id)
	return scanExpandedDeployment(row)
}

func (r *DeploymentRepo) List(appID string) ([]*ct.Deployment, error) {
	rows, err := r.db.Query("deployment_list", appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deployments []*ct.Deployment
	for rows.Next() {
		deployment, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		deployments = append(deployments, deployment)
	}
	return deployments, rows.Err()
}

func (r *DeploymentRepo) ListCount(appID string, before *time.Time, beforeID string, count int) ([]*ct.Deployment, error) {
	var ts interface{}
	if before != nil {
		ts = *before
	}
	rows, err := r.db.Query("deployment_list_count", appID, ts, beforeID, count)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deployments []*ct.Deployment
	for rows.Next() {
		deployment, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		deployments = append(deployments, deployment)
	}
	return deployments, rows.Err()
}

type ListDeploymentOptions struct {
	PageToken     PageToken
	AppIDs        []string
	DeploymentIDs []string
	StatusFilters []string
	TypeFilters   []ct.ReleaseType
}

func (r *DeploymentRepo) ListPage(opts ListDeploymentOptions) ([]*ct.ExpandedDeployment, *PageToken, error) {
	pageSize := DEFAULT_PAGE_SIZE
	if opts.PageToken.Size > 0 {
		pageSize = opts.PageToken.Size
	}
	typeFilters := make([]string, 0, len(opts.TypeFilters))
	for _, t := range opts.TypeFilters {
		if t == ct.ReleaseTypeAny {
			typeFilters = []string{}
			break
		}
		typeFilters = append(typeFilters, string(t))
	}
	cursor, err := opts.PageToken.Cursor()
	if err != nil {
		return nil, nil, err
	}
	rows, err := r.db.Query("deployment_list_page", opts.AppIDs, opts.DeploymentIDs, opts.StatusFilters, typeFilters, cursor, pageSize+1)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var deployments []*ct.ExpandedDeployment
	for rows.Next() {
		deployment, err := scanExpandedDeployment(rows)
		if err != nil {
			return nil, nil, err
		}
		deployments = append(deployments, deployment)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var nextPageToken *PageToken
	if len(deployments) == pageSize+1 {
		nextPageToken = &PageToken{
			CursorID: toCursorID(deployments[pageSize].CreatedAt),
			Size:     pageSize,
		}
		deployments = deployments[0:pageSize]
	}
	return deployments, nextPageToken, nil
}

func scanExpandedDeployment(s postgres.Scanner) (*ct.ExpandedDeployment, error) {
	d := &ct.ExpandedDeployment{}
	oldRelease := &ct.Release{}
	newRelease := &ct.Release{}
	var oldArtifactIDs string
	var newArtifactIDs string
	var oldReleaseID *string
	var status *string
	err := s.Scan(
		&d.ID, &d.AppID, &oldReleaseID, &newRelease.ID, &d.Strategy, &status, &d.Processes, &d.Tags, &d.DeployTimeout, &d.DeployBatchSize, &d.CreatedAt, &d.FinishedAt,
		&oldArtifactIDs, &oldRelease.Env, &oldRelease.Processes, &oldRelease.Meta, &oldRelease.CreatedAt,
		&newArtifactIDs, &newRelease.Env, &newRelease.Processes, &newRelease.Meta, &newRelease.CreatedAt,
		&d.Type,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			err = ErrNotFound
		}
		return nil, err
	}
	if oldReleaseID != nil {
		oldRelease.ID = *oldReleaseID
		oldRelease.AppID = d.AppID
		if oldArtifactIDs != "" {
			oldRelease.ArtifactIDs = splitPGStringArray(oldArtifactIDs)
		}
		d.OldRelease = oldRelease
	}
	if newArtifactIDs != "" {
		newRelease.ArtifactIDs = splitPGStringArray(newArtifactIDs)
	}
	newRelease.AppID = d.AppID
	d.NewRelease = newRelease
	if status != nil {
		d.Status = *status
	}
	return d, err
}

func scanDeployment(s postgres.Scanner) (*ct.Deployment, error) {
	d := &ct.Deployment{}
	var oldReleaseID *string
	var status *string
	var relType *string
	err := s.Scan(&d.ID, &d.AppID, &oldReleaseID, &d.NewReleaseID, &d.Strategy, &status, &d.Processes, &d.Tags, &d.DeployTimeout, &d.DeployBatchSize, &d.CreatedAt, &d.FinishedAt, &relType)
	if err == pgx.ErrNoRows {
		err = ErrNotFound
	}
	if oldReleaseID != nil {
		d.OldReleaseID = *oldReleaseID
	}
	if status != nil {
		d.Status = *status
	}
	if relType != nil {
		d.Type = ct.ReleaseType(*relType)
	}
	return d, err
}

func createDeploymentEvent(dbExec func(string, ...interface{}) error, d *ct.Deployment, status string) error {
	e := ct.DeploymentEvent{
		AppID:        d.AppID,
		DeploymentID: d.ID,
		ReleaseID:    d.NewReleaseID,
		Status:       status,
	}
	return CreateEvent(dbExec, &ct.Event{
		AppID:      d.AppID,
		ObjectID:   d.ID,
		ObjectType: ct.EventTypeDeployment,
		Op:         ct.EventOpCreate,
	}, e)
}

func artifactIDsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
