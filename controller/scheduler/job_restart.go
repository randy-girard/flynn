package main

import (
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

// restartAgedJobs restarts running jobs that have been up longer than the
// cluster job restart interval, to release memory the process has accumulated.
//
// Safety rules:
//   - at most one job per app is replaced at a time, and the next one only
//     starts once every formation of the app is back at its full process
//     count with no job pending, starting or stopping (so datastore quorums
//     and web capacity are never reduced by more than one);
//   - process types with a single instance are skipped, since the scheduler
//     stops the old job before starting its replacement and that would be
//     downtime;
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
	if now.Sub(job.StartedAt) < s.jobRestartInterval {
		return false
	}
	return job.Formation.Processes[job.Type] >= 2
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
