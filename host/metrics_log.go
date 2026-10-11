package main

import (
	"net/http"
	"sync"
	"time"

	host "github.com/randy-girard/flynn/host/types"
)

const metricsFragmentTimeout = 2 * time.Second

var metricsFragmentClient = &http.Client{Timeout: metricsFragmentTimeout}

func (h *Host) startContainerMetricsLogs() {
	go h.loopContainerMetricsLogs()
}

func (h *Host) loopContainerMetricsLogs() {
	for {
		time.Sleep(host.NextMetricsTick(time.Now()))
		h.writeContainerMetricsLogs()
	}
}

func (h *Host) writeContainerMetricsLogs() {
	if h == nil || h.state == nil || h.state.logMux == nil || h.backend == nil {
		return
	}
	var wg sync.WaitGroup
	for _, job := range h.state.GetActive() {
		if job == nil || job.Job == nil || job.Status != host.StatusRunning {
			continue
		}
		stats, err := h.backend.GetJobStats(job.Job.ID)
		if err != nil || stats == nil {
			continue
		}
		port := job.Job.Config.Env[host.MetricsPortEnv]
		if port == "" || job.InternalIP == "" {
			if line := host.FormatJobMetricsLog(stats, ""); line != "" {
				h.state.writeLifecycleLog(job, line)
			}
			continue
		}
		wg.Add(1)
		go func(job *host.ActiveJob, stats *host.ContainerStats, port string) {
			defer wg.Done()
			fragment := host.FetchMetricsFragment(metricsFragmentClient, job.InternalIP, port)
			if line := host.FormatJobMetricsLog(stats, fragment); line != "" {
				h.state.writeLifecycleLog(job, line)
			}
		}(job, stats, port)
	}
	wg.Wait()
}
