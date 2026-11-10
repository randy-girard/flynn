package main

import (
	"time"

	"github.com/randy-girard/flynn/controller/buildadmit"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	"github.com/randy-girard/flynn/host/resource"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

// buildClient is the controller API the scheduler uses for compiles.
// The concrete client implements it. Test fakes do not, and the tick no-ops.
type buildClient interface {
	// ListPendingBuilds returns pending, blocked, starting, and up compiles.
	ListPendingBuilds() ([]*ct.Job, error)
	GetBuildSpec(appID, jobID string) (*ct.BuildSpec, error)
	MintBuildToken(appID, jobID string) (string, error)
	GetRuntimeSettings() (*ct.RuntimeSettings, error)
}

func (s *Scheduler) tickBuilds() {
	if s.buildTick != nil {
		return
	}
	ch := make(chan time.Time, 1)
	s.buildTick = ch
	ticker := time.NewTicker(time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case t := <-ticker.C:
				select {
				case ch <- t:
				default:
				}
			}
		}
	}()
}

func (s *Scheduler) beginAdmitBuilds() {
	if s.buildsInFlight {
		return
	}
	api, ok := s.ControllerClient.(buildClient)
	if !ok {
		return
	}
	s.buildsInFlight = true
	go func() {
		jobs, listErr := api.ListPendingBuilds()
		settings, setErr := api.GetRuntimeSettings()
		s.deliver(func() {
			s.buildsInFlight = false
			if !s.IsLeader() {
				return
			}
			if setErr == nil && settings != nil {
				clusterLimit, accountLimit := settings.BuildLimits()
				s.buildLimits = buildadmit.Limits{Cluster: clusterLimit, Account: accountLimit}
			}
			if listErr != nil {
				s.logger.Error("listing pending builds", "err", listErr)
				return
			}
			for _, job := range jobs {
				s.adoptControllerBuild(job)
			}
			s.maybeStartSelected()
		})
	}()
}

func (s *Scheduler) adoptControllerBuild(cj *ct.Job) bool {
	if cj == nil || !ct.IsBuilderProcessType(cj.Type) {
		return false
	}
	switch cj.State {
	case ct.JobStatePending, ct.JobStateBlocked, ct.JobStateStarting, ct.JobStateUp:
	default:
		return false
	}
	s.noteForeignBuild(cj)
	if _, ok := s.jobs[cj.UUID]; ok {
		return true
	}
	state := JobStatePending
	switch cj.State {
	case ct.JobStateBlocked:
		state = JobStateBlocked
	case ct.JobStateStarting:
		state = JobStateStarting
	case ct.JobStateUp:
		state = JobStateRunning
	}
	owner := ""
	if cj.Meta != nil {
		owner = cj.Meta[ct.MetaBuildOwner]
	}
	job := &Job{
		ID:              cj.UUID,
		Type:            cj.Type,
		AppID:           cj.AppID,
		ReleaseID:       cj.ReleaseID,
		HostID:          cj.HostID,
		JobID:           cj.ID,
		Args:            cj.Args,
		State:           state,
		Owner:           buildadmit.NormalizeOwner(owner),
		createdAt:       jobCreatedAt(cj),
		explicitVolumes: []ct.VolumeReq{{Path: "/tmp", DeleteOnStop: true}},
		metadata: map[string]string{
			ct.MetaBuildOwner: buildadmit.NormalizeOwner(owner),
		},
	}
	s.jobs.Add(job)
	return true
}

func (s *Scheduler) noteForeignBuild(cj *ct.Job) {
	if cj == nil || cj.ID == "" || cj.HostID == "" || buildadmit.JobOnHost(cj.ID, cj.HostID) {
		return
	}
	jobID, hostID := cj.ID, cj.HostID
	go s.stopBuildOnHost(jobID, hostID)
}

func (s *Scheduler) stopBuildOnHost(jobID, hostID string) {
	if s.ClusterClient == nil || jobID == "" {
		return
	}
	seen := map[string]struct{}{}
	for _, id := range []string{hostID} {
		if id == "" {
			continue
		}
		seen[id] = struct{}{}
		if h, err := s.Host(id); err == nil && h != nil {
			h.StopJob(jobID)
		}
	}
	if prefix, err := cluster.ExtractHostID(jobID); err == nil {
		if _, ok := seen[prefix]; !ok {
			if h, err := s.Host(prefix); err == nil && h != nil {
				h.StopJob(jobID)
			}
		}
	}
}

func (s *Scheduler) maybeStartSelected() {
	if !s.IsLeader() {
		return
	}
	limits := s.buildLimits
	if limits.Cluster == 0 {
		limits = buildadmit.StoredLimits(0, 0)
	}
	var snaps []buildadmit.Job
	for _, job := range s.jobs {
		if job == nil {
			continue
		}
		snaps = append(snaps, buildadmit.Job{
			ID:        job.ID,
			Type:      job.Type,
			Owner:     job.Owner,
			State:     schedulerState(job.State),
			CreatedAt: job.createdAt,
		})
	}
	for _, start := range buildadmit.SelectPending(snaps, limits) {
		job := s.jobs[start.ID]
		if job == nil || job.State != JobStatePending {
			continue
		}
		if s.buildStarting == nil {
			s.buildStarting = map[string]struct{}{}
		}
		if _, ok := s.buildStarting[job.ID]; ok {
			continue
		}
		s.buildStarting[job.ID] = struct{}{}
		go s.StartBuild(job)
	}
}

func schedulerState(state JobState) string {
	switch state {
	case JobStatePending:
		return buildadmit.StatePending
	case JobStateBlocked:
		return buildadmit.StateBlocked
	case JobStateStarting:
		return buildadmit.StateStarting
	case JobStateRunning:
		return buildadmit.StateUp
	case JobStateStopping:
		return buildadmit.StateStopping
	default:
		return buildadmit.StateDown
	}
}

func (s *Scheduler) StartBuild(job *Job) {
	defer func() {
		s.deliver(func() {
			if s.buildStarting != nil {
				delete(s.buildStarting, job.ID)
			}
		})
	}()
	log := s.logger.New("fn", "StartBuild", "job.id", job.ID, "job.type", job.Type, "app.id", job.AppID)
	api, ok := s.ControllerClient.(buildClient)
	if !ok {
		log.Error("controller client cannot start builds")
		return
	}
	spec, err := api.GetBuildSpec(job.AppID, job.ID)
	if err != nil {
		log.Error("error loading build spec", "err", err)
		return
	}
	if spec == nil || spec.Artifact == nil {
		log.Error("build spec has no artifact")
		return
	}

	var h *Host
	if job.HostID == "" || job.JobID == "" || !buildadmit.JobOnHost(job.JobID, job.HostID) {
		req, err := s.PlaceJob(job)
		if err != nil {
			log.Info("build not placed", "err", err)
			s.deliver(func() {
				if job.State != JobStatePending {
					job.State = JobStatePending
					job.HostID = ""
					job.JobID = ""
					s.persistJob(job)
				}
			})
			return
		}
		h = req.Host
	} else {
		found := make(chan *Host, 1)
		s.deliver(func() { found <- s.hosts[job.HostID] })
		h = <-found
	}
	if h == nil || h.client == nil {
		log.Error("placed build has no host client", "host.id", job.HostID)
		s.deliver(func() { s.abandonBuildPlacement(job) })
		return
	}

	admitted := make(chan bool, 1)
	s.deliver(func() {
		if !s.IsLeader() || job.State != JobStatePending {
			admitted <- false
			return
		}
		limits := s.buildLimits
		if limits.Cluster == 0 {
			limits = buildadmit.StoredLimits(0, 0)
		}
		var snaps []buildadmit.Job
		for _, existing := range s.jobs {
			if existing == nil {
				continue
			}
			snaps = append(snaps, buildadmit.Job{
				ID:        existing.ID,
				Type:      existing.Type,
				Owner:     existing.Owner,
				State:     schedulerState(existing.State),
				CreatedAt: existing.createdAt,
			})
		}
		ok := false
		for _, start := range buildadmit.SelectPending(snaps, limits) {
			if start.ID == job.ID {
				ok = true
				break
			}
		}
		if !ok {
			s.abandonBuildPlacement(job)
		}
		admitted <- ok
	})
	if !<-admitted {
		log.Info("build left pending; cap would be exceeded")
		return
	}

	for _, vol := range job.Volumes {
		if vol == nil || vol.GetState() != ct.VolumeStatePending {
			continue
		}
		log.Info("creating build volume", "host.id", h.ID, "vol.id", vol.ID)
		if err := h.client.CreateVolume("default", vol.Info()); err != nil {
			log.Error("error creating build volume", "err", err)
			return
		}
		vol.SetState(ct.VolumeStateCreated)
		s.persistVolume(vol)
	}

	token, err := api.MintBuildToken(job.AppID, job.ID)
	if err != nil {
		log.Error("error minting build token", "err", err)
		s.deliver(func() {
			job.State = JobStateStopped
			s.persistJob(job)
		})
		return
	}
	config := buildHostJob(job, spec, token)
	s.persistJob(job)
	log.Info("adding build to host", "host.id", h.ID, "cluster.id", job.JobID)
	if err := h.client.AddJob(config); err != nil && !addJobConflict(err) {
		log.Error("error adding build to host", "err", err)
	}
}

func buildHostJob(job *Job, spec *ct.BuildSpec, token string) *host.Job {
	appName := ""
	var env map[string]string
	var args []string
	var resources resource.Resources
	if spec != nil {
		appName = spec.AppName
		env = spec.Env
		args = spec.Args
		resources = spec.Resources
	}
	if len(args) == 0 {
		args = []string{"/builder/build.sh"}
	}
	hjob := buildadmit.HostJob(buildadmit.HostJobInput{
		HostID:    job.HostID,
		UUID:      job.ID,
		AppID:     job.AppID,
		AppName:   appName,
		ReleaseID: job.ReleaseID,
		Type:      job.Type,
		Owner:     job.Owner,
		Args:      args,
		Env:       env,
		Token:     token,
		Resources: resources,
	})
	if spec != nil && spec.Artifact != nil {
		utils.SetupMountspecs(hjob, []*ct.Artifact{spec.Artifact})
	}
	if len(job.Volumes) > 0 {
		hjob.Config.Volumes = make([]host.VolumeBinding, 0, len(job.Volumes))
		for _, vol := range job.Volumes {
			if vol == nil {
				continue
			}
			hjob.Config.Volumes = append(hjob.Config.Volumes, host.VolumeBinding{
				Target:       vol.Path,
				VolumeID:     vol.ID,
				Writeable:    true,
				DeleteOnStop: vol.DeleteOnStop,
			})
		}
	}
	return hjob
}

// abandonBuildPlacement drops a host assignment that must not start.
// Pending ephemeral volumes exist only in memory until CreateVolume, so
// they are removed here instead of written to the controller.
func (s *Scheduler) abandonBuildPlacement(job *Job) {
	if job == nil {
		return
	}
	job.HostID = ""
	job.JobID = ""
	job.State = JobStatePending
	kept := make([]*Volume, 0, len(job.Volumes))
	for _, vol := range job.Volumes {
		if vol == nil {
			continue
		}
		if vol.DeleteOnStop && vol.GetState() == ct.VolumeStatePending {
			delete(s.volumes, vol.ID)
			if ids := s.hostVolumeIDs[vol.HostID]; ids != nil {
				delete(ids, vol.ID)
			}
			continue
		}
		kept = append(kept, vol)
	}
	job.Volumes = kept
	s.persistJob(job)
}

func (s *Scheduler) dropForeignBuildVolumes(job *Job) {
	if job == nil || s == nil {
		return
	}
	refs := make([]buildadmit.VolumeRef, 0, len(job.Volumes))
	for _, vol := range job.Volumes {
		if vol == nil {
			continue
		}
		refs = append(refs, buildadmit.VolumeRef{
			ID:           vol.ID,
			HostID:       vol.HostID,
			Path:         vol.Path,
			DeleteOnStop: vol.DeleteOnStop,
		})
	}
	kept := buildadmit.KeepVolumes(refs, "")
	keep := map[string]struct{}{}
	for _, ref := range kept {
		keep[ref.ID] = struct{}{}
	}
	next := make([]*Volume, 0, len(kept))
	for _, vol := range job.Volumes {
		if vol == nil {
			continue
		}
		if _, ok := keep[vol.ID]; ok {
			next = append(next, vol)
			continue
		}
		vol.SetState(ct.VolumeStateDestroyed)
		vol.JobID = nil
		if s.controllerPersist != nil {
			s.persistVolume(vol)
		}
	}
	job.Volumes = next
}

func (s *Scheduler) stampBuildMeta(job *Job) {
	if job == nil || !ct.IsBuilderProcessType(job.Type) {
		return
	}
	if job.metadata == nil {
		job.metadata = map[string]string{}
	}
	job.metadata[ct.MetaBuildOwner] = buildadmit.NormalizeOwner(job.Owner)
}

func jobCreatedAt(job *ct.Job) time.Time {
	if job == nil || job.CreatedAt == nil {
		return time.Time{}
	}
	return *job.CreatedAt
}
