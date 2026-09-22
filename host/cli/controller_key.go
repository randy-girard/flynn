package cli

import (
	"fmt"
	"os"
	"strings"

	controller "github.com/randy-girard/flynn/controller/client"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

// controllerAPIKey is the cluster controller secret for flynn-host CLI.
// Discoverd no longer publishes AUTH_KEY on controller instances (SEC-028).
// Order: process env / host.json (already copied into env), then discoverd
// meta for mixed-version rollouts, then AUTH_KEY on a running controller job.
func controllerAPIKey(meta map[string]string) string {
	if k := controller.KeyFromEnvOrMeta(meta); k != "" {
		return k
	}
	k := lookupControllerKeyFromCluster()
	if k == "" {
		return ""
	}
	if os.Getenv("AUTH_KEY") == "" {
		os.Setenv("AUTH_KEY", k)
	}
	if os.Getenv("CONTROLLER_KEY") == "" {
		os.Setenv("CONTROLLER_KEY", k)
	}
	return k
}

func lookupControllerKeyFromCluster() string {
	hosts, err := cluster.NewClient().Hosts()
	if err != nil {
		return ""
	}
	return controllerKeyFromHosts(hosts)
}

func controllerKeyFromHosts(hosts []*cluster.Host) string {
	for _, h := range hosts {
		if h == nil {
			continue
		}
		jobs, err := h.ListJobs()
		if err != nil {
			continue
		}
		if k := controllerKeyFromActiveJobs(jobs); k != "" {
			return k
		}
	}
	return ""
}

func controllerKeyFromJobEnv(env map[string]string) string {
	if env == nil {
		return ""
	}
	if k := strings.TrimSpace(env["CONTROLLER_KEY"]); k != "" {
		return k
	}
	return strings.TrimSpace(env["AUTH_KEY"])
}

func controllerKeyFromActiveJobs(jobs map[string]host.ActiveJob) string {
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
		key := controllerKeyFromJobEnv(j.Job.Config.Env)
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

func missingControllerKeyErr() error {
	return fmt.Errorf("controller AUTH_KEY is unavailable (discoverd no longer publishes it; set AUTH_KEY or CONTROLLER_KEY, or keep a running controller job)")
}
