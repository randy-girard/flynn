package main

import (
	"net/http"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

// GetAppJobsStats returns stats for all jobs belonging to a specific app
func (c *controllerAPI) GetAppJobsStats(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)

	hosts, err := c.clusterClient.Hosts()
	if err != nil {
		respondWithError(w, err)
		return
	}

	result := make([]*host.ContainerStats, 0)
	for _, h := range hosts {
		jobsStats, err := h.GetAllJobsStats()
		if err != nil {
			logger.Warn("failed to get jobs stats for host", "host_id", h.ID(), "error", err)
			continue
		}

		// Filter jobs that belong to this app by checking job metadata
		for _, jobStats := range jobsStats.Jobs {
			if !isJobForApp(h, jobStats.JobID, app.ID) {
				continue
			}
			if hideInternal(ctx, app) && jobStatsIsInternal(h, jobStats.JobID) {
				continue
			}
			result = append(result, jobStats)
		}
	}

	httphelper.JSON(w, 200, result)
}

// isJobForApp checks if a job belongs to the given app by examining job metadata
func isJobForApp(h interface {
	ListJobs() (map[string]host.ActiveJob, error)
}, jobID, appID string) bool {
	jobs, err := h.ListJobs()
	if err != nil {
		return false
	}

	job, ok := jobs[jobID]
	if !ok {
		return false
	}

	// Check the job metadata for the app ID
	if job.Job != nil && job.Job.Metadata != nil {
		if jobAppID, exists := job.Job.Metadata["flynn-controller.app"]; exists {
			return jobAppID == appID
		}
	}

	// Fallback: check if job ID contains the app ID prefix
	// Flynn job IDs typically have format: host-uuid-app-uuid
	return strings.Contains(jobID, appID)
}

func jobProcessType(job host.ActiveJob) string {
	if job.Job != nil && job.Job.Metadata != nil {
		return job.Job.Metadata["flynn-controller.type"]
	}
	return ""
}

func jobStatsIsInternal(h interface {
	ListJobs() (map[string]host.ActiveJob, error)
}, jobID string) bool {
	jobs, err := h.ListJobs()
	if err != nil {
		return false
	}
	job, ok := jobs[jobID]
	if !ok {
		return false
	}
	return ct.IsInternalProcessType(jobProcessType(job))
}

// AppJobStats extends ContainerStats with app-specific metadata
type AppJobStats struct {
	*host.ContainerStats
	AppID       string `json:"app_id,omitempty"`
	ReleaseID   string `json:"release_id,omitempty"`
	ProcessType string `json:"process_type,omitempty"`
}

// GetAppJobsStatsEnriched returns stats for all jobs belonging to a specific app with enriched metadata
func (c *controllerAPI) GetAppJobsStatsEnriched(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)

	hosts, err := c.clusterClient.Hosts()
	if err != nil {
		respondWithError(w, err)
		return
	}

	result := make([]*AppJobStats, 0)
	for _, h := range hosts {
		jobsStats, err := h.GetAllJobsStats()
		if err != nil {
			logger.Warn("failed to get jobs stats for host", "host_id", h.ID(), "error", err)
			continue
		}

		jobs, _ := h.ListJobs()

		for _, jobStats := range jobsStats.Jobs {
			job, ok := jobs[jobStats.JobID]
			if !ok {
				continue
			}

			// Check if job belongs to this app
			jobAppID := ""
			releaseID := ""
			processType := ""

			if job.Job != nil && job.Job.Metadata != nil {
				jobAppID = job.Job.Metadata["flynn-controller.app"]
				releaseID = job.Job.Metadata["flynn-controller.release"]
				processType = job.Job.Metadata["flynn-controller.type"]
			}

			if jobAppID != app.ID {
				continue
			}
			if hideInternal(ctx, app) && ct.IsInternalProcessType(processType) {
				continue
			}

			result = append(result, &AppJobStats{
				ContainerStats: jobStats,
				AppID:          jobAppID,
				ReleaseID:      releaseID,
				ProcessType:    processType,
			})
		}
	}

	httphelper.JSON(w, 200, result)
}
