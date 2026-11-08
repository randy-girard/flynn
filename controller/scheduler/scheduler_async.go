package main

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/host/volume"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

// errHostFollowStarted means followHost began the host connection on
// another goroutine. The caller must not treat it as a failed follow.
var errHostFollowStarted = errors.New("host follow started")

// leaderBarrier is one in-loop leader promotion. Rectify waits until the
// host, formation, and job reads that started for it have been applied,
// including reads that failed.
type leaderBarrier struct {
	id         uint64
	hosts      bool
	formations bool
	jobs       bool
}

type placementHold struct {
	req   *PlacementRequest
	hosts map[string]struct{}
}

type hostRef struct {
	id     string
	epoch  uint64
	client utils.HostClient
}

type hostJobSnap struct {
	hostID string
	epoch  uint64
	jobs   map[string]host.ActiveJob
	err    error
}

type jobProbe struct {
	schedID   string
	hostID    string
	hostJobID string
	epoch     uint64
	client    utils.HostClient
	job       *host.ActiveJob
	err       error
}

type hostVolSnap struct {
	hostID string
	epoch  uint64
	ids    map[string]struct{}
	infos  []*volume.Info
	err    error
}

type volumeFetch struct {
	snaps          []hostVolSnap
	controller     []*ct.Volume
	controllerErr  error
	fetchedControl bool
}

type hostCheckResult struct {
	id  string
	err error
}

type formationFetch struct {
	formations  []*ct.ExpandedFormation
	formErr     error
	profiles    []*ct.RuntimeProfile
	profilesOK  bool
	profileErr  error
	tenancy     *ct.TenancySettings
	tenancyOK   bool
	tenancyErr  error
	settings    *ct.RuntimeSettings
	settingsOK  bool
	settingsErr error
}

// deliver runs fn on the scheduler goroutine. It must not be called from
// that goroutine: a full loopApply buffer would deadlock the loop.
func (s *Scheduler) deliver(fn func()) {
	if s == nil || fn == nil || s.loopApply == nil {
		return
	}
	select {
	case s.loopApply <- fn:
	case <-s.stop:
	}
}

func (s *Scheduler) captureLeader(part string) uint64 {
	if s.pendingLeader == nil {
		return 0
	}
	switch part {
	case "hosts":
		if s.pendingLeader.hosts {
			return 0
		}
	case "formations":
		if s.pendingLeader.formations {
			return 0
		}
	case "jobs":
		if s.pendingLeader.jobs {
			return 0
		}
	default:
		return 0
	}
	return s.pendingLeader.id
}

func (s *Scheduler) noteLeader(part string, id uint64) {
	if s.pendingLeader == nil || id == 0 || id != s.pendingLeader.id {
		return
	}
	switch part {
	case "hosts":
		s.pendingLeader.hosts = true
	case "formations":
		s.pendingLeader.formations = true
	case "jobs":
		s.pendingLeader.jobs = true
	}
	if s.pendingLeader.hosts && s.pendingLeader.formations && s.pendingLeader.jobs {
		s.pendingLeader = nil
		s.holdRectify = false
		s.rectifyAll()
	}
}

func (s *Scheduler) bumpJobEpoch(hostID string) {
	if hostID == "" || s.hosts == nil {
		return
	}
	if h := s.hosts[hostID]; h != nil {
		h.jobEpoch++
	}
}

func (s *Scheduler) bumpVolEpoch(hostID string) {
	if hostID == "" || s.hosts == nil {
		return
	}
	if h := s.hosts[hostID]; h != nil {
		h.volEpoch++
	}
}

func (s *Scheduler) snapshotHosts() []hostRef {
	out := make([]hostRef, 0, len(s.hosts))
	for id, h := range s.hosts {
		if h == nil || h.client == nil {
			continue
		}
		out = append(out, hostRef{id: id, epoch: h.jobEpoch, client: h.client})
	}
	return out
}

func (s *Scheduler) snapshotVolumeHosts() []hostRef {
	out := make([]hostRef, 0, len(s.hosts))
	for id, h := range s.hosts {
		if h == nil || h.client == nil {
			continue
		}
		out = append(out, hostRef{id: id, epoch: h.volEpoch, client: h.client})
	}
	return out
}

func (s *Scheduler) beginAsyncSyncJobs() {
	if !s.loopActive {
		s.SyncJobs()
		return
	}
	if s.jobsInFlight {
		s.jobsAgain = true
		return
	}
	s.jobsInFlight = true
	s.jobsAgain = false
	leader := s.captureLeader("jobs")
	hosts := s.snapshotHosts()
	go s.fetchJobLists(hosts, leader)
}

func (s *Scheduler) fetchJobLists(hosts []hostRef, leader uint64) {
	snaps := make([]hostJobSnap, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(i int, h hostRef) {
			defer wg.Done()
			jobs, err := h.client.ListActiveJobs()
			snaps[i] = hostJobSnap{hostID: h.id, epoch: h.epoch, jobs: jobs, err: err}
		}(i, h)
	}
	wg.Wait()
	s.deliver(func() { s.applyJobLists(snaps, leader) })
}

func (s *Scheduler) applyJobLists(snaps []hostJobSnap, leader uint64) {
	log := s.logger.New("fn", "SyncJobs")
	log.Info("syncing jobs")
	s.drainDownPersistResults()

	listed := make(map[string]map[string]struct{}, len(snaps))
	skipped := false
	for _, snap := range snaps {
		h := s.hosts[snap.hostID]
		if h == nil || h.jobEpoch != snap.epoch {
			skipped = true
			continue
		}
		if snap.err != nil {
			log.Error("error getting active host jobs", "host.id", snap.hostID, "err", snap.err)
			skipped = true
			continue
		}
		ids := make(map[string]struct{}, len(snap.jobs))
		for _, job := range snap.jobs {
			job := job
			ids[job.Job.ID] = struct{}{}
			s.handleActiveJob(&job)
		}
		listed[snap.hostID] = ids
	}
	if skipped {
		s.finishJobSync(true, leader)
		return
	}
	s.fetchInactiveProbes(listed, leader)
}

func (s *Scheduler) fetchInactiveProbes(listed map[string]map[string]struct{}, leader uint64) {
	log := s.logger.New("fn", "SyncJobs")
	var probes []jobProbe
	for _, job := range s.jobs {
		if job.State != JobStateStarting && job.State != JobStateRunning && job.State != JobStateStopping {
			continue
		}
		if ids, ok := listed[job.HostID]; ok {
			if _, active := ids[job.JobID]; active {
				continue
			}
		} else if _, known := s.hosts[job.HostID]; !known {
			log.Warn("job no longer active", "job.id", job.JobID, "job.state", job.State)
			log.Error("error getting inactive job, unknown host", "job.id", job.JobID, "host.id", job.HostID)
			s.markAsStopped(job)
			continue
		} else {
			// Host was not in this snapshot. Leave the job alone.
			continue
		}
		h := s.hosts[job.HostID]
		if h == nil || h.client == nil {
			log.Warn("job no longer active", "job.id", job.JobID, "job.state", job.State)
			log.Error("error getting inactive job, unknown host", "job.id", job.JobID, "host.id", job.HostID)
			s.markAsStopped(job)
			continue
		}
		log.Warn("job no longer active", "job.id", job.JobID, "job.state", job.State)
		probes = append(probes, jobProbe{
			schedID:   job.ID,
			hostID:    job.HostID,
			hostJobID: job.JobID,
			epoch:     h.jobEpoch,
			client:    h.client,
		})
	}
	go s.runJobProbes(probes, leader)
}

func (s *Scheduler) runJobProbes(probes []jobProbe, leader uint64) {
	var wg sync.WaitGroup
	for i := range probes {
		if probes[i].client == nil || probes[i].hostJobID == "" {
			probes[i].err = errHostFollowStarted
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			job, err := probes[i].client.GetJob(probes[i].hostJobID)
			probes[i].job = job
			probes[i].err = err
		}(i)
	}
	jobs, listErr := s.JobListActive()
	wg.Wait()
	s.deliver(func() { s.applyJobProbes(probes, jobs, listErr, leader) })
}

func (s *Scheduler) applyJobProbes(probes []jobProbe, jobs []*ct.Job, listErr error, leader uint64) {
	log := s.logger.New("fn", "SyncJobs")
	stale := false
	for _, p := range probes {
		h := s.hosts[p.hostID]
		if h == nil || h.jobEpoch != p.epoch {
			stale = true
			continue
		}
		job := s.jobs[p.schedID]
		if job == nil {
			continue
		}
		if p.err != nil || p.job == nil {
			log.Error("error getting inactive job, marking as stopped", "job.id", job.JobID, "err", p.err)
			s.markAsStopped(job)
			continue
		}
		s.handleActiveJob(p.job)
	}
	if stale {
		s.finishJobSync(true, leader)
		return
	}
	if listErr != nil {
		if listErr == controller.ErrNotFound {
			log.Warn("skipping controller job sync, controller missing active job route")
			s.finishJobSync(false, leader)
			return
		}
		log.Error("error getting controller active jobs", "err", listErr)
		s.finishJobSync(true, leader)
		return
	}
	s.applyControllerJobList(jobs)
	s.finishJobSync(false, leader)
}

func (s *Scheduler) applyControllerJobList(jobs []*ct.Job) {
	activeControllerJobs := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		activeControllerJobs[job.UUID] = struct{}{}

		j, ok := s.jobs[job.UUID]
		if !ok {
			job.State = ct.JobStateDown
			s.persistControllerJob(job)
			continue
		}
		if j.State == JobStateStopping {
			continue
		}
		if job.State == ct.JobStatePending && j.State != JobStatePending ||
			job.State == ct.JobStateStarting && j.State != JobStateStarting ||
			job.State == ct.JobStateUp && j.State != JobStateRunning ||
			job.State == ct.JobStateStopping {
			s.persistJob(j)
		}
	}

	for _, job := range s.jobs {
		switch job.State {
		case JobStatePending, JobStateStarting, JobStateRunning:
			if _, active := activeControllerJobs[job.ID]; !active {
				s.persistJob(job)
			}
		case JobStateStopping:
			s.stopJob(job)
		}
	}
	s.forgetDurableStoppedJobs(activeControllerJobs)
	s.restartAgedJobs(time.Now())
}

func (s *Scheduler) finishJobSync(failed bool, leader uint64) {
	s.noteLeader("jobs", leader)
	again := s.jobsAgain
	s.jobsAgain = false
	s.jobsInFlight = false
	if again {
		s.beginAsyncSyncJobs()
		return
	}
	if failed {
		time.AfterFunc(100*time.Millisecond, s.triggerSyncJobs)
	}
}

func (s *Scheduler) resolveInactiveJobs(active map[string]struct{}) {
	log := s.logger.New("fn", "SyncJobs")
	for _, job := range s.jobs {
		if job.State != JobStateStarting && job.State != JobStateRunning && job.State != JobStateStopping {
			continue
		}
		if _, ok := active[job.JobID]; ok {
			continue
		}
		log.Warn("job no longer active", "job.id", job.JobID, "job.state", job.State)
		h, ok := s.hosts[job.HostID]
		if !ok {
			log.Error("error getting inactive job, unknown host", "job.id", job.JobID, "host.id", job.HostID)
			s.markAsStopped(job)
			continue
		}
		j, err := h.client.GetJob(job.JobID)
		if err != nil {
			log.Error("error getting inactive job, marking as stopped", "job.id", job.JobID, "err", err)
			s.markAsStopped(job)
			continue
		}
		s.handleActiveJob(j)
	}
}

func (s *Scheduler) beginAsyncSyncVolumes() {
	if !s.loopActive {
		s.SyncVolumes()
		return
	}
	if s.volsInFlight {
		s.volsAgain = true
		return
	}
	s.volsInFlight = true
	s.volsAgain = false
	hosts := s.snapshotVolumeHosts()
	go func() {
		snaps := make([]hostVolSnap, len(hosts))
		var wg sync.WaitGroup
		for i, h := range hosts {
			wg.Add(1)
			go func(i int, h hostRef) {
				defer wg.Done()
				vols, err := h.client.ListVolumes()
				ids := map[string]struct{}{}
				if err == nil {
					for _, info := range vols {
						if info == nil || !isAppVolume(info) {
							continue
						}
						ids[info.ID] = struct{}{}
					}
				}
				snaps[i] = hostVolSnap{hostID: h.id, epoch: h.epoch, ids: ids, infos: vols, err: err}
			}(i, h)
		}
		wg.Wait()
		rpcFailed := false
		for _, snap := range snaps {
			if snap.err != nil {
				rpcFailed = true
				break
			}
		}
		fetch := volumeFetch{snaps: snaps}
		if !rpcFailed {
			fetch.controller, fetch.controllerErr = s.VolumeList()
			fetch.fetchedControl = true
		}
		s.deliver(func() { s.applyVolumeFetch(fetch) })
	}()
}

func (s *Scheduler) applyVolumeFetch(fetch volumeFetch) {
	log := s.logger.New("fn", "SyncVolumes")
	log.Info("syncing volumes")
	rpcFailed := false
	stale := false
	for _, snap := range fetch.snaps {
		h := s.hosts[snap.hostID]
		if h == nil || h.volEpoch != snap.epoch {
			stale = true
			continue
		}
		if snap.err != nil {
			log.Error("error getting host volumes", "host.id", snap.hostID, "err", snap.err)
			rpcFailed = true
			continue
		}
		s.importListedVolumes(snap.hostID, snap.infos)
	}
	if rpcFailed {
		// Keep the previous hostVolumeIDs. A partial list must not destroy
		// volumes, and it must not mark every volume missing.
		s.finishVolumeSync(true)
		return
	}
	next := make(map[string]map[string]struct{}, len(fetch.snaps))
	for _, snap := range fetch.snaps {
		h := s.hosts[snap.hostID]
		if h == nil || h.volEpoch != snap.epoch || snap.err != nil {
			continue
		}
		next[snap.hostID] = snap.ids
	}
	s.hostVolumeIDs = next
	s.sweepMissingVolumes(next)
	if fetch.fetchedControl {
		s.applyControllerVolumeList(fetch.controller, fetch.controllerErr)
	}
	s.finishVolumeSync(stale)
}

func (s *Scheduler) sweepMissingVolumes(hostVolumeIDs map[string]map[string]struct{}) {
	log := s.logger.New("fn", "SyncVolumes")
	for id, vol := range s.volumes {
		if vol.GetState() == ct.VolumeStateDestroyed || vol.DecommissionedAt != nil {
			s.noteVolumePresentOnHost(id)
			continue
		}
		if vol.HostID == "" {
			continue
		}
		set, ok := hostVolumeIDs[vol.HostID]
		if !ok {
			continue
		}
		if _, exists := set[id]; exists {
			s.noteVolumePresentOnHost(id)
			continue
		}
		if s.volumeHeldByLiveJob(vol) {
			log.Warn("volume missing on host listing but still held by a live job, not destroying",
				"vol.id", id, "host.id", vol.HostID, "job.id", *vol.JobID)
			s.noteVolumePresentOnHost(id)
			continue
		}
		if !s.noteVolumeMissingFromHost(id) {
			log.Warn("volume missing on host, waiting for a second miss before destroying",
				"vol.id", id, "host.id", vol.HostID)
			continue
		}
		log.Warn("volume missing on host, marking destroyed", "vol.id", id, "host.id", vol.HostID)
		vol.SetState(ct.VolumeStateDestroyed)
		vol.JobID = nil
		s.persistVolume(vol)
	}
}

func (s *Scheduler) importListedVolumes(hostID string, infos []*volume.Info) {
	for _, info := range infos {
		if info == nil || !isAppVolume(info) {
			continue
		}
		if _, ok := s.volumes[info.ID]; ok {
			continue
		}
		vol := NewVolume(info, ct.VolumeStateCreated, hostID)
		s.volumes[info.ID] = vol
		s.persistVolume(vol)
	}
}

func (s *Scheduler) importControllerVolumes() {
	volumes, err := s.VolumeList()
	s.applyControllerVolumeList(volumes, err)
}

func (s *Scheduler) applyControllerVolumeList(volumes []*ct.Volume, err error) {
	if err != nil {
		s.logger.Error("error getting controller volumes", "fn", "SyncVolumes", "err", err)
		return
	}
	for _, volume := range volumes {
		s.handleControllerVolume(volume)
	}
}

func (s *Scheduler) finishVolumeSync(failed bool) {
	again := s.volsAgain
	s.volsAgain = false
	s.volsInFlight = false
	if again {
		s.beginAsyncSyncVolumes()
		return
	}
	if failed {
		time.AfterFunc(100*time.Millisecond, s.triggerSyncVolumes)
	}
}

func (s *Scheduler) beginAsyncSyncFormations() {
	if !s.loopActive {
		s.SyncFormations()
		return
	}
	if s.formationsInFlight {
		s.formationsAgain = true
		return
	}
	s.formationsInFlight = true
	s.formationsAgain = false
	leader := s.captureLeader("formations")
	go func() {
		fetch := s.loadFormationFetch()
		s.deliver(func() { s.applyFormationFetch(fetch, leader) })
	}()
}

func (s *Scheduler) loadFormationFetch() formationFetch {
	var fetch formationFetch
	fetch.formations, fetch.formErr = s.FormationListActive()
	if fetch.formErr != nil {
		return fetch
	}
	fetch.profiles, fetch.profilesOK, fetch.profileErr = s.fetchRuntimeProfiles()
	fetch.tenancy, fetch.tenancyOK, fetch.tenancyErr = s.fetchTenancy()
	fetch.settings, fetch.settingsOK, fetch.settingsErr = s.fetchRuntimeSettings()
	return fetch
}

func (s *Scheduler) applyFormationFetch(fetch formationFetch, leader uint64) {
	log := s.logger.New("fn", "SyncFormations")
	log.Info("syncing formations")
	defer log.Debug("formations synced")
	defer s.finishFormationSync(leader)

	if fetch.formErr != nil {
		log.Error("error getting active formations", "err", fetch.formErr)
		return
	}
	active := make(map[utils.FormationKey]struct{}, len(fetch.formations))
	for _, f := range fetch.formations {
		active[utils.FormationKey{AppID: f.App.ID, ReleaseID: f.Release.ID}] = struct{}{}
		s.handleFormation(f)
	}
	s.applyRuntimeProfiles(log, fetch.profiles, fetch.profilesOK, fetch.profileErr)
	s.applyTenancy(log, fetch.tenancy, fetch.tenancyOK, fetch.tenancyErr)
	s.applyRuntimeSettings(log, fetch.settings, fetch.settingsOK, fetch.settingsErr)
	for _, f := range s.formations {
		if _, ok := active[f.key()]; !ok && !f.GetProcesses().IsEmpty() {
			log.Warn("formation should not be active, scaling down", "app.id", f.App.ID, "release.id", f.Release.ID)
			f.Processes = nil
			s.triggerRectify(f.key())
		}
	}
}

func (s *Scheduler) finishFormationSync(leader uint64) {
	s.noteLeader("formations", leader)
	again := s.formationsAgain
	s.formationsAgain = false
	s.formationsInFlight = false
	if again {
		s.beginAsyncSyncFormations()
		return
	}
	if s.holdRectify || len(s.rectifyBatch) == 0 {
		return
	}
	select {
	case s.rectify <- struct{}{}:
	default:
	}
}

func (s *Scheduler) fetchRuntimeProfiles() ([]*ct.RuntimeProfile, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	g, ok := s.ControllerClient.(runtimeProfileLister)
	if !ok {
		return nil, false, nil
	}
	list, err := g.ListRuntimeProfiles()
	return list, true, err
}

func (s *Scheduler) applyRuntimeProfiles(log log15.Logger, list []*ct.RuntimeProfile, ok bool, err error) {
	if !ok {
		return
	}
	if err != nil {
		log.Error("error listing runtime profiles", "err", err)
		return
	}
	next := make(map[string]bool, len(list))
	tags := make(map[string]map[string]string, len(list))
	for _, p := range list {
		if p == nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(p.Name))
		next[name] = p.ReserveResources
		if len(p.Tags) > 0 {
			copied := make(map[string]string, len(p.Tags))
			for k, v := range p.Tags {
				copied[k] = v
			}
			tags[name] = copied
		}
	}
	s.profileReserve = next
	s.profileTags = tags
}

func (s *Scheduler) fetchTenancy() (*ct.TenancySettings, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	g, ok := s.ControllerClient.(tenancyGetter)
	if !ok {
		return nil, false, nil
	}
	settings, err := g.GetTenancy()
	return settings, true, err
}

func (s *Scheduler) applyTenancy(log log15.Logger, settings *ct.TenancySettings, ok bool, err error) {
	if !ok {
		return
	}
	if err != nil || settings == nil {
		log.Error("error getting tenancy", "err", err)
		return
	}
	s.tenancyMode = strings.TrimSpace(settings.Mode)
}

func (s *Scheduler) fetchRuntimeSettings() (*ct.RuntimeSettings, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	g, ok := s.ControllerClient.(runtimeSettingsGetter)
	if !ok {
		return nil, false, nil
	}
	settings, err := g.GetRuntimeSettings()
	return settings, true, err
}

func (s *Scheduler) applyRuntimeSettings(log log15.Logger, settings *ct.RuntimeSettings, ok bool, err error) {
	if !ok {
		return
	}
	if err != nil {
		log.Error("error getting runtime settings", "err", err)
		return
	}
	s.setMaxRestarts(settings.MaxRestarts)
	s.setJobRestartInterval(settings.JobRestartIntervalDuration())
	s.setSingleInstanceRestart(settings.SingleInstanceRestart)
}

func (s *Scheduler) beginAsyncSyncSinks() {
	if !s.loopActive {
		s.SyncSinks()
		return
	}
	if s.sinksInFlight {
		s.sinksAgain = true
		return
	}
	s.sinksInFlight = true
	s.sinksAgain = false
	go func() {
		sinks, err := s.ListSinks()
		s.deliver(func() { s.applySinkList(sinks, err) })
	}()
}

type sinkChange struct {
	id   string
	sink *ct.Sink
	add  bool
}

func (s *Scheduler) applySinkList(sinks []*ct.Sink, err error) {
	log := s.logger.New("fn", "SyncSinks")
	log.Info("syncing log sinks")
	if err != nil {
		log.Error("error getting controller sinks", "err", err)
		s.finishSinkSync()
		return
	}
	active := make(map[string]struct{}, len(sinks))
	var changes []sinkChange
	for _, sink := range sinks {
		active[sink.ID] = struct{}{}
		existing, ok := s.sinks[sink.ID]
		if !ok || !reflect.DeepEqual(existing.Config, sink.Config) {
			changes = append(changes, sinkChange{id: sink.ID, sink: sink, add: true})
		}
		s.sinks[sink.ID] = sink
	}
	for id, sink := range s.sinks {
		if _, ok := active[id]; !ok {
			log.Warn("sink should no longer be active, removing", "sink.id", id)
			changes = append(changes, sinkChange{id: id, sink: sink, add: false})
			delete(s.sinks, id)
		}
	}
	desired := make(map[string]*ct.Sink, len(s.sinks))
	for id, sink := range s.sinks {
		desired[id] = sink
	}
	var hosts []*Host
	for _, h := range s.hosts {
		if h != nil {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		s.finishSinkSync()
		return
	}
	go func() {
		var wg sync.WaitGroup
		for _, h := range hosts {
			wg.Add(1)
			go func(h *Host) {
				defer wg.Done()
				s.reconcileHostSinks(h, changes, desired)
			}(h)
		}
		wg.Wait()
		s.deliver(func() { s.finishSinkSync() })
	}()
}

func (s *Scheduler) reconcileHostSinks(h *Host, changes []sinkChange, desired map[string]*ct.Sink) {
	log := s.logger.New("fn", "SyncSinks", "host.id", h.ID)
	for _, change := range changes {
		if change.add {
			if err := h.AddSink(change.sink); err != nil {
				log.Error("error adding sink", "sink.id", change.id, "err", err)
			}
			continue
		}
		if err := h.RemoveSink(change.id); err != nil {
			log.Error("error removing sink", "err", err)
		}
	}
	got, err := h.GetSinks()
	if err != nil {
		log.Error("error getting host sinks", "err", err)
		return
	}
	for _, sink := range got {
		if sink == nil || sink.HostManaged {
			continue
		}
		expected, ok := desired[sink.ID]
		if !ok {
			log.Warn("removing non existent host sink", "sink.id", sink.ID)
			h.RemoveSink(sink.ID)
		} else if !reflect.DeepEqual(sink.Config, expected.Config) {
			log.Warn("updating stale host sink", "sink.id", sink.ID)
			h.AddSink(expected)
		}
	}
}

func (s *Scheduler) finishSinkSync() {
	again := s.sinksAgain
	s.sinksAgain = false
	s.sinksInFlight = false
	if again {
		s.beginAsyncSyncSinks()
	}
}

func (s *Scheduler) fanoutHost(fn func(h *Host, desired map[string]*ct.Sink)) {
	desired := make(map[string]*ct.Sink, len(s.sinks))
	for id, sink := range s.sinks {
		desired[id] = sink
	}
	hosts := make([]*Host, 0, len(s.hosts))
	for _, h := range s.hosts {
		if h != nil {
			hosts = append(hosts, h)
		}
	}
	if !s.loopActive {
		for _, h := range hosts {
			fn(h, desired)
		}
		return
	}
	for _, h := range hosts {
		h := h
		go fn(h, desired)
	}
}

func (s *Scheduler) beginAsyncSyncHosts() {
	if !s.loopActive {
		s.SyncHosts()
		return
	}
	if s.hostsInFlight {
		s.hostsAgain = true
		return
	}
	s.hostsInFlight = true
	s.hostsAgain = false
	leader := s.captureLeader("hosts")
	go func() {
		hosts, err := s.Hosts()
		s.deliver(func() { s.applyDiscoveredHosts(hosts, err, leader) })
	}()
}

func (s *Scheduler) applyDiscoveredHosts(hosts []utils.HostClient, err error, leader uint64) {
	log := s.logger.New("fn", "SyncHosts")
	log.Info("syncing hosts")
	if err != nil {
		log.Error("error getting hosts", "err", err)
		s.finishHostSync(true, leader)
		return
	}
	if s.hostSyncWait == nil {
		s.hostSyncWait = make(map[string]struct{})
	}
	known := make(map[string]struct{}, len(hosts))
	var followErr bool
	for _, host := range hosts {
		known[host.ID()] = struct{}{}
		if existing, ok := s.hosts[host.ID()]; ok {
			s.maybeStartBlockedJobs(existing)
			continue
		}
		s.hostSyncWait[host.ID()] = struct{}{}
		s.hostSyncPending++
		if _, flying := s.hostFollowInFlight[host.ID()]; flying {
			continue
		}
		if ferr := s.startHostFollow(host); ferr != nil {
			log.Error("error following host", "host.id", host.ID(), "err", ferr)
			followErr = true
			delete(s.hostSyncWait, host.ID())
			s.hostSyncPending--
		}
	}
	for id, host := range s.hosts {
		if _, ok := known[id]; !ok && !host.Shutdown {
			s.markHostAsUnhealthy(host)
		}
	}
	s.hostSyncFailed = followErr || len(hosts) == 0
	s.hostSyncLeader = leader
	if len(hosts) == 0 {
		log.Error(ErrNoHosts.Error())
	}
	if s.hostSyncPending == 0 {
		if !s.hostSyncFailed {
			s.maybePromoteSireniaHA()
		}
		s.finishHostSync(s.hostSyncFailed, leader)
	}
}

func (s *Scheduler) finishHostSync(failed bool, leader uint64) {
	s.hostSyncPending = 0
	s.hostSyncWait = nil
	s.hostSyncLeader = 0
	s.noteLeader("hosts", leader)
	again := s.hostsAgain
	s.hostsAgain = false
	s.hostsInFlight = false
	if again {
		s.beginAsyncSyncHosts()
		return
	}
	if failed {
		time.AfterFunc(100*time.Millisecond, s.triggerSyncHosts)
	}
}

func (s *Scheduler) noteHostFollowDone(id string, err error) {
	if s.hostSyncWait == nil {
		return
	}
	if _, ok := s.hostSyncWait[id]; !ok {
		return
	}
	delete(s.hostSyncWait, id)
	if err != nil {
		s.hostSyncFailed = true
	}
	if s.hostSyncPending > 0 {
		s.hostSyncPending--
	}
	if s.hostSyncPending == 0 && s.hostsInFlight {
		if !s.hostSyncFailed {
			s.maybePromoteSireniaHA()
		}
		s.finishHostSync(s.hostSyncFailed, s.hostSyncLeader)
	}
}

func (s *Scheduler) startHostFollow(h utils.HostClient) error {
	if h == nil {
		return errHostFollowStarted
	}
	if s.hostFollowInFlight == nil {
		s.hostFollowInFlight = make(map[string]struct{})
	}
	if _, ok := s.hostFollowInFlight[h.ID()]; ok {
		return nil
	}
	s.hostFollowInFlight[h.ID()] = struct{}{}
	go func() {
		host, volumes, jobs, err := s.connectHost(h)
		s.deliver(func() {
			delete(s.hostFollowInFlight, h.ID())
			if err != nil || host == nil {
				s.logger.Error("error following host", "fn", "followHost", "host.id", h.ID(), "err", err)
				s.noteHostFollowDone(h.ID(), err)
				return
			}
			if _, ok := s.hosts[host.ID]; ok {
				s.discardExtraHost(host)
				s.noteHostFollowDone(h.ID(), nil)
				return
			}
			s.installHost(host, volumes, jobs)
			host.releaseEvents()
			s.maybeStartBlockedJobs(host)
			s.maybePromoteSireniaHA()
			s.noteHostFollowDone(h.ID(), nil)
		})
	}()
	return nil
}

func (s *Scheduler) connectHost(h utils.HostClient) (*Host, map[string]*Volume, map[string]host.ActiveJob, error) {
	host := NewHost(h, s.logger)
	if stats, err := h.GetStats(); err == nil && stats != nil {
		host.MemoryTotalBytes = stats.MemoryTotalBytes
		if stats.CPUCount > 0 {
			host.CPUMilli = int64(stats.CPUCount) * 1000
		}
	}
	volumes, err := host.StreamVolumeEventsTo(s.volumeEvents)
	if err != nil {
		return nil, nil, nil, err
	}
	jobs, err := host.StreamJobEventsTo(s.jobEvents)
	if err != nil {
		s.abortHostStream(host)
		return nil, nil, nil, err
	}
	return host, volumes, jobs, nil
}

func (s *Scheduler) abortHostStream(host *Host) {
	if host == nil {
		return
	}
	host.releaseEvents()
	host.stopOnce.Do(func() { close(host.stop) })
	<-host.volsDone
}

func (s *Scheduler) discardExtraHost(host *Host) {
	if host == nil {
		return
	}
	host.Close()
}

func (s *Scheduler) installHost(host *Host, volumes map[string]*Volume, jobs map[string]host.ActiveJob) {
	for id, vol := range volumes {
		s.volumes[id] = vol
		s.persistVolume(vol)
	}
	s.hosts[host.ID] = host
	for _, job := range jobs {
		job := job
		s.handleActiveJob(&job)
	}
	s.triggerSyncFormations()
}

func (s *Scheduler) followHostSync(h utils.HostClient) (*Host, error) {
	if host, ok := s.hosts[h.ID()]; ok {
		return host, nil
	}
	host, volumes, jobs, err := s.connectHost(h)
	if err != nil {
		return nil, err
	}
	s.installHost(host, volumes, jobs)
	host.releaseEvents()
	return host, nil
}

func (s *Scheduler) beginAsyncHostChecks() {
	if !s.loopActive {
		s.PerformHostChecks()
		return
	}
	if s.checksInFlight {
		s.checksAgain = true
		return
	}
	s.checksInFlight = true
	s.checksAgain = false
	var targets []hostRef
	for id, h := range s.hosts {
		if h == nil || h.client == nil || h.Healthy {
			continue
		}
		targets = append(targets, hostRef{id: id, client: h.client})
	}
	go func() {
		results := make([]hostCheckResult, len(targets))
		var wg sync.WaitGroup
		for i, t := range targets {
			wg.Add(1)
			go func(i int, t hostRef) {
				defer wg.Done()
				_, err := t.client.GetStatus()
				results[i] = hostCheckResult{id: t.id, err: err}
			}(i, t)
		}
		wg.Wait()
		s.deliver(func() { s.applyHostChecks(results) })
	}()
}

func (s *Scheduler) applyHostChecks(results []hostCheckResult) {
	log := s.logger.New("fn", "PerformHostChecks")
	log.Info("performing host checks")
	allHealthy := true
	seen := map[string]struct{}{}
	for _, res := range results {
		seen[res.id] = struct{}{}
		host := s.hosts[res.id]
		if host == nil || host.Healthy {
			continue
		}
		hlog := log.New("host.id", res.id)
		hlog.Info("getting status of unhealthy host")
		if res.err == nil {
			hlog.Info("host is now healthy")
			host.Healthy = true
			host.Checks = 0
			continue
		}
		host.Checks++
		if host.Checks >= s.maxHostChecks {
			hlog.Warn("host unhealthy for consecutive checks, unfollowing", "checks", s.maxHostChecks)
			s.unfollowHost(host)
			continue
		}
		allHealthy = false
	}
	for id, host := range s.hosts {
		if _, ok := seen[id]; ok || host == nil || host.Healthy {
			continue
		}
		allHealthy = false
	}
	s.finishHostChecks(allHealthy)
}

func (s *Scheduler) finishHostChecks(allHealthy bool) {
	again := s.checksAgain
	s.checksAgain = false
	s.checksInFlight = false
	if again {
		s.beginAsyncHostChecks()
		return
	}
	if !allHealthy {
		time.AfterFunc(time.Second, s.triggerHostChecks)
	}
}

func (s *Scheduler) volumeKnown(hostID, volumeID string) (exists bool, known bool) {
	if _, failed := s.volSnapFailed[hostID]; failed {
		if set, ok := s.hostVolumeIDs[hostID]; ok {
			_, exists = set[volumeID]
			return exists, true
		}
		return false, true
	}
	if snap, ok := s.volSnap[hostID]; ok {
		_, exists = snap[volumeID]
		return exists, true
	}
	if set, ok := s.hostVolumeIDs[hostID]; ok {
		if _, exists = set[volumeID]; exists {
			return true, true
		}
	}
	h, ok := s.hosts[hostID]
	if !ok || h == nil || h.client == nil {
		return false, true
	}
	return false, false
}

func (s *Scheduler) volumeExistsOnHostSync(hostID, volumeID string) bool {
	if set, ok := s.hostVolumeIDs[hostID]; ok {
		if _, exists := set[volumeID]; exists {
			return true
		}
	}
	h, ok := s.hosts[hostID]
	if !ok || h == nil || h.client == nil {
		return false
	}
	volumes, err := h.client.ListVolumes()
	if err != nil {
		if set, ok := s.hostVolumeIDs[hostID]; ok {
			_, exists := set[volumeID]
			return exists
		}
		return false
	}
	for _, info := range volumes {
		if info != nil && info.ID == volumeID {
			if s.hostVolumeIDs[hostID] == nil {
				s.hostVolumeIDs[hostID] = make(map[string]struct{})
			}
			s.hostVolumeIDs[hostID][volumeID] = struct{}{}
			return true
		}
	}
	return false
}

func (s *Scheduler) placementNeedsVolumeList(job *Job) (string, bool) {
	if !s.loopActive || job == nil {
		return "", false
	}
	for _, req := range job.VolumeRequests() {
		vol := s.findVolume(job, &req)
		if vol == nil || vol.HostID == "" {
			continue
		}
		if _, known := s.volumeKnown(vol.HostID, vol.ID); known {
			continue
		}
		return vol.HostID, true
	}
	return "", false
}

func (s *Scheduler) parkPlacement(req *PlacementRequest, hostID string) {
	hold := &placementHold{req: req, hosts: map[string]struct{}{hostID: {}}}
	s.placementHolds = append(s.placementHolds, hold)
	s.ensureVolumeList(hostID)
}

func (s *Scheduler) ensureVolumeList(hostID string) {
	if s.volListInFlight == nil {
		s.volListInFlight = make(map[string]struct{})
	}
	if _, ok := s.volListInFlight[hostID]; ok {
		return
	}
	h := s.hosts[hostID]
	if h == nil || h.client == nil {
		if s.volSnapFailed == nil {
			s.volSnapFailed = make(map[string]struct{})
		}
		s.volSnapFailed[hostID] = struct{}{}
		s.wakeVolumeHolds(hostID)
		return
	}
	s.volListInFlight[hostID] = struct{}{}
	epoch := h.volEpoch
	client := h.client
	go func() {
		vols, err := client.ListVolumes()
		s.deliver(func() { s.finishVolumeList(hostID, epoch, vols, err) })
	}()
}

func (s *Scheduler) finishVolumeList(hostID string, epoch uint64, vols []*volume.Info, err error) {
	delete(s.volListInFlight, hostID)
	h := s.hosts[hostID]
	if h == nil || h.volEpoch != epoch {
		if h == nil {
			if s.volSnapFailed == nil {
				s.volSnapFailed = make(map[string]struct{})
			}
			s.volSnapFailed[hostID] = struct{}{}
			s.wakeVolumeHolds(hostID)
			return
		}
		if s.volumeHoldNeeds(hostID) {
			s.ensureVolumeList(hostID)
		}
		return
	}
	if err != nil {
		if s.volSnapFailed == nil {
			s.volSnapFailed = make(map[string]struct{})
		}
		s.volSnapFailed[hostID] = struct{}{}
		s.wakeVolumeHolds(hostID)
		return
	}
	snap := make(map[string]struct{}, len(vols))
	for _, info := range vols {
		if info == nil {
			continue
		}
		snap[info.ID] = struct{}{}
		if s.hostVolumeIDs[hostID] == nil {
			if s.hostVolumeIDs == nil {
				s.hostVolumeIDs = make(map[string]map[string]struct{})
			}
			s.hostVolumeIDs[hostID] = make(map[string]struct{})
		}
		s.hostVolumeIDs[hostID][info.ID] = struct{}{}
	}
	if s.volSnap == nil {
		s.volSnap = make(map[string]map[string]struct{})
	}
	delete(s.volSnapFailed, hostID)
	s.volSnap[hostID] = snap
	s.wakeVolumeHolds(hostID)
}

func (s *Scheduler) volumeHoldNeeds(hostID string) bool {
	for _, hold := range s.placementHolds {
		if _, ok := hold.hosts[hostID]; ok {
			return true
		}
	}
	return false
}

func (s *Scheduler) wakeVolumeHolds(hostID string) {
	var pending []*placementHold
	var resume []*PlacementRequest
	for _, hold := range s.placementHolds {
		delete(hold.hosts, hostID)
		if len(hold.hosts) == 0 {
			resume = append(resume, hold.req)
			continue
		}
		pending = append(pending, hold)
	}
	s.placementHolds = pending
	for _, req := range resume {
		s.HandlePlacementRequest(req)
	}
	if len(s.placementHolds) == 0 {
		s.volSnap = nil
		s.volSnapFailed = nil
	}
}

func addJobConflict(err error) bool {
	var je httphelper.JSONError
	if !errors.As(err, &je) {
		return false
	}
	return je.Code == httphelper.ConflictErrorCode || je.Code == httphelper.ObjectExistsErrorCode
}
