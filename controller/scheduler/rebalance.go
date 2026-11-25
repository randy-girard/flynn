package main

import (
	"sort"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	host "github.com/randy-girard/flynn/host/types"
)

// Rebalancing spreads replicas that ended up on one host. Placement already
// prefers the host with the fewest replicas of an app and type, but while a
// host is down its replacements can only go to the hosts that are left, and
// nothing moved them back when it returned. In EDGE-47's chaos run C both
// controller web jobs stayed on dev-node1 that way, and powering dev-node1
// off then took the controller API down for 92 s (EDGE-112).
//
// The rules:
//   - only a non-omni process type with two or more replicas that registers
//     a discoverd service (so "running" means the service is up), with no
//     persistent volume and no sirenia release (databases keep their data
//     host). Workers without a service are left where they are: moving one
//     can interrupt the work it is doing, and stacking them only slows work;
//   - only when every replica of that type is running, the formation is at
//     its count, the app runs a single release (no deploy in progress) and
//     there is no pending scale request;
//   - only when one host runs two or more of the replicas and a healthy,
//     eligible host (pickHost: tags, role, process cap, capacity) runs none;
//   - one move at a time across the cluster, rebalanceCooldown apart;
//   - the new job starts first. The stacked job is stopped (deregistered and
//     drained, as for a scale down) only once the new job is running, which
//     for a routed service means it is up in the routers. A new job that
//     stops, or is not running within rebalanceTimeout, cancels the move and
//     the stacked jobs keep running.
//
// The formation count does not include the job being moved out while the
// move is in flight, so a rectify neither stops the new job nor refills the
// formation when the old one stops.

const (
	rebalanceCooldown = 30 * time.Second
	rebalanceTimeout  = 3 * time.Minute
	rebalanceInterval = 30 * time.Second

	stopReasonRebalance = "rebalance"
)

type rebalanceMove struct {
	key       utils.FormationKey
	typ       string
	count     int
	oldJob    *Job
	newJob    *Job
	startedAt time.Time
}

func (s *Scheduler) triggerRebalance() {
	select {
	case s.rebalanceCh <- struct{}{}:
	default:
	}
}

func (s *Scheduler) tickRebalance(d time.Duration) {
	go func() {
		for range time.Tick(d) {
			s.triggerRebalance()
		}
	}()
}

// movingOut reports how many jobs of f/typ are counted out of the formation
// because a move is replacing them.
func (s *Scheduler) movingOut(key utils.FormationKey, typ string) int {
	m := s.rebalance
	if m == nil || m.key != key || m.typ != typ {
		return 0
	}
	if !m.oldJob.IsInFormation(key) || !m.newJob.IsInFormation(key) {
		return 0
	}
	return 1
}

// maybeRebalance runs on the loop. It starts at most one move.
func (s *Scheduler) maybeRebalance() {
	if s == nil || !s.IsLeader() {
		return
	}
	now := time.Now()
	if m := s.rebalance; m != nil {
		if m.newJob.State != JobStateRunning && now.Sub(m.startedAt) > rebalanceTimeout {
			s.logger.Warn("rebalance: new job not running in time, keeping the stacked job", "fn", "maybeRebalance",
				"app.id", m.key.AppID, "job.type", m.typ, "old_job.id", m.oldJob.ID, "new_job.id", m.newJob.ID)
			newJob := m.newJob
			s.endRebalance()
			s.stopJobReason(newJob, stopReasonRebalance)
		}
		return
	}
	if now.Before(s.rebalanceNext) {
		return
	}
	keys := make([]utils.FormationKey, 0, len(s.formations))
	for key := range s.formations {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].AppID != keys[j].AppID {
			return keys[i].AppID < keys[j].AppID
		}
		return keys[i].ReleaseID < keys[j].ReleaseID
	})
	for _, key := range keys {
		f := s.formations[key]
		types := make([]string, 0, len(f.Processes))
		for typ := range f.Processes {
			types = append(types, typ)
		}
		sort.Strings(types)
		for _, typ := range types {
			if s.startRebalance(f, typ, now) {
				return
			}
		}
	}
}

func rebalanceableType(f *Formation, typ string) bool {
	if f == nil || f.ExpandedFormation == nil || f.Release == nil || f.App == nil {
		return false
	}
	if f.Processes[typ] < 2 || f.OriginalProcesses[typ] < 2 || ct.IsBuilderProcessType(typ) {
		return false
	}
	if f.Release.IsSirenia() {
		return false
	}
	proc, ok := f.Release.Processes[typ]
	if !ok || proc.Omni || proc.Service == "" {
		return false
	}
	for _, v := range proc.Volumes {
		if !v.DeleteOnStop {
			return false
		}
	}
	return true
}

func (s *Scheduler) startRebalance(f *Formation, typ string, now time.Time) bool {
	if !rebalanceableType(f, typ) {
		return false
	}
	if req := f.PendingScaleRequest; req != nil && req.State == ct.ScaleRequestStatePending {
		return false
	}
	if s.activeFormationCount(f.App.ID) > 1 {
		return false
	}
	key := f.key()
	perHost := map[string][]*Job{}
	running := 0
	for _, job := range s.jobs {
		if job == nil || job.Type != typ || job.Formation == nil || job.Formation.key() != key {
			continue
		}
		switch job.State {
		case JobStateStopped:
			continue
		case JobStateRunning:
			running++
			perHost[job.HostID] = append(perHost[job.HostID], job)
		default:
			return false
		}
	}
	if running != f.Processes[typ] {
		return false
	}
	var stacked []*Job
	for _, jobs := range perHost {
		if len(jobs) >= 2 && len(jobs) > len(stacked) {
			stacked = jobs
		}
	}
	if stacked == nil {
		return false
	}
	newJob := &Job{
		ID:        s.generateJobUUID(),
		Type:      typ,
		AppID:     f.App.ID,
		ReleaseID: f.Release.ID,
		Formation: f,
		StartedAt: now,
		State:     JobStatePending,
		Args:      f.Release.Processes[typ].Args,
		Reason:    host.JobReasonReplace,
	}
	target := s.pickHost(newJob, s.jobs.GetHostSpreadCounts(key, typ))
	if target == nil || len(perHost[target.ID]) > 0 {
		return false
	}
	// The newest stacked job goes; sortJobs puts it first.
	sorted := sortJobs(append([]*Job(nil), stacked...))
	sorted.Sort()
	oldJob := sorted[0]

	s.logger.Info("rebalance: starting a replica on a host without one before stopping a stacked replica", "fn", "maybeRebalance",
		"app.id", f.App.ID, "release.id", f.Release.ID, "job.type", typ, "stacked.host", oldJob.HostID, "stacked", len(stacked),
		"target.host", target.ID, "old_job.id", oldJob.ID, "new_job.id", newJob.ID)
	newJob.rebalanceOf = oldJob.ID
	s.rebalance = &rebalanceMove{key: key, typ: typ, count: f.Processes[typ], oldJob: oldJob, newJob: newJob, startedAt: now}
	s.jobs.Add(newJob)
	s.persistJob(newJob)
	go s.StartJob(newJob)
	return true
}

// noteRebalanceJobState runs from handleJobStatus after job's state changed,
// before the formation diff is computed.
func (s *Scheduler) noteRebalanceJobState(job *Job, previous JobState) {
	m := s.rebalance
	if m == nil || job == nil || job.State == previous {
		return
	}
	log := s.logger.New("fn", "noteRebalanceJobState", "app.id", m.key.AppID, "job.type", m.typ, "old_job.id", m.oldJob.ID, "new_job.id", m.newJob.ID)
	switch job {
	case m.newJob:
		switch job.State {
		case JobStateRunning:
			if m.oldJob.State != JobStateRunning {
				// The stacked job is already going on its own.
				log.Info("rebalance: new job running, stacked job already stopping")
				return
			}
			log.Info("rebalance: new job running, stopping the stacked job", "old.host", m.oldJob.HostID, "new.host", job.HostID)
			if err := s.stopJobReason(m.oldJob, stopReasonRebalance); err != nil {
				log.Error("rebalance: error stopping the stacked job", "err", err)
				s.endRebalance()
			}
		case JobStateStopped, JobStateStopping:
			if m.oldJob.State == JobStateRunning {
				log.Warn("rebalance: new job stopped before it was running, keeping the stacked job")
				s.endRebalance()
			}
		}
	case m.oldJob:
		if job.State == JobStateStopped {
			log.Info("rebalance: done")
			s.endRebalance()
		}
	}
}

// endRebalance forgets the move and allows the next one after the cooldown.
func (s *Scheduler) endRebalance() {
	if s.rebalance == nil {
		return
	}
	s.rebalance.newJob.rebalanceOf = ""
	s.rebalance = nil
	s.rebalanceNext = time.Now().Add(rebalanceCooldown)
	time.AfterFunc(rebalanceCooldown, s.triggerRebalance)
}

// cancelRebalanceOnScale drops a move whose formation count changed: the
// normal rectify then handles every job of the type, the stacked one included.
func (s *Scheduler) cancelRebalanceOnScale(f *Formation) {
	m := s.rebalance
	if m == nil || f == nil || m.key != f.key() || f.Processes[m.typ] == m.count {
		return
	}
	s.logger.Info("rebalance: formation scaled, cancelling the move", "fn", "cancelRebalanceOnScale", "app.id", m.key.AppID, "job.type", m.typ)
	s.endRebalance()
}
