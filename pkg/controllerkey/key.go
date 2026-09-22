package controllerkey

import (
	"strings"

	controller "github.com/randy-girard/flynn/controller/client"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

// FromEnvOrMeta is the discoverd/env lookup used by system jobs. Empty after
// SEC-028 unless CONTROLLER_KEY / AUTH_KEY is in the process environment.
func FromEnvOrMeta(meta map[string]string) string {
	return controller.KeyFromEnvOrMeta(meta)
}

// FromJobEnv returns the cluster controller secret from a job environment.
func FromJobEnv(env map[string]string) string {
	if env == nil {
		return ""
	}
	if k := strings.TrimSpace(env["CONTROLLER_KEY"]); k != "" {
		return k
	}
	return strings.TrimSpace(env["AUTH_KEY"])
}

// FromActiveJobs returns AUTH_KEY / CONTROLLER_KEY from the newest running
// (or starting) controller job. Discoverd no longer publishes AUTH_KEY
// (SEC-028); ListJobs on the host API still includes the job env.
func FromActiveJobs(jobs map[string]host.ActiveJob) string {
	var bestKey string
	var bestTs int64
	for _, j := range jobs {
		if j.Job == nil {
			continue
		}
		if j.Status != host.StatusRunning && j.Status != host.StatusStarting {
			continue
		}
		if j.Job.Metadata["flynn-controller.app_name"] != "controller" {
			continue
		}
		key := FromJobEnv(j.Job.Config.Env)
		if key == "" {
			continue
		}
		ts := j.CreatedAt.Unix()
		if bestKey == "" || ts >= bestTs {
			bestKey = key
			bestTs = ts
		}
	}
	return bestKey
}

// FromHosts lists jobs on each host and returns a controller API key.
func FromHosts(hosts []*cluster.Host) string {
	for _, h := range hosts {
		if h == nil {
			continue
		}
		jobs, err := h.ListJobs()
		if err != nil {
			continue
		}
		if k := FromActiveJobs(jobs); k != "" {
			return k
		}
	}
	return ""
}
