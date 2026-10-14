package main

import (
	"hash/fnv"
	"sort"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

// maxAgeRestartsPerSync caps how many jobs one SyncJobs pass restarts across
// the whole cluster, so an upgrade (when every job is already older than the
// interval) rolls through apps gradually instead of all at once.
const maxAgeRestartsPerSync = 5

// jobRestartEventPoster is implemented by the controller client when it can
// record job_restart events.
type jobRestartEventPoster interface {
	CreateJobRestartEvent(appID string, ev *ct.JobRestartEvent) error
}

// setJobRestartInterval sets the age at which running jobs are restarted.
// Zero disables periodic restarts.
func (s *Scheduler) setJobRestartInterval(d time.Duration) {
	if d < 0 {
		d = 0
	}
	if d == s.jobRestartInterval {
		return
	}
	s.logger.Info("cluster job restart interval changed", "fn", "setJobRestartInterval", "from", s.jobRestartInterval, "to", d)
	s.jobRestartInterval = d
}

// setSingleInstanceRestart sets whether periodic restarts may replace the only
// instance of a user app process type.
func (s *Scheduler) setSingleInstanceRestart(on bool) {
	if on == s.singleInstanceRestart {
		return
	}
	s.logger.Info("cluster single instance restart changed", "fn", "setSingleInstanceRestart", "from", s.singleInstanceRestart, "to", on)
	s.singleInstanceRestart = on
}

// restartAgedJobs restarts running jobs that have been up longer than the
// cluster job restart interval, to release memory the process has accumulated.
//
// Safety rules:
//   - at most one job per app is replaced at a time, and the next one only
//     starts once every formation of the app is back at its full process
//     count with no job pending, starting or stopping (so datastore quorums
//     and web capacity are never reduced by more than one);
//   - only user apps are restarted; system apps, plugin apps and datastores
//     (postgres, mysql, redis, mongodb, kafka, clickhouse) are never restarted
//     by this rule;
//   - process types with a single instance are skipped unless the cluster
//     single_instance_restart option is on, since the scheduler stops the old
//     job before starting its replacement and that would be downtime;
//   - a job is due between 80% and 100% of the interval, at a point fixed per
//     job (see jobRestartThreshold);
//   - the oldest job of an app goes first;
//   - at most maxAgeRestartsPerSync jobs are restarted per pass.
func (s *Scheduler) restartAgedJobs(now time.Time) {
	if s.jobRestartInterval <= 0 || !s.IsLeader() {
		return
	}
	oldest := make(map[string]*Job)
	for _, job := range s.jobs {
		if !s.eligibleForAgeRestart(job, now) {
			continue
		}
		if cur, ok := oldest[job.AppID]; !ok || job.StartedAt.Before(cur.StartedAt) {
			oldest[job.AppID] = job
		}
	}
	candidates := make([]*Job, 0, len(oldest))
	for _, job := range oldest {
		candidates = append(candidates, job)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].StartedAt.Equal(candidates[j].StartedAt) {
			return candidates[i].StartedAt.Before(candidates[j].StartedAt)
		}
		return candidates[i].AppID < candidates[j].AppID
	})
	restarted := 0
	for _, job := range candidates {
		if restarted >= maxAgeRestartsPerSync {
			return
		}
		if !s.appSettled(job.AppID) {
			continue
		}
		if s.restartForAge(job, now) {
			restarted++
		}
	}
}

// eligibleForAgeRestart reports whether a job is old enough and part of a
// process type that has another instance to keep serving.
func (s *Scheduler) eligibleForAgeRestart(job *Job, now time.Time) bool {
	if job == nil || job.State != JobStateRunning || job.JobID == "" || job.StartedAt.IsZero() {
		return false
	}
	if job.Formation == nil || job.Formation.ExpandedFormation == nil || job.Formation.App == nil {
		return false
	}
	if !isUserApp(job.Formation.App) {
		return false
	}
	if now.Sub(job.StartedAt) < jobRestartThreshold(s.jobRestartInterval, job.JobID) {
		return false
	}
	if s.singleInstanceRestart {
		return true
	}
	return job.Formation.Processes[job.Type] >= 2
}

// isUserApp reports whether the app is a user app (web, worker, other process
// types). System apps, plugin apps and datastores (postgres, mysql, redis,
// mongodb, kafka, clickhouse) are never restarted by the age rule.
func isUserApp(app *ct.App) bool {
	return app != nil && !app.System() && !app.Plugin() && !app.Datastore()
}

// jobRestartJitterPercent is how much earlier than the interval a job may be
// restarted. Each job gets its own fixed point in that window so instances
// started together (a scaled app, a deploy) do not all come due at once.
const jobRestartJitterPercent = 20

// jobRestartThreshold is the age at which a job becomes due for restart: the
// cluster interval minus a per-job offset of up to jobRestartJitterPercent.
// The offset is derived from the job ID, so it is stable across scheduler
// passes and leader changes, and no job ever runs longer than the interval.
func jobRestartThreshold(interval time.Duration, jobID string) time.Duration {
	window := interval / 100 * jobRestartJitterPercent
	if window <= 0 {
		return interval
	}
	h := fnv.New32a()
	h.Write([]byte(jobID))
	return interval - time.Duration(int64(h.Sum32())%int64(window))
}

// appSettled reports whether the app is at its full process count with no
// job in transition, so another job can safely be taken down.
func (s *Scheduler) appSettled(appID string) bool {
	for _, job := range s.jobs {
		if job == nil || job.AppID != appID {
			continue
		}
		switch job.State {
		case JobStatePending, JobStateStarting, JobStateStopping, JobStateBlocked:
			return false
		}
	}
	for _, f := range s.formations {
		if f.ExpandedFormation == nil || f.App == nil || f.App.ID != appID {
			continue
		}
		if !f.Diff(s.jobs.GetProcesses(f.key())).IsEmpty() {
			return false
		}
	}
	return true
}

// restartForAge stops the job so the normal restart path replaces it. The job
// is marked as a planned restart so it is not counted as a crash.
func (s *Scheduler) restartForAge(job *Job, now time.Time) bool {
	age := now.Sub(job.StartedAt)
	log := s.logger.New("fn", "restartForAge", "app.id", job.AppID, "job.type", job.Type, "job.id", job.ID, "age", age)
	job.plannedRestart = true
	if err := s.stopJobReason(job, host.JobReasonRestart); err != nil {
		job.plannedRestart = false
		log.Error("error restarting aged job", "err", err)
		return false
	}
	log.Info("restarting job that exceeded the job restart interval", "interval", s.jobRestartInterval)
	s.emitJobRestartEvent(job, age, now)
	return true
}

func (s *Scheduler) emitJobRestartEvent(job *Job, age time.Duration, now time.Time) {
	poster, ok := s.ControllerClient.(jobRestartEventPoster)
	if !ok {
		return
	}
	ev := &ct.JobRestartEvent{
		AppID:       job.AppID,
		AppName:     job.Formation.App.Name,
		ProcessType: job.Type,
		JobID:       job.JobID,
		JobUUID:     job.ID,
		Age:         age.Round(time.Second).String(),
		AgeSeconds:  int64(age.Seconds()),
		Interval:    s.jobRestartInterval.String(),
		Reason:      ct.JobRestartReasonMaxAge,
		RestartedAt: now.UTC(),
	}
	go func() {
		err := controllerPersistAttempts.RunWithValidator(func() error {
			return poster.CreateJobRestartEvent(ev.AppID, ev)
		}, httphelper.IsRetryableError)
		if err != nil {
			s.logger.Error("error posting job restart event", "fn", "emitJobRestartEvent", "app.id", ev.AppID, "job.id", ev.JobID, "err", err)
		}
	}()
}
