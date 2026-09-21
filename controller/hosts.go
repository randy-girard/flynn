package main

import (
	"net/http"

	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
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

	hostCtx, cancel := context.WithTimeout(ctx, statsHostTimeout)
	defer cancel()
	stats, err := getHostStats(hostCtx, h)
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

	httphelper.JSON(w, 200, collectClusterStats(ctx, asStatsHosts(hosts)))
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

	httphelper.JSON(w, 200, collectClusterJobsStats(ctx, asStatsHosts(hosts)))
}
