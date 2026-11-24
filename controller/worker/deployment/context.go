package deployment

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/flynn/que-go"
	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/attempt"
	"github.com/randy-girard/flynn/pkg/postgres"
)

type context struct {
	client controller.Client
	logger log15.Logger
	store  deploymentStore
}

func JobHandler(db *postgres.DB, client controller.Client, logger log15.Logger) func(*que.Job) error {
	return (&context{client: client, logger: logger, store: &pgDeploymentStore{db}}).HandleDeployment
}

// errRollbackInProgress is returned to que when another worker has started
// rolling the deployment back: the job stays queued and is retried later,
// when the deployment is finished (or the rollback is taken over).
var errRollbackInProgress = errors.New("another worker is rolling this deployment back")

// rollbackTakeoverAfter is how old a rollback marker must be before a worker
// that claims the job assumes the worker that wrote it is gone and rolls the
// deployment back itself. A rollback takes at most a few minutes (the
// restore wait, 30 s of delete retries, the sirenia waits of up to 2 min,
// 60 s to write the outcome).
const rollbackTakeoverAfter = 10 * time.Minute

// HandleDeployment runs a deployment job. The deployment's outcome and its
// finished_at are written together, last (EDGE-75): after the rollback of a
// failed deploy, and after SetAppRelease of a successful one. A reader that
// sees finished_at therefore sees status complete or failed, and the app's
// next queued deployment starts only once the rollback is done.
//
// Before rolling back, the worker records that it is doing so. que's lock is
// a Postgres session lock: when the platform Postgres primary moves, another
// worker can claim the same job while this one is still rolling back. That
// claim finds the marker and does not run Perform again.
func (c *context) HandleDeployment(job *que.Job) (e error) {
	log := c.logger.New("fn", "HandleDeployment")
	log.Info("handling deployment", "job_id", job.ID, "error_count", job.ErrorCount)

	var args ct.DeployID
	if err := json.Unmarshal(job.Args, &args); err != nil {
		log.Error("error unmarshaling job", "err", err)
		return err
	}

	log.Info("getting deployment record", "deployment_id", args.ID)
	deployment, err := c.client.GetDeployment(args.ID)
	if err == controller.ErrNotFound {
		log.Error("deployment record not found, dropping the job", "deployment_id", args.ID)
		return nil
	}
	if err != nil {
		// Nothing has been done yet: let que retry. (This used to set
		// finished_at with no outcome; a deployment no worker can read
		// is finished by the stale-deploy repair, deployment_stale.go.)
		log.Error("error getting deployment record", "deployment_id", args.ID, "err", err)
		return err
	}
	if deployment.FinishedAt != nil {
		log.Info("deployment already finished")
		return nil
	}

	log = log.New(
		"deployment_id", deployment.ID,
		"app_id", deployment.AppID,
		"strategy", deployment.Strategy,
	)

	startedAt, startErr, rollingBack, err := c.store.RollbackStarted(deployment.ID)
	if err != nil {
		log.Error("error reading the deployment's rollback marker", "err", err)
		return err
	}
	if rollingBack {
		return c.resumeRollback(log, deployment, startedAt, startErr, job.Stop)
	}

	// Buffer job events so a hung event_insert (postgres primary restart
	// during a sirenia deploy) cannot stall Perform on an unbuffered send.
	events := make(chan ct.DeploymentEvent, 32)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		log.Info("watching deployment events")
		for ev := range events {
			log.Info("received deployment event", "status", ev.Status, "type", ev.JobType, "state", ev.JobState)
			ev.AppID = deployment.AppID
			ev.DeploymentID = deployment.ID
			if err := c.createDeploymentEvent(ev); err != nil {
				log.Error("error creating deployment event record", "err", err)
			}
		}
		log.Info("stopped watching deployment events")
	}()
	// closeEvents waits until every progress event is written, so the
	// rollback marker and the outcome are the deployment's latest events
	// (deployment_status() is the latest event's status).
	var closeOnce sync.Once
	closeEvents := func() {
		closeOnce.Do(func() {
			close(events)
			<-drained
		})
	}
	defer closeEvents()

	var f *ct.Formation
	defer func() {
		if deploymentOutcomeFor(e) == outcomeLeaveRunning {
			return
		}
		closeEvents()
		status, errMsg := "complete", ""
		if e != nil {
			status, errMsg = "failed", e.Error()
			switch {
			case IsSkipRollback(e):
				// ErrSkipRollback indicates the deploy failed in some way
				// but no further action should be taken, so set the error
				// to nil to avoid retrying the deploy
				e = nil
			case f != nil:
				if err := c.store.MarkRollingBack(deployment, errMsg); err != nil {
					// Without the marker, a worker that claimed the job
					// during the rollback would run Perform again. Roll
					// nothing back, finish nothing, and let que retry.
					log.Error("error recording the rollback, leaving the deployment for a retry", "err", err)
					e = fmt.Errorf("%s; could not record the rollback: %v", errMsg, err)
					return
				}
				log.Warn("rolling back deployment due to error", "err", e)
				e = c.rollback(log, deployment, f, job.Stop)
				if e != nil {
					// The failed event is what flynn-host update and
					// `flynn deployment` show: say what the rollback left.
					errMsg += "; rollback: " + e.Error()
				}
			default:
				e = nil
			}
		}
		if err := c.finish(log, deployment, status, errMsg); err != nil && e == nil {
			// isolate_deploys is WHERE finished_at IS NULL: a retry
			// must finish it (HA 2026-09-27: each controller process
			// opened a 20-conn pool against a 20-conn role limit).
			e = err
		}
	}()

	// for recovery purposes, fetch old formation
	log.Info("getting old formation")
	err = retryAPICall("get old formation "+deployment.OldReleaseID, apiRetryBudget, job.Stop, log, func() (err error) {
		f, err = c.client.GetFormation(deployment.AppID, deployment.OldReleaseID)
		return err
	})
	if err != nil {
		log.Error("error getting old formation", "release_id", deployment.OldReleaseID, "err", err)
		return err
	}

	j := &DeployJob{
		Deployment:   deployment,
		client:       c.client,
		deployEvents: events,
		logger:       c.logger,
		stop:         job.Stop,
	}

	log.Info("performing deployment")
	if err := j.Perform(); err != nil {
		log.Error("error performing deployment", "err", err)
		return err
	}
	log.Info("setting the app release")
	// Every process has rolled by now: a transient error here must not roll
	// the finished deploy back (EDGE-65).
	if err := retryAPICall("set the app release", apiRetryBudget, job.Stop, log, func() error {
		return c.client.SetAppRelease(deployment.AppID, deployment.NewReleaseID)
	}); err != nil {
		log.Error("error setting the app release", "err", err)
		return err
	}
	log.Info("deployment complete")

	c.zeroFailedDeploymentFormations(log, deployment)

	log.Info("scheduling app garbage collection")
	if err := c.client.ScheduleAppGarbageCollection(deployment.AppID); err != nil {
		// just log the error, no need to rollback the deploy
		log.Error("error scheduling app garbage collection", "err", err)
	}

	return nil
}

// finish writes finished_at with the outcome in one write, then starts the
// app's next queued deployment.
func (c *context) finish(log log15.Logger, deployment *ct.Deployment, status, errMsg string) error {
	log.Info("marking the deployment as done", "status", status)
	finished, err := c.store.Finish(deployment.ID, status, errMsg)
	if err != nil {
		log.Error("error marking the deployment as done", "err", err)
		return err
	}
	if !finished {
		log.Warn("the deployment was already finished, not writing an outcome", "status", status)
		return nil
	}
	if err := c.store.StartNextQueued(deployment.AppID); err != nil {
		log.Error("error starting the next queued deployment", "err", err)
	}
	c.clearAppBuilding(deployment.AppID)
	return nil
}

// resumeRollback handles a claim of a deployment that a worker already
// started rolling back. It never runs Perform. While the marker is recent
// the rollback belongs to that worker, and the job is retried later. An old
// marker means that worker is gone: roll back again from the deployment's
// original formation (restoring it and deleting the new one are both safe to
// repeat; the sirenia rollback reads the cluster again) and finish the
// deployment as failed.
func (c *context) resumeRollback(log log15.Logger, deployment *ct.Deployment, startedAt time.Time, deployErr string, stop chan struct{}) error {
	age := time.Since(startedAt)
	if age < rollbackTakeoverAfter {
		log.Warn("another worker is rolling this deployment back, not performing it again", "rollback.started_at", startedAt, "age", age)
		return errRollbackInProgress
	}
	log.Warn("the worker that started rolling this deployment back did not finish, rolling it back again", "rollback.started_at", startedAt, "age", age)
	errMsg := deployErr
	if errMsg == "" {
		errMsg = "deployment failed"
	}
	if err := c.store.MarkRollingBack(deployment, errMsg); err != nil {
		log.Error("error recording the rollback", "err", err)
		return err
	}
	errMsg += "; the worker rolling it back did not finish, so it was rolled back again"
	original := &ct.Formation{
		AppID:     deployment.AppID,
		ReleaseID: deployment.OldReleaseID,
		Processes: deployment.Processes,
		Tags:      deployment.Tags,
	}
	rbErr := c.rollback(log, deployment, original, stop)
	if rbErr != nil {
		errMsg += "; rollback: " + rbErr.Error()
	}
	if err := c.finish(log, deployment, "failed", errMsg); err != nil {
		return err
	}
	return rbErr
}

func (c *context) rollback(l log15.Logger, deployment *ct.Deployment, original *ct.Formation, stop chan struct{}) error {
	log := l.New("fn", "rollback")
	if deployment.Strategy == "sirenia" {
		if handled, err := c.rollbackSirenia(log, deployment, original, stop); handled {
			return err
		}
	}
	if err := c.restoreOriginalFormation(log, original, original.Processes, stop); err != nil {
		return err
	}
	return c.deleteNewFormation(log, deployment)
}

// restoreOriginalFormation scales the old release back to processes with
// the original tags. It returns an error only when the old formation was
// left unchanged; then the new one must not be removed, or the app could be
// left with nothing running (the next successful deploy zeroes this release,
// zeroFailedDeploymentFormations).
func (c *context) restoreOriginalFormation(log log15.Logger, original *ct.Formation, processes map[string]int, stop chan struct{}) error {
	log.Info("restoring the original formation", "release.id", original.ReleaseID, "processes", processes)
	timeout := 10 * time.Second
	restoreWritten := false
	opts := ct.ScaleOptions{
		Processes: processes,
		// An omni roll pins the old formation to the hosts it has not left
		// yet (flynn-host-ids). Restore every process type's original tags
		// so the old release comes back on every host.
		Tags:    rollbackTags(original),
		Timeout: &timeout,
		Stop:    stop,
		ScaleRequestCallback: func(*ct.ScaleRequest) {
			restoreWritten = true
		},
		JobEventCallback: func(job *ct.Job) error {
			log.Info("got job event", "job.id", job.ID, "job.type", job.Type, "job.state", job.State)
			return nil
		},
	}
	// Retry a restore that was not written because of a transient error
	// (EDGE-65); once written, its outcome is handled below.
	var err error
	if rerr := retryAPICall("restore the original formation", apiRetryBudget, stop, log, func() error {
		err = c.client.ScaleAppRelease(original.AppID, original.ReleaseID, opts)
		if err == nil || restoreWritten {
			return nil
		}
		return err
	}); rerr != nil {
		err = rerr
	}
	if err != nil {
		if !restoreWritten || err == ct.ErrScalingStopped {
			log.Error("error restoring the original formation", "err", err)
			return err
		}
		// The restore is written and the scheduler will converge on it.
		// Returning here (EDGE-63) left the new release's formation running
		// for good: the que retry sees finished_at and does nothing.
		log.Warn("original formation restored but not confirmed in time, going on with the rollback", "err", err)
	}
	return nil
}

func (c *context) deleteNewFormation(log log15.Logger, deployment *ct.Deployment) error {
	log.Info("deleting the new formation")
	if err := rollbackDeleteAttempts.Run(func() error {
		err := c.client.DeleteFormation(deployment.AppID, deployment.NewReleaseID)
		if err == controller.ErrNotFound {
			return nil
		}
		return err
	}); err != nil {
		log.Error("error deleting the new formation:", "err", err)
		return err
	}

	log.Info("rollback complete")
	return nil
}

// rollbackDeleteAttempts retries removing the new formation: the controller
// API can be slow to answer while its own release is being rolled back.
var rollbackDeleteAttempts = attempt.Strategy{
	Total: 30 * time.Second,
	Delay: 500 * time.Millisecond,
}

// rollbackTags is the original formation's tags with an entry for every
// process type, so a scale request replaces any tag the deploy set.
func rollbackTags(original *ct.Formation) map[string]map[string]string {
	tags := make(map[string]map[string]string, len(original.Processes)+len(original.Tags))
	for typ := range original.Processes {
		tags[typ] = map[string]string{}
	}
	for typ, t := range original.Tags {
		inner := make(map[string]string, len(t))
		for k, v := range t {
			inner[k] = v
		}
		tags[typ] = inner
	}
	return tags
}

// zeroFailedDeploymentFormations scales to zero the formations of releases
// that a failed deployment of this app targeted and that still run
// processes (EDGE-63: a rollback that stopped early left the controller's
// failed release with scheduler=1 on every host through later deploys).
//
// It must never take capacity away from the app (EDGE-72: a leftover
// release can be the formation that actually runs the app; zeroing one
// stopped every system Postgres engine). A formation is zeroed only when
// all of these hold:
//
//   - the deploy is not a sirenia deploy and no release involved has a
//     process type with volumes: data formations are never scaled here;
//   - its release was never the app's current release (the old release of
//     any deployment) and no deployment completed it, and it is not one of
//     this deploy's releases;
//   - for every process type it runs, the release this deploy just made
//     current has a non-zero count and at least that many jobs up right now
//     (for an omni type: an up job on every host the leftover runs one on).
//
// Anything else is left alone and logged. It runs only after Perform
// succeeded and the new release is current. Errors are logged; the deploy
// itself has succeeded.
func (c *context) zeroFailedDeploymentFormations(l log15.Logger, deployment *ct.Deployment) {
	log := l.New("fn", "zeroFailedDeploymentFormations")
	if deployment.Strategy == "sirenia" {
		log.Info("not cleaning up failed-deployment formations of a sirenia app", "strategy", deployment.Strategy)
		return
	}
	deployments, err := c.client.DeploymentList(deployment.AppID)
	if err != nil {
		log.Error("error listing deployments", "err", err)
		return
	}
	keep := map[string]bool{deployment.NewReleaseID: true, deployment.OldReleaseID: true}
	for _, d := range deployments {
		if d == nil {
			continue
		}
		if d.Status == "complete" {
			keep[d.NewReleaseID] = true
		}
		// A release a deployment started from was the app's current
		// release; the app may still run on it.
		keep[d.OldReleaseID] = true
	}
	seen := make(map[string]bool)
	var targets []string
	for _, d := range deployments {
		if d == nil || d.ID == deployment.ID || d.Status != "failed" {
			continue
		}
		rid := d.NewReleaseID
		if rid == "" || keep[rid] || seen[rid] {
			continue
		}
		seen[rid] = true
		targets = append(targets, rid)
	}
	if len(targets) == 0 {
		return
	}
	sort.Strings(targets)

	newRelease, err := c.client.GetRelease(deployment.NewReleaseID)
	if err != nil {
		log.Error("error getting the new release, leaving failed-deployment formations alone", "release.id", deployment.NewReleaseID, "err", err)
		return
	}
	if hasVolumes(newRelease) {
		log.Info("not cleaning up failed-deployment formations of an app with volumes", "release.id", deployment.NewReleaseID)
		return
	}
	current, err := c.client.GetFormation(deployment.AppID, deployment.NewReleaseID)
	if err == controller.ErrNotFound {
		// A deploy of an app scaled to zero creates no new formation.
		current, err = &ct.Formation{AppID: deployment.AppID, ReleaseID: deployment.NewReleaseID}, nil
	}
	if err != nil {
		log.Error("error getting the new formation, leaving failed-deployment formations alone", "release.id", deployment.NewReleaseID, "err", err)
		return
	}
	jobs, err := c.client.JobList(deployment.AppID)
	if err != nil {
		log.Error("error listing jobs, leaving failed-deployment formations alone", "err", err)
		return
	}

	for _, rid := range targets {
		form, err := c.client.GetFormation(deployment.AppID, rid)
		if err == controller.ErrNotFound {
			continue
		} else if err != nil {
			log.Error("error getting formation", "release.id", rid, "err", err)
			continue
		}
		zero := make(map[string]int, len(form.Processes))
		running := false
		for typ, n := range form.Processes {
			if n > 0 {
				running = true
			}
			zero[typ] = 0
		}
		if !running {
			continue
		}
		if rel, err := c.client.GetRelease(rid); err != nil || hasVolumes(rel) {
			log.Warn("leaving a failed deployment's formation alone: its release has volumes or could not be read", "release.id", rid, "processes", form.Processes, "err", err)
			continue
		}
		if reason := leftoverNotCovered(form, current, newRelease, jobs); reason != "" {
			log.Warn("leaving a failed deployment's formation alone: the current release does not cover it", "release.id", rid, "processes", form.Processes, "current.processes", current.Processes, "reason", reason)
			continue
		}
		log.Warn("scaling down a formation left by a failed deployment", "release.id", rid, "processes", form.Processes, "current.release.id", deployment.NewReleaseID, "current.processes", current.Processes)
		if err := c.client.ScaleAppRelease(deployment.AppID, rid, ct.ScaleOptions{Processes: zero, NoWait: true}); err != nil {
			log.Error("error scaling down a failed deployment's formation", "release.id", rid, "err", err)
		}
	}
}

// leftoverNotCovered says why zeroing leftover could leave a process type
// of the app with fewer jobs than the current release's formation asks for,
// or "" when the current release already runs every type leftover runs.
func leftoverNotCovered(leftover, current *ct.Formation, currentRelease *ct.Release, jobs []*ct.Job) string {
	up := make(map[string]int)
	upHosts := make(map[string]map[string]bool)
	leftHosts := make(map[string]map[string]bool)
	for _, j := range jobs {
		if j == nil || j.State != ct.JobStateUp {
			continue
		}
		switch j.ReleaseID {
		case current.ReleaseID:
			up[j.Type]++
			if upHosts[j.Type] == nil {
				upHosts[j.Type] = make(map[string]bool)
			}
			upHosts[j.Type][j.HostID] = true
		case leftover.ReleaseID:
			if leftHosts[j.Type] == nil {
				leftHosts[j.Type] = make(map[string]bool)
			}
			leftHosts[j.Type][j.HostID] = true
		}
	}
	types := make([]string, 0, len(leftover.Processes))
	for typ, n := range leftover.Processes {
		if n > 0 {
			types = append(types, typ)
		}
	}
	sort.Strings(types)
	for _, typ := range types {
		want := current.Processes[typ]
		if want <= 0 {
			return fmt.Sprintf("the current release runs no %s", typ)
		}
		if up[typ] < want {
			return fmt.Sprintf("the current release has %d of %d %s jobs up", up[typ], want, typ)
		}
		if currentRelease.Processes[typ].Omni {
			for h := range leftHosts[typ] {
				if !upHosts[typ][h] {
					return fmt.Sprintf("the current release has no %s job up on host %s", typ, h)
				}
			}
		}
	}
	return ""
}

func hasVolumes(r *ct.Release) bool {
	if r == nil {
		return false
	}
	for _, p := range r.Processes {
		if len(p.Volumes) > 0 || p.DeprecatedData {
			return true
		}
	}
	return false
}

func (c *context) clearAppBuilding(appID string) {
	if c == nil || c.client == nil || appID == "" {
		return
	}
	app, err := c.client.GetApp(appID)
	if err != nil || app == nil || app.Meta == nil || app.Meta[ct.MetaAppBuilding] == "" {
		return
	}
	app.Meta = ct.ClearAppBuildingMeta(app.Meta)
	if err := c.client.UpdateAppMeta(app); err != nil && c.logger != nil {
		c.logger.Error("error clearing building status", "app_id", appID, "err", err)
	}
}

func (c *context) createDeploymentEvent(e ct.DeploymentEvent) error {
	if e.Status == "" {
		e.Status = "running"
	}
	return c.store.CreateEvent(e)
}

var execAttempts = attempt.Strategy{
	Total: 10 * time.Second,
	Delay: 100 * time.Millisecond,
}

// doneAttempts is longer than execAttempts: marking finished_at (and the
// rollback marker) must survive a brief platform-postgres connection-limit
// storm or isolate_deploys sticks.
var doneAttempts = attempt.Strategy{
	Total: 60 * time.Second,
	Delay: 500 * time.Millisecond,
}
