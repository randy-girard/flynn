package main

import (
	"context"
	"errors"
	"sync"

	"github.com/randy-girard/flynn/controller/utils"
	host "github.com/randy-girard/flynn/host/types"
)

// capacitySample is one host's stats read. err or a nil stats leaves the
// scheduler's last totals unchanged.
type capacitySample struct {
	id    string
	stats *host.HostResourceStats
	err   error
}

type capacityStatsContext interface {
	GetStatsContext(ctx context.Context) (*host.HostResourceStats, error)
}

func hostCapacityStats(ctx context.Context, h utils.HostClient) (*host.HostResourceStats, error) {
	if h == nil {
		return nil, errors.New("host has no client")
	}
	if c, ok := h.(capacityStatsContext); ok {
		return c.GetStatsContext(ctx)
	}
	return h.GetStats()
}

// beginCapacityRefresh samples placeable hosts off the scheduler loop.
// Only the leader starts a round. One round runs at a time; a later request
// coalesces into one more round. Totals are applied on the loop.
func (s *Scheduler) beginCapacityRefresh() {
	if s == nil || !s.loopActive || !s.IsLeader() {
		return
	}
	if s.capacityInFlight {
		s.capacityAgain = true
		return
	}
	targets := s.capacityRefreshTargets()
	if len(targets) == 0 {
		return
	}
	s.capacityInFlight = true
	s.capacityAgain = false
	go func() {
		samples := collectCapacity(targets)
		s.deliver(func() {
			s.applyHostCapacity(samples)
			s.finishCapacityRefresh()
		})
	}()
}

func (s *Scheduler) finishCapacityRefresh() {
	again := s.capacityAgain
	s.capacityAgain = false
	s.capacityInFlight = false
	if again && s.loopActive && s.IsLeader() {
		s.beginCapacityRefresh()
	}
}

func (s *Scheduler) capacityRefreshTargets() []hostRef {
	if s == nil {
		return nil
	}
	out := make([]hostRef, 0, len(s.hosts))
	for id, h := range s.hosts {
		if !hostCanPlace(h) || h.client == nil {
			continue
		}
		out = append(out, hostRef{id: id, client: h.client})
	}
	return out
}

func collectCapacity(targets []hostRef) []capacitySample {
	samples := make([]capacitySample, len(targets))
	if len(targets) == 0 {
		return samples
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, capacityRefreshConcurrency)
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t hostRef) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), capacityRefreshTimeout)
			defer cancel()
			stats, err := hostCapacityStats(ctx, t.client)
			samples[i] = capacitySample{id: t.id, stats: stats, err: err}
		}(i, t)
	}
	wg.Wait()
	return samples
}

func (s *Scheduler) applyHostCapacity(samples []capacitySample) {
	for _, sample := range samples {
		h := s.hosts[sample.id]
		if h == nil {
			continue
		}
		if sample.err != nil || sample.stats == nil {
			if s.logger != nil {
				s.logger.Warn("host capacity refresh failed", "fn", "applyHostCapacity", "host.id", sample.id, "err", sample.err)
			}
			continue
		}
		changed, grew := applyCapacityTotals(h, sample.stats)
		if !changed {
			continue
		}
		if s.logger != nil {
			s.logger.Info("host capacity updated", "fn", "applyHostCapacity", "host.id", h.ID, "memory_total_bytes", h.MemoryTotalBytes, "cpu_milli", h.CPUMilli)
		}
		if grew {
			s.maybeStartBlockedJobs(h)
		}
	}
}

// applyCapacityTotals copies a successful sample onto the host. A zero
// field is not a reading: it must not clear the other total or the last
// good value. grew is true when either total became larger, which is the
// only change that can make a blocked reserving job fit.
func applyCapacityTotals(h *Host, stats *host.HostResourceStats) (changed, grew bool) {
	if h == nil || stats == nil {
		return false, false
	}
	if stats.MemoryTotalBytes > 0 && stats.MemoryTotalBytes != h.MemoryTotalBytes {
		if stats.MemoryTotalBytes > h.MemoryTotalBytes {
			grew = true
		}
		h.MemoryTotalBytes = stats.MemoryTotalBytes
		changed = true
	}
	if stats.CPUCount > 0 {
		cpu := int64(stats.CPUCount) * 1000
		if cpu != h.CPUMilli {
			if cpu > h.CPUMilli {
				grew = true
			}
			h.CPUMilli = cpu
			changed = true
		}
	}
	return changed, grew
}
