package controllerkey

import (
	"testing"
	"time"

	host "github.com/randy-girard/flynn/host/types"
)

func TestFromActiveJobsUsesNewestRunningController(t *testing.T) {
	jobs := map[string]host.ActiveJob{
		"old": {
			Status:    host.StatusRunning,
			CreatedAt: time.Unix(100, 0),
			Job: &host.Job{
				Metadata: map[string]string{"flynn-controller.app_name": "controller", "flynn-controller.type": "web"},
				Config:   host.ContainerConfig{Env: map[string]string{"AUTH_KEY": "old-key"}},
			},
		},
		"new": {
			Status:    host.StatusRunning,
			CreatedAt: time.Unix(200, 0),
			Job: &host.Job{
				Metadata: map[string]string{"flynn-controller.app_name": "controller", "flynn-controller.type": "web"},
				Config:   host.ContainerConfig{Env: map[string]string{"CONTROLLER_KEY": "new-key"}},
			},
		},
		"done": {
			Status: host.StatusDone,
			Job: &host.Job{
				Metadata: map[string]string{"flynn-controller.app_name": "controller"},
				Config:   host.ContainerConfig{Env: map[string]string{"AUTH_KEY": "stale"}},
			},
		},
		"other": {
			Status: host.StatusRunning,
			Job: &host.Job{
				Metadata: map[string]string{"flynn-controller.app_name": "postgres"},
				Config:   host.ContainerConfig{Env: map[string]string{"AUTH_KEY": "pg"}},
			},
		},
	}
	if got := FromActiveJobs(jobs); got != "new-key" {
		t.Fatalf("got %q, want newest running controller job key", got)
	}
}

func TestFromJobEnvPrefersControllerKey(t *testing.T) {
	got := FromJobEnv(map[string]string{"AUTH_KEY": "auth", "CONTROLLER_KEY": "ctrl"})
	if got != "ctrl" {
		t.Fatalf("got %q", got)
	}
}
