package main

import (
	"net/http"
	"strings"
	"sync"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
)

var (
	// statsHostTimeout bounds each host RPC (ListJobs + jobs-stats) so a
	// wedged flynn-host cannot hang the handler. Shared RetryClient has no
	// total Timeout because SSE/attach streams reuse it (REL-001).
	statsHostTimeout = 10 * time.Second
	// statsHostConcurrency caps parallel host fan-out.
	statsHostConcurrency = 8
)

// statsHost is the host RPC surface used by jobs-stats collectors.
type statsHost interface {
	ID() string
	ListJobs() (map[string]host.ActiveJob, error)
	GetAllJobsStats() (*host.AllJobsStats, error)
	GetStats() (*host.HostResourceStats, error)
}

type statsHostContext interface {
	ListJobsContext(ctx context.Context) (map[string]host.ActiveJob, error)
	GetAllJobsStatsContext(ctx context.Context) (*host.AllJobsStats, error)
	GetStatsContext(ctx context.Context) (*host.HostResourceStats, error)
}

func asStatsHosts(hosts []utils.HostClient) []statsHost {
	out := make([]statsHost, len(hosts))
	for i, h := range hosts {
		out[i] = h
	}
	return out
}

func listHostJobs(ctx context.Context, h statsHost) (map[string]host.ActiveJob, error) {
	if c, ok := h.(statsHostContext); ok {
		return c.ListJobsContext(ctx)
	}
	return h.ListJobs()
}

func getAllHostJobsStats(ctx context.Context, h statsHost) (*host.AllJobsStats, error) {
	if c, ok := h.(statsHostContext); ok {
		return c.GetAllJobsStatsContext(ctx)
	}
	return h.GetAllJobsStats()
}

func getHostStats(ctx context.Context, h statsHost) (*host.HostResourceStats, error) {
	if c, ok := h.(statsHostContext); ok {
		return c.GetStatsContext(ctx)
	}
	return h.GetStats()
}

// fetchHostJobsAndStats calls ListJobs once per host and returns the ID index
// plus container stats (PERF-001).
func fetchHostJobsAndStats(ctx context.Context, h statsHost) (map[string]host.ActiveJob, []*host.ContainerStats, error) {
	jobsStats, err := getAllHostJobsStats(ctx, h)
	if err != nil {
		return nil, nil, err
	}
	jobs, err := listHostJobs(ctx, h)
	if err != nil {
		return nil, nil, err
	}
	if jobs == nil {
		jobs = map[string]host.ActiveJob{}
	}
	var list []*host.ContainerStats
	if jobsStats != nil {
		list = jobsStats.Jobs
	}
	return jobs, list, nil
}

func forEachHost(ctx context.Context, hosts []statsHost, fn func(ctx context.Context, h statsHost) error) error {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(statsHostConcurrency)
	for _, h := range hosts {
		h := h
		g.Go(func() error {
			hostCtx, cancel := context.WithTimeout(ctx, statsHostTimeout)
			defer cancel()
			if err := fn(hostCtx, h); err != nil {
				logger.Warn("failed to collect stats from host", "host_id", h.ID(), "error", err)
			}
			return nil
		})
	}
	return g.Wait()
}

// GetAppJobsStats returns stats for all jobs belonging to a specific app
func (c *controllerAPI) GetAppJobsStats(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)

	hosts, err := c.clusterClient.Hosts()
	if err != nil {
		respondWithError(w, err)
		return
	}

	httphelper.JSON(w, 200, collectAppJobsStats(ctx, asStatsHosts(hosts), app.ID, hideInternal(ctx, app)))
}

func collectAppJobsStats(ctx context.Context, hosts []statsHost, appID string, hideInternalJobs bool) []*host.ContainerStats {
	var mu sync.Mutex
	result := make([]*host.ContainerStats, 0)
	_ = forEachHost(ctx, hosts, func(hostCtx context.Context, h statsHost) error {
		jobs, stats, err := fetchHostJobsAndStats(hostCtx, h)
		if err != nil {
			return err
		}
		filtered := filterAppContainerStats(jobs, stats, appID, hideInternalJobs)
		mu.Lock()
		result = append(result, filtered...)
		mu.Unlock()
		return nil
	})
	return result
}

// jobBelongsToApp checks metadata, then the historic job-ID substring fallback.
func jobBelongsToApp(job host.ActiveJob, jobID, appID string) bool {
	if job.Job != nil && job.Job.Metadata != nil {
		if jobAppID, exists := job.Job.Metadata["flynn-controller.app"]; exists {
			return jobAppID == appID
		}
	}
	return strings.Contains(jobID, appID)
}

func jobProcessType(job host.ActiveJob) string {
	if job.Job != nil && job.Job.Metadata != nil {
		return job.Job.Metadata["flynn-controller.type"]
	}
	return ""
}

func filterAppContainerStats(jobs map[string]host.ActiveJob, stats []*host.ContainerStats, appID string, hideInternalJobs bool) []*host.ContainerStats {
	out := make([]*host.ContainerStats, 0)
	for _, jobStats := range stats {
		if jobStats == nil {
			continue
		}
		job, ok := jobs[jobStats.JobID]
		if !ok {
			continue
		}
		if !jobBelongsToApp(job, jobStats.JobID, appID) {
			continue
		}
		if hideInternalJobs && ct.IsInternalProcessType(jobProcessType(job)) {
			continue
		}
		out = append(out, jobStats)
	}
	return out
}

func jobMeta(job host.ActiveJob) (appID, releaseID, processType string) {
	if job.Job == nil || job.Job.Metadata == nil {
		return "", "", ""
	}
	return job.Job.Metadata["flynn-controller.app"],
		job.Job.Metadata["flynn-controller.release"],
		job.Job.Metadata["flynn-controller.type"]
}

// AppJobStats extends ContainerStats with app-specific metadata
type AppJobStats struct {
	*host.ContainerStats
	AppID       string `json:"app_id,omitempty"`
	ReleaseID   string `json:"release_id,omitempty"`
	ProcessType string `json:"process_type,omitempty"`
}

func enrichAppJobStats(jobs map[string]host.ActiveJob, stats []*host.ContainerStats, appID string, hideInternalJobs bool) []*AppJobStats {
	out := make([]*AppJobStats, 0)
	for _, jobStats := range stats {
		if jobStats == nil {
			continue
		}
		job, ok := jobs[jobStats.JobID]
		if !ok {
			continue
		}
		jobAppID, releaseID, processType := jobMeta(job)
		if jobAppID != appID {
			continue
		}
		if hideInternalJobs && ct.IsInternalProcessType(processType) {
			continue
		}
		out = append(out, &AppJobStats{
			ContainerStats: jobStats,
			AppID:          jobAppID,
			ReleaseID:      releaseID,
			ProcessType:    processType,
		})
	}
	return out
}

// GetAppJobsStatsEnriched returns stats for all jobs belonging to a specific app with enriched metadata
func (c *controllerAPI) GetAppJobsStatsEnriched(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)

	hosts, err := c.clusterClient.Hosts()
	if err != nil {
		respondWithError(w, err)
		return
	}

	httphelper.JSON(w, 200, collectAppJobsStatsEnriched(ctx, asStatsHosts(hosts), app.ID, hideInternal(ctx, app)))
}

func collectAppJobsStatsEnriched(ctx context.Context, hosts []statsHost, appID string, hideInternalJobs bool) []*AppJobStats {
	var mu sync.Mutex
	result := make([]*AppJobStats, 0)
	_ = forEachHost(ctx, hosts, func(hostCtx context.Context, h statsHost) error {
		jobs, stats, err := fetchHostJobsAndStats(hostCtx, h)
		if err != nil {
			return err
		}
		enriched := enrichAppJobStats(jobs, stats, appID, hideInternalJobs)
		mu.Lock()
		result = append(result, enriched...)
		mu.Unlock()
		return nil
	})
	return result
}

func collectClusterStats(ctx context.Context, hosts []statsHost) []*host.HostResourceStats {
	var mu sync.Mutex
	result := make([]*host.HostResourceStats, 0, len(hosts))
	_ = forEachHost(ctx, hosts, func(hostCtx context.Context, h statsHost) error {
		stats, err := getHostStats(hostCtx, h)
		if err != nil {
			return err
		}
		mu.Lock()
		result = append(result, stats)
		mu.Unlock()
		return nil
	})
	return result
}

func collectClusterJobsStats(ctx context.Context, hosts []statsHost) []*EnrichedContainerStats {
	var mu sync.Mutex
	result := make([]*EnrichedContainerStats, 0)
	_ = forEachHost(ctx, hosts, func(hostCtx context.Context, h statsHost) error {
		jobsStats, err := getAllHostJobsStats(hostCtx, h)
		if err != nil {
			return err
		}
		jobs, err := listHostJobs(hostCtx, h)
		if err != nil || jobs == nil {
			jobs = map[string]host.ActiveJob{}
		}
		var list []*host.ContainerStats
		if jobsStats != nil {
			list = jobsStats.Jobs
		}
		enriched := enrichClusterJobStats(h.ID(), jobs, list)
		mu.Lock()
		result = append(result, enriched...)
		mu.Unlock()
		return nil
	})
	return result
}

func enrichClusterJobStats(hostID string, jobs map[string]host.ActiveJob, stats []*host.ContainerStats) []*EnrichedContainerStats {
	out := make([]*EnrichedContainerStats, 0, len(stats))
	for _, jobStats := range stats {
		if jobStats == nil {
			continue
		}
		enriched := &EnrichedContainerStats{
			ContainerStats: jobStats,
			HostID:         hostID,
		}
		if job, ok := jobs[jobStats.JobID]; ok {
			appID, releaseID, processType := jobMeta(job)
			enriched.AppID = appID
			enriched.ReleaseID = releaseID
			enriched.ProcessType = processType
			if job.Job != nil && job.Job.Metadata != nil {
				enriched.AppName = job.Job.Metadata["flynn-controller.app_name"]
			}
		}
		out = append(out, enriched)
	}
	return out
}
