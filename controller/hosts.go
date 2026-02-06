package main

import (
	"net/http"

	host "github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/ctxhelper"
	"github.com/flynn/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

// HostInfo represents basic information about a host in the cluster
type HostInfo struct {
	ID   string            `json:"id"`
	Tags map[string]string `json:"tags"`
	Addr string            `json:"addr"`
}

// GetHosts returns a list of all hosts in the cluster
func (c *controllerAPI) GetHosts(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	hosts, err := c.clusterClient.Hosts()
	if err != nil {
		respondWithError(w, err)
		return
	}

	result := make([]HostInfo, len(hosts))
	for i, h := range hosts {
		result[i] = HostInfo{
			ID:   h.ID(),
			Tags: h.Tags(),
			Addr: h.Addr(),
		}
	}
	httphelper.JSON(w, 200, result)
}

// GetHostStats returns resource usage stats for a specific host
func (c *controllerAPI) GetHostStats(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	hostID := params.ByName("host_id")

	h, err := c.clusterClient.Host(hostID)
	if err != nil {
		respondWithError(w, err)
		return
	}

	stats, err := h.GetStats()
	if err != nil {
		respondWithError(w, err)
		return
	}

	httphelper.JSON(w, 200, stats)
}

// GetClusterStats returns resource usage stats for all hosts in the cluster
func (c *controllerAPI) GetClusterStats(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	hosts, err := c.clusterClient.Hosts()
	if err != nil {
		respondWithError(w, err)
		return
	}

	result := make([]*host.HostResourceStats, 0, len(hosts))
	for _, h := range hosts {
		stats, err := h.GetStats()
		if err != nil {
			// Log but continue - don't fail entire request for one host
			logger.Warn("failed to get stats for host", "host_id", h.ID(), "error", err)
			continue
		}
		result = append(result, stats)
	}

	httphelper.JSON(w, 200, result)
}

// EnrichedContainerStats extends ContainerStats with job metadata
type EnrichedContainerStats struct {
	*host.ContainerStats
	HostID      string `json:"host_id"`
	AppID       string `json:"app_id,omitempty"`
	AppName     string `json:"app_name,omitempty"`
	ReleaseID   string `json:"release_id,omitempty"`
	ProcessType string `json:"process_type,omitempty"`
}

// GetClusterJobsStats returns stats for all jobs running across all hosts with enriched metadata
func (c *controllerAPI) GetClusterJobsStats(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	hosts, err := c.clusterClient.Hosts()
	if err != nil {
		respondWithError(w, err)
		return
	}

	result := make([]*EnrichedContainerStats, 0)
	for _, h := range hosts {
		jobsStats, err := h.GetAllJobsStats()
		if err != nil {
			// Log but continue - don't fail entire request for one host
			logger.Warn("failed to get jobs stats for host", "host_id", h.ID(), "error", err)
			continue
		}

		// Get job metadata to enrich stats
		jobs, _ := h.ListJobs()

		for _, jobStats := range jobsStats.Jobs {
			enriched := &EnrichedContainerStats{
				ContainerStats: jobStats,
				HostID:         h.ID(),
			}

			// Try to get metadata from job
			if job, ok := jobs[jobStats.JobID]; ok && job.Job != nil && job.Job.Metadata != nil {
				enriched.AppID = job.Job.Metadata["flynn-controller.app"]
				enriched.AppName = job.Job.Metadata["flynn-controller.app_name"]
				enriched.ReleaseID = job.Job.Metadata["flynn-controller.release"]
				enriched.ProcessType = job.Job.Metadata["flynn-controller.type"]
			}

			result = append(result, enriched)
		}
	}

	httphelper.JSON(w, 200, result)
}
