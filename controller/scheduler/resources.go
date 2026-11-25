package main

import (
	"strings"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/placement"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	"github.com/randy-girard/flynn/host/resource"
)

func jobResourceRequest(job *Job) (mem, cpu int64) {
	if job == nil || job.Formation == nil || job.Formation.Release == nil {
		return 0, 0
	}
	t, ok := job.Formation.Release.Processes[job.Type]
	if !ok {
		return 0, 0
	}
	if spec, ok := t.Resources[resource.TypeMemory]; ok && spec.Request != nil {
		mem = *spec.Request
	}
	if spec, ok := t.Resources[resource.TypeCPU]; ok && spec.Request != nil {
		cpu = *spec.Request
	}
	return mem, cpu
}

func jobRuntimeProfileName(job *Job) string {
	if job == nil || job.Formation == nil || job.Formation.Release == nil {
		return ""
	}
	t, ok := job.Formation.Release.Processes[job.Type]
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(t.RuntimeProfile))
}

func jobHoldsReservation(job *Job) bool {
	if job == nil || job.HostID == "" {
		return false
	}
	switch job.State {
	case JobStatePending, JobStateStarting, JobStateRunning, JobStateStopping:
		return true
	default:
		return false
	}
}

// schedulerTenancyHosted matches controller/tenancy.ModeHosted. This package
// does not import tenancy; that package pulls scrypt into the scheduler binary.
const schedulerTenancyHosted = "hosted"

func hostedBuiltinProfile(name string) bool {
	switch name {
	case resource.ProfileSmall, resource.ProfileMedium, resource.ProfileLarge:
		return true
	default:
		return false
	}
}

func (s *Scheduler) jobReservesResources(job *Job) bool {
	if s == nil {
		return false
	}
	name := jobRuntimeProfileName(job)
	if name == "" {
		return false
	}
	if s.profileReserve[name] {
		return true
	}
	return s.tenancyMode == schedulerTenancyHosted && hostedBuiltinProfile(name)
}

func (s *Scheduler) hostReservedResources(hostID string) (mem, cpu int64) {
	if s == nil {
		return 0, 0
	}
	for _, job := range s.jobs {
		if job == nil || job.HostID != hostID || !jobHoldsReservation(job) {
			continue
		}
		if !s.jobReservesResources(job) {
			continue
		}
		m, c := jobResourceRequest(job)
		if m <= 0 && c <= 0 {
			// Guaranteed runtime whose Request was not copied onto the
			// process: count the cap so the host still cannot overbook.
			if job.Formation != nil && job.Formation.Release != nil {
				if t, ok := job.Formation.Release.Processes[job.Type]; ok {
					if spec, ok := t.Resources[resource.TypeMemory]; ok && spec.Limit != nil {
						m = *spec.Limit
					}
					if spec, ok := t.Resources[resource.TypeCPU]; ok && spec.Limit != nil {
						c = *spec.Limit
					}
				}
			}
		}
		mem += m
		cpu += c
	}
	return mem, cpu
}

func (s *Scheduler) hostHasCapacity(h *Host, job *Job) bool {
	if h == nil {
		return false
	}
	needMem, needCPU := jobResourceRequest(job)
	if needMem <= 0 && needCPU <= 0 && s.jobReservesResources(job) {
		if job.Formation != nil && job.Formation.Release != nil {
			if t, ok := job.Formation.Release.Processes[job.Type]; ok {
				if spec, ok := t.Resources[resource.TypeMemory]; ok && spec.Limit != nil {
					needMem = *spec.Limit
				}
				if spec, ok := t.Resources[resource.TypeCPU]; ok && spec.Limit != nil {
					needCPU = *spec.Limit
				}
			}
		}
	}
	if needMem <= 0 && needCPU <= 0 {
		return true
	}
	// Both totals unset means this process has never read the host. A
	// reserving job must not treat that as infinite capacity. Shared jobs
	// still place. A single zero field skips that dimension below, and a
	// failed refresh leaves the last good sample in place.
	if h.MemoryTotalBytes == 0 && h.CPUMilli == 0 {
		if s.jobReservesResources(job) {
			return false
		}
		return true
	}
	usedMem, usedCPU := s.hostReservedResources(h.ID)
	if h.MemoryTotalBytes > 0 && needMem > 0 && uint64(usedMem+needMem) > h.MemoryTotalBytes {
		return false
	}
	if h.CPUMilli > 0 && needCPU > 0 && usedCPU+needCPU > h.CPUMilli {
		return false
	}
	return true
}

// hostCanPlace is whether the scheduler may start jobs on this host.
// Shutdown is a graceful drain. Healthy=false is a crash (discoverd down)
// still in the map until PerformHostChecks unfollows.
func hostCanPlace(h *Host) bool {
	return h != nil && !h.Shutdown && h.Healthy
}

func (s *Scheduler) anyHostMatchesTags(job *Job) bool {
	if s == nil || job == nil {
		return false
	}
	for _, h := range s.hosts {
		if !hostCanPlace(h) {
			continue
		}
		if s.jobMatchesHost(job, h) {
			return true
		}
	}
	return false
}

// anyHostMatchesPlacement is anyHostMatchesTags plus the split role filter.
// A core host that matches process tags is not capacity for a user job.
func (s *Scheduler) anyHostMatchesPlacement(job *Job) bool {
	if s == nil || job == nil {
		return false
	}
	for _, h := range s.hosts {
		if hostCanPlace(h) && s.jobMatchesHost(job, h) && s.hostAllowsRole(job, h) {
			return true
		}
	}
	return false
}

// placementMode is mixed until the first successful read, and whenever the
// stored value is not split.
func (s *Scheduler) placementMode() string {
	if s == nil || strings.TrimSpace(s.workloadPlacement) != placement.ModeSplit {
		return placement.ModeMixed
	}
	return placement.ModeSplit
}

// hostAllowsRole applies flynn-role only while split is on. It is not part of
// jobMatchesHost, so stopJobsWithMismatchedTags does not stop a job because
// the mode or the role tag changed.
func (s *Scheduler) hostAllowsRole(job *Job, h *Host) bool {
	if job == nil || h == nil {
		return false
	}
	_, omni := job.omniPerHost()
	var app *ct.App
	if job.Formation != nil {
		app = job.Formation.App
	}
	return placement.HostAllows(s.placementMode(), placement.Classify(app, omni), h.Tags)
}

// volumePinsTo reports whether this job already has a persistent volume on hostID.
func (s *Scheduler) volumePinsTo(job *Job, hostID string) bool {
	if s == nil || job == nil || hostID == "" {
		return false
	}
	for _, req := range job.VolumeRequests() {
		vol := s.findVolume(job, &req)
		if vol != nil && vol.HostID == hostID {
			return true
		}
	}
	return false
}

func hostJobCount(counts map[string]int, hostID string) int {
	if counts == nil {
		return 0
	}
	return counts[hostID]
}

func betterHost(cur *Host, curCount int, next *Host, nextCount int) (*Host, int) {
	if cur == nil || nextCount < curCount || (nextCount == 0 && curCount > 0) {
		return next, nextCount
	}
	return cur, curCount
}

type runtimeProfileLister interface {
	ListRuntimeProfiles() ([]*ct.RuntimeProfile, error)
}

func (s *Scheduler) syncRuntimeProfiles(log log15.Logger) {
	list, ok, err := s.fetchRuntimeProfiles()
	s.applyRuntimeProfiles(log, list, ok, err)
}

type tenancyGetter interface {
	GetTenancy() (*ct.TenancySettings, error)
}

// syncTenancy loads cluster_settings.tenancy_mode. A failed read keeps the
// last mode. Before the first success the mode stays empty, so only the
// stored runtime flag reserves.
func (s *Scheduler) syncTenancy(log log15.Logger) {
	settings, ok, err := s.fetchTenancy()
	s.applyTenancy(log, settings, ok, err)
}

func (j *Job) countsTowardHostProcessCap() bool {
	if j == nil || j.Formation == nil || j.Formation.App == nil {
		return false
	}
	if _, omni := j.omniPerHost(); omni {
		return false
	}
	switch j.State {
	case JobStatePending, JobStateStarting, JobStateRunning, JobStateStopping:
	default:
		return false
	}
	meta := j.Formation.App.Meta
	if meta["flynn-datastore"] == "true" {
		return true
	}
	return meta["flynn-system-app"] != "true"
}

func (s *Scheduler) hostTenantProcessCount(hostID string) int {
	if s == nil {
		return 0
	}
	n := 0
	for _, job := range s.jobs {
		if job == nil || job.HostID != hostID || !job.countsTowardHostProcessCap() {
			continue
		}
		n++
	}
	return n
}

func (s *Scheduler) hostAtProcessCap(h *Host, placing *Job) bool {
	if h == nil {
		return true
	}
	max, ok := ct.ParseHostMaxProcesses(h.Tags)
	if !ok {
		return false
	}
	n := s.hostTenantProcessCount(h.ID)
	if placing != nil && placing.HostID == h.ID && placing.countsTowardHostProcessCap() {
		// already counted
		return n > max
	}
	if placing != nil && placing.countsTowardHostProcessCap() {
		return n >= max
	}
	return n >= max
}

type runtimeSettingsGetter interface {
	GetRuntimeSettings() (*ct.RuntimeSettings, error)
}

// syncRuntimeSettings loads the cluster restart limit. A changed limit
// clears exhausted processes so they get a fresh set of restarts.
func (s *Scheduler) syncRuntimeSettings(log log15.Logger) {
	settings, ok, err := s.fetchRuntimeSettings()
	s.applyRuntimeSettings(log, settings, ok, err)
}

func (s *Scheduler) setMaxRestarts(n int) {
	if n < 0 {
		n = 0
	}
	if n == s.maxRestarts {
		return
	}
	s.logger.Info("cluster restart limit changed", "fn", "setMaxRestarts", "from", s.maxRestarts, "to", n)
	s.maxRestarts = n
	for key := range s.restartsExhausted {
		s.triggerRectify(key)
	}
	s.restartsExhausted = make(map[utils.FormationKey]Processes)
}

// restartLimitReached reports whether a crashed formation job has used its
// restarts. System apps always restart so the cluster can recover.
func (s *Scheduler) restartLimitReached(job *Job, restarts uint) bool {
	if s.maxRestarts <= 0 || job == nil || job.Formation == nil {
		return false
	}
	if app := job.Formation.App; app != nil && app.System() {
		return false
	}
	return restarts >= uint(s.maxRestarts)
}

func (s *Scheduler) hostPersistentSingletonCount(hostID string) int {
	if s == nil {
		return 0
	}
	n := 0
	for _, job := range s.jobs {
		if job == nil || job.HostID != hostID {
			continue
		}
		if job.State == JobStateStopped || job.State == JobStateStopping {
			continue
		}
		if job.isPersistentSingleton() {
			n++
		}
	}
	return n
}

// hostAtPersistentSingletonCap reports whether hostID is already at
// flynn-max-persistent-singletons. A missing or non-numeric tag is not a cap.
// The placing job is already included in the count only when its HostID is
// this host.
func (s *Scheduler) hostAtPersistentSingletonCap(h *Host, placing *Job) bool {
	if h == nil {
		return true
	}
	max, ok := ct.ParseHostPersistentSingletons(h.Tags)
	if !ok {
		return false
	}
	n := s.hostPersistentSingletonCount(h.ID)
	if placing != nil && placing.HostID == h.ID && placing.isPersistentSingleton() && !placing.spreadsAcrossHosts() {
		return n > max
	}
	return n >= max
}

// persistentSingletonCapBlocked reports whether pickHost returned nil because
// every host that otherwise fit is at the persistent-singleton cap. Tag
// mismatch, the process cap, omni, and reserved CPU/memory stay their own errors.
func (s *Scheduler) persistentSingletonCapBlocked(job *Job) bool {
	if s == nil || job == nil || !job.isPersistentSingleton() || job.spreadsAcrossHosts() {
		return false
	}
	_, omni := job.omniPerHost()
	if omni {
		return false
	}
	needReserve := s.jobReservesResources(job)
	saw := false
	for _, h := range s.hosts {
		if !hostCanPlace(h) || !s.jobMatchesHost(job, h) || !s.hostAllowsRole(job, h) || s.hostAtProcessCap(h, job) {
			continue
		}
		if needReserve && !s.hostHasCapacity(h, job) {
			continue
		}
		saw = true
		if !s.hostAtPersistentSingletonCap(h, job) {
			return false
		}
	}
	return saw
}

// wakeBlockedPersistentSingletons retries databases that were blocked because
// every eligible host was at the singleton cap. maybeStartBlockedJobs still
// runs on host sync and tag changes; this is the immediate path when a
// counted job is gone and is not being replaced.
func (s *Scheduler) wakeBlockedPersistentSingletons() {
	if s == nil {
		return
	}
	for _, job := range s.jobs {
		if job == nil || job.State != JobStateBlocked || !job.isPersistentSingleton() || job.spreadsAcrossHosts() {
			continue
		}
		job.State = JobStatePending
		go s.StartJob(job)
	}
}

// dropPendingVolume forgets a volume that never got past CreateVolume.
// A created volume is left alone so its HostID keeps the data on that host.
func (s *Scheduler) dropPendingVolume(vol *Volume) {
	if s == nil || vol == nil || vol.GetState() != ct.VolumeStatePending {
		return
	}
	if s.volumes != nil {
		delete(s.volumes, vol.ID)
	}
	if set := s.hostVolumeIDs[vol.HostID]; set != nil {
		delete(set, vol.ID)
	}
	vol.JobID = nil
	vol.HostID = ""
}

// onLoop runs fn on the scheduler goroutine and waits. Tests and startup,
// which have no loop, run it inline. StartJob uses this so a pending volume
// is gone before the next placement reads s.volumes.
func (s *Scheduler) onLoop(fn func()) {
	if s == nil || fn == nil {
		return
	}
	if !s.loopActive || s.loopApply == nil {
		fn()
		return
	}
	done := make(chan struct{})
	s.deliver(func() {
		fn()
		close(done)
	})
	select {
	case <-done:
	case <-s.stop:
	}
}

// omniHostAvailable reports whether a placeable, tag-matching host still
// lacks its share of the job's omni type. Non-omni jobs always report true.
func (s *Scheduler) omniHostAvailable(job *Job) bool {
	perHost, omni := job.omniPerHost()
	if !omni {
		return true
	}
	counts := s.jobs.GetHostJobCounts(job.Formation.key(), job.Type)
	for _, h := range s.hosts {
		if hostCanPlace(h) && s.jobMatchesHost(job, h) && hostJobCount(counts, h.ID) < perHost {
			return true
		}
	}
	return false
}

// pickHost chooses a tag-matching host. Jobs whose runtime reserves
// resources only land on hosts with remaining requested CPU/memory. Nothing
// that fits leaves the job blocked. Shared runtimes pack onto the
// least-loaded host and keep their CPU/memory as caps only.
//
// Persistent singletons (one broker/volume per resource, and not a peer that
// already spreads across hosts) land on the eligible host with the fewest
// such jobs. Same-type count is only the tie-break. Deleting this branch
// would return the first shuffled host with a zero same-type count, and a
// fresh shuffle per job can put three new databases on one host.
//
// Omni jobs (discoverd, flannel) run a fixed count on every host. A host
// that already runs its share is skipped: restarting a lost host's omni job
// on a live host doubled it there, and the surplus stop could then take
// down the live host's original.
func (s *Scheduler) pickHost(job *Job, counts map[string]int) *Host {
	if s == nil || job == nil {
		return nil
	}
	// A retry after AddJob failed prefers any other eligible host. A random
	// pick between two hosts kept choosing the refusing one for eleven
	// retries in CI. The refusing host is still used if nothing else fits.
	//
	// A replica that spreads across hosts goes back to the refusing host
	// while that host is the only one with fewer replicas, for up to
	// maxSpreadRetriesOnRefusingHost refusals in a row. A single
	// "ratelimited" refusal (flynn-host runs a bounded number of AddJob
	// calls at once) otherwise put both controller web jobs on one host
	// when another host was lost (EDGE-112).
	if failed := job.addJobFailedHost; failed != "" {
		alt := s.pickHostExcept(job, counts, failed)
		if alt == nil {
			return s.pickHostExcept(job, counts, "")
		}
		if !job.spreadsAcrossHosts() || job.isPersistentSingleton() || job.addJobFailures >= maxSpreadRetriesOnRefusingHost ||
			hostJobCount(counts, alt.ID) <= hostJobCount(counts, failed) {
			return alt
		}
		if h := s.pickHostExcept(job, counts, ""); h != nil {
			return h
		}
		return alt
	}
	return s.pickHostExcept(job, counts, "")
}

// maxSpreadRetriesOnRefusingHost is how many AddJob refusals in a row a
// spreading replica takes from the only host without a replica before it is
// placed next to another replica. StartJob waits 0.5 s, 1 s and 1.5 s
// between them.
const maxSpreadRetriesOnRefusingHost = 3

// noteAddJobFailure runs on the loop after hostID refused AddJob for job, so
// the next placement of the job tries another host first.
func (s *Scheduler) noteAddJobFailure(job *Job, hostID string) {
	if job == nil {
		return
	}
	if job.addJobFailedHost == hostID {
		job.addJobFailures++
	} else {
		job.addJobFailures = 1
	}
	job.addJobFailedHost = hostID
}

// hostActiveJobCounts counts pending (placed), starting and running jobs per
// host, of every app. A spreading replica uses it to break ties between
// hosts with the same number of replicas.
func (s *Scheduler) hostActiveJobCounts() map[string]int {
	counts := make(map[string]int)
	for _, j := range s.jobs {
		if j == nil || j.HostID == "" {
			continue
		}
		switch j.State {
		case JobStatePending, JobStateStarting, JobStateRunning:
			counts[j.HostID]++
		}
	}
	return counts
}

func (s *Scheduler) pickHostExcept(job *Job, counts map[string]int, skip string) *Host {
	needReserve := s.jobReservesResources(job)
	spreadPersist := job.isPersistentSingleton() && !job.spreadsAcrossHosts()
	omniPerHost, omni := job.omniPerHost()
	var omniCounts map[string]int
	if omni {
		omniCounts = s.jobs.GetHostJobCounts(job.Formation.key(), job.Type)
	}
	var fit *Host
	var fitCount int
	var fitPack int
	var fitLoad int
	var spreadLoad map[string]int
	if !spreadPersist && !omni && job.spreadsAcrossHosts() {
		spreadLoad = s.hostActiveJobCounts()
	}
	for _, h := range s.ShuffledHosts() {
		if h.ID == skip || !hostCanPlace(h) || !s.jobMatchesHost(job, h) || !s.hostAllowsRole(job, h) {
			continue
		}
		if s.hostAtProcessCap(h, job) {
			continue
		}
		if omni && hostJobCount(omniCounts, h.ID) >= omniPerHost {
			continue
		}
		if needReserve && !s.hostHasCapacity(h, job) {
			continue
		}
		count := hostJobCount(counts, h.ID)
		if spreadPersist {
			if s.hostAtPersistentSingletonCap(h, job) {
				continue
			}
			pack := s.hostPersistentSingletonCount(h.ID)
			if fit == nil || pack < fitPack || (pack == fitPack && count < fitCount) {
				fit, fitCount, fitPack = h, count, pack
			}
			continue
		}
		if spreadLoad != nil {
			// Replicas of this app and type first, then the host's load.
			// Hosts come shuffled, so equal hosts are picked at random.
			load := spreadLoad[h.ID]
			if fit == nil || count < fitCount || (count == fitCount && load < fitLoad) {
				fit, fitCount, fitLoad = h, count, load
			}
			continue
		}
		if count == 0 {
			return h
		}
		fit, fitCount = betterHost(fit, fitCount, h, count)
	}
	return fit
}
