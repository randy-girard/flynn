package main

import (
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/controllerkey"
	"github.com/randy-girard/flynn/pkg/netpolicy"
)

// resolveAggregatorKey is the cluster key flynn-host sends to the
// logaggregator API (cursor lookups before shipping logs). Cheapest source
// first: the daemon's own env (seeded once any controller lookup succeeds),
// then a controller or logaggregator job on this host, then a lookup across
// the cluster's hosts.
func resolveAggregatorKey(sources ...func() string) string {
	for _, f := range sources {
		if f == nil {
			continue
		}
		if k := f(); k != "" {
			return k
		}
	}
	return ""
}

// localClusterKey is the cluster key from a running controller or
// logaggregator job on this host. Both releases carry it.
func localClusterKey(state *State) string {
	if state == nil {
		return ""
	}
	jobs := map[string]host.ActiveJob{}
	for id, j := range state.Get() {
		if j == nil || j.Job == nil {
			continue
		}
		jobs[id] = *j
	}
	return clusterKeyFromJobs(jobs)
}

func clusterKeyFromJobs(jobs map[string]host.ActiveJob) string {
	if k := controllerkey.FromActiveJobs(jobs); k != "" {
		return k
	}
	for _, j := range jobs {
		if j.Job == nil || j.Job.Metadata["flynn-controller.app_name"] != "logaggregator" || !netpolicy.HostTrusted(j.Job) {
			continue
		}
		if j.Status != host.StatusRunning && j.Status != host.StatusStarting {
			continue
		}
		if k := controllerkey.FromJobEnv(j.Job.Config.Env); k != "" {
			return k
		}
	}
	return ""
}
