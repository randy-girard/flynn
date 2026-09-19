package main

import (
	"time"

	host "github.com/randy-girard/flynn/host/types"
)

const containerMetricsLogInterval = 30 * time.Second

func (h *Host) startContainerMetricsLogs() {
	go h.loopContainerMetricsLogs(containerMetricsLogInterval)
}

func (h *Host) loopContainerMetricsLogs(interval time.Duration) {
	if interval <= 0 {
		interval = containerMetricsLogInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	h.writeContainerMetricsLogs()
	for range ticker.C {
		h.writeContainerMetricsLogs()
	}
}

func (h *Host) writeContainerMetricsLogs() {
	if h == nil || h.state == nil || h.state.logMux == nil || h.backend == nil {
		return
	}
	for _, job := range h.state.GetActive() {
		if job == nil || job.Status != host.StatusRunning {
			continue
		}
		stats, err := h.backend.GetJobStats(job.Job.ID)
		if err != nil || stats == nil {
			continue
		}
		line := host.FormatContainerMetricsLog(stats)
		if line == "" {
			continue
		}
		h.state.writeLifecycleLog(job, line)
	}
}
