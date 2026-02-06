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

// GetClusterJobsStats returns stats for all jobs running across all hosts
func (c *controllerAPI) GetClusterJobsStats(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	hosts, err := c.clusterClient.Hosts()
	if err != nil {
		respondWithError(w, err)
		return
	}

	result := make([]*host.ContainerStats, 0)
	for _, h := range hosts {
		jobsStats, err := h.GetAllJobsStats()
		if err != nil {
			// Log but continue - don't fail entire request for one host
			logger.Warn("failed to get jobs stats for host", "host_id", h.ID(), "error", err)
			continue
		}
		result = append(result, jobsStats.Jobs...)
	}

	httphelper.JSON(w, 200, result)
}

