package main

import (
	"strings"

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

func (s *Scheduler) jobReservesResources(job *Job) bool {
	if s == nil {
		return false
	}
	name := jobRuntimeProfileName(job)
	if name == "" {
		return false
	}
	return s.profileReserve[name]
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

type runtimeProfileLister interface {
	ListRuntimeProfiles() ([]*ct.RuntimeProfile, error)
}

func (s *Scheduler) syncRuntimeProfiles(log log15.Logger) {
	if s == nil {
		return
	}
	g, ok := s.ControllerClient.(runtimeProfileLister)
	if !ok {
		return
	}
	list, err := g.ListRuntimeProfiles()
	if err != nil {
		log.Error("error listing runtime profiles", "err", err)
		return
	}
	next := make(map[string]bool, len(list))
	for _, p := range list {
		if p == nil {
			continue
		}
		next[strings.ToLower(strings.TrimSpace(p.Name))] = p.ReserveResources
	}
	s.profileReserve = next
}

// pickHost chooses a tag-matching host. Jobs whose runtime guarantees
// resources only land on hosts with remaining requested CPU/memory (they stay
// pending if nothing fits). Shared runtimes pack onto the least-loaded host
// and keep their CPU/memory as caps only.
func (s *Scheduler) pickHost(job *Job, counts map[string]int) *Host {
	if s == nil || job == nil {
		return nil
	}
	needReserve := s.jobReservesResources(job)
	var fit *Host
	var fitCount int
	for _, h := range s.ShuffledHosts() {
		if h == nil || h.Shutdown || !job.TagsMatchHost(h) {
			continue
		}
		if needReserve && !s.hostHasCapacity(h, job) {
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
