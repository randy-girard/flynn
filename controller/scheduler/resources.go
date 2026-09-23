package main

import (
	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
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

func (s *Scheduler) hostReservedResources(hostID string) (mem, cpu int64) {
	if s == nil {
		return 0, 0
	}
	for _, job := range s.jobs {
		if job == nil || job.HostID != hostID || !jobHoldsReservation(job) {
			continue
		}
		m, c := jobResourceRequest(job)
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
	if needMem <= 0 && needCPU <= 0 {
		return true
	}
	if h.MemoryTotalBytes == 0 && h.CPUMilli == 0 {
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

func (s *Scheduler) anyHostMatchesTags(job *Job) bool {
	if s == nil || job == nil {
		return false
	}
	for _, h := range s.hosts {
		if h == nil || h.Shutdown {
			continue
		}
		if job.TagsMatchHost(h) {
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

type runtimeSettingsGetter interface {
	GetRuntimeSettings() (*ct.RuntimeSettings, error)
}

func (s *Scheduler) syncRuntimeSettings(log log15.Logger) {
	if s == nil {
		return
	}
	g, ok := s.ControllerClient.(runtimeSettingsGetter)
	if !ok {
		return
	}
	st, err := g.GetRuntimeSettings()
	if err != nil {
		log.Error("error getting runtime settings", "err", err)
		return
	}
	on := st != nil && st.ReserveResources
	if s.reserveResources != on {
		log.Info("runtime reservation setting", "reserve_resources", on)
	}
	s.reserveResources = on
}

// pickHost chooses a tag-matching host. When reserveResources is on, only
// hosts with remaining requested CPU/memory are eligible (jobs stay pending
// if nothing fits). When it is off, jobs pack onto the least-loaded host.
func (s *Scheduler) pickHost(job *Job, counts map[string]int) *Host {
	if s == nil || job == nil {
		return nil
	}
	var fit *Host
	var fitCount int
	for _, h := range s.ShuffledHosts() {
		if h == nil || h.Shutdown || !job.TagsMatchHost(h) {
			continue
		}
		if s.reserveResources && !s.hostHasCapacity(h, job) {
			continue
		}
		count := hostJobCount(counts, h.ID)
		if count == 0 {
			return h
		}
		fit, fitCount = betterHost(fit, fitCount, h, count)
	}
	return fit
}
