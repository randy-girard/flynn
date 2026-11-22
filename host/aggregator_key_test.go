package main

import (
	"testing"

	host "github.com/randy-girard/flynn/host/types"
)

func TestResolveAggregatorKeyOrder(t *testing.T) {
	calls := 0
	never := func() string { calls++; return "cluster" }
	if got := resolveAggregatorKey(func() string { return "env" }, never); got != "env" || calls != 0 {
		t.Fatalf("got %q calls=%d: env must win without a cluster lookup", got, calls)
	}
	if got := resolveAggregatorKey(func() string { return "" }, nil, func() string { return "local" }, never); got != "local" {
		t.Fatalf("got %q", got)
	}
	if got := resolveAggregatorKey(func() string { return "" }, never); got != "cluster" {
		t.Fatalf("got %q", got)
	}
}

func TestClusterKeyFromJobsUsesControllerOrLogaggregator(t *testing.T) {
	job := func(app, key string, st host.JobStatus) host.ActiveJob {
		return host.ActiveJob{Status: st, Job: &host.Job{
			Metadata: map[string]string{"flynn-controller.app_name": app, "flynn-system-app": "true"},
			Config:   host.ContainerConfig{Env: map[string]string{"CONTROLLER_KEY": key}},
		}}
	}
	if got := clusterKeyFromJobs(map[string]host.ActiveJob{"a": job("logaggregator", "lk", host.StatusRunning)}); got != "lk" {
		t.Fatalf("logaggregator job: %q", got)
	}
	if got := clusterKeyFromJobs(map[string]host.ActiveJob{
		"a": job("logaggregator", "lk", host.StatusRunning),
		"b": job("controller", "ck", host.StatusRunning),
	}); got != "ck" {
		t.Fatalf("controller first: %q", got)
	}
	untrusted := job("logaggregator", "forged", host.StatusRunning)
	delete(untrusted.Job.Metadata, "flynn-system-app")
	if got := clusterKeyFromJobs(map[string]host.ActiveJob{"a": untrusted}); got != "" {
		t.Fatalf("non-system logaggregator job: %q", got)
	}
	if got := clusterKeyFromJobs(map[string]host.ActiveJob{
		"a": job("logaggregator", "lk", host.StatusDone),
		"b": job("my-app", "tenant-set", host.StatusRunning),
	}); got != "" {
		t.Fatalf("stopped or tenant jobs must not count: %q", got)
	}
}
