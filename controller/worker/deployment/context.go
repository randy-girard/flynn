package deployment

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/flynn/que-go"
	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/attempt"
	"github.com/randy-girard/flynn/pkg/postgres"
)

type context struct {
	db     *postgres.DB
	client controller.Client
	logger log15.Logger
}

func JobHandler(db *postgres.DB, client controller.Client, logger log15.Logger) func(*que.Job) error {
	return (&context{db, client, logger}).HandleDeployment
}

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
	if err != nil {
		log.Error("error getting deployment record", "deployment_id", args.ID, "err", err)
		if args.ID != "" {
			_ = c.setDeploymentDone(args.ID)
		}
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

	// Buffer job events so a hung event_insert (postgres primary restart
	// during a sirenia deploy) cannot stall Perform on an unbuffered send.
	events := make(chan ct.DeploymentEvent, 32)
	defer close(events)
	go func() {
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

	var f *ct.Formation
	defer func() {
		if deploymentOutcomeFor(e) == outcomeLeaveRunning {
			return
		}
		log.Info("marking the deployment as done")
		if err := c.setDeploymentDone(deployment.ID); err != nil {
			log.Error("error marking the deployment as done", "err", err)
			// isolate_deploys is WHERE finished_at IS NULL. Emitting complete
			// here leaves the unique index held (HA 2026-09-27: each
			// controller process opened a 20-conn pool against a 20-conn
			// role limit).
			if e == nil {
				e = err
			}
			return
		}
		if err := data.StartNextQueuedDeployment(c.db, deployment.AppID); err != nil {
			log.Error("error starting the next queued deployment", "err", err)
		}
		c.clearAppBuilding(deployment.AppID)

		if e == nil {
			// signal success
			events <- ct.DeploymentEvent{
				ReleaseID: deployment.NewReleaseID,
				Status:    "complete",
			}
		} else {
			// rollback failed deploy
			errMsg := e.Error()
			if IsSkipRollback(e) {
				// ErrSkipRollback indicates the deploy failed in some way
				// but no further action should be taken, so set the error
				// to nil to avoid retrying the deploy
				e = nil
			} else if f != nil {
				log.Warn("rolling back deployment due to error", "err", e)
				e = c.rollback(log, deployment, f, job.Stop)
				if e != nil {
					// The failed event is what flynn-host update and
					// `flynn deployment` show: say what the rollback left.
					errMsg += "; rollback: " + e.Error()
				}
			} else {
				e = nil
			}
			events <- ct.DeploymentEvent{
				ReleaseID: deployment.NewReleaseID,
				Status:    "failed",
				Error:     errMsg,
			}
		}
	}()

	// for recovery purposes, fetch old formation
	log.Info("getting old formation")
	f, err = c.client.GetFormation(deployment.AppID, deployment.OldReleaseID)
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
	if err := c.client.SetAppRelease(deployment.AppID, deployment.NewReleaseID); err != nil {
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
	if err := c.client.ScaleAppRelease(original.AppID, original.ReleaseID, opts); err != nil {
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

func (c *context) setDeploymentDone(id string) error {
	return doneAttempts.Run(func() error {
		return c.db.Exec("deployment_update_finished_at_now", id)
	})
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
	return c.execWithRetries("event_insert_op", e.AppID, e.DeploymentID, string(ct.EventTypeDeployment), e, ct.EventOpUpdate)
}

var execAttempts = attempt.Strategy{
	Total: 10 * time.Second,
	Delay: 100 * time.Millisecond,
}

// doneAttempts is longer than execAttempts: marking finished_at must survive
// a brief platform-postgres connection-limit storm or isolate_deploys sticks.
var doneAttempts = attempt.Strategy{
	Total: 60 * time.Second,
	Delay: 500 * time.Millisecond,
}

// Retry db queries in case postgres has been deployed
func (c *context) execWithRetries(query string, args ...interface{}) error {
	return execAttempts.Run(func() error {
		return c.db.Exec(query, args...)
	})
}
