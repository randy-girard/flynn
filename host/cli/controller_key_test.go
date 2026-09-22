package cli

import (
	"os"
	"testing"
	"time"

	host "github.com/randy-girard/flynn/host/types"
)

func TestControllerKeyFromActiveJobs(t *testing.T) {
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
	if got := controllerKeyFromActiveJobs(jobs); got != "new-key" {
		t.Fatalf("got %q, want newest running controller job key", got)
	}
}

func TestControllerAPIKeyPrefersEnvOverJobs(t *testing.T) {
	t.Setenv("CONTROLLER_KEY", "")
	t.Setenv("AUTH_KEY", "from-env")
	if got := controllerAPIKey(map[string]string{"AUTH_KEY": "from-meta"}); got != "from-env" {
		t.Fatalf("got %q", got)
	}
}

func TestControllerAPIKeyUsesDiscoverdMetaWhenEnvEmpty(t *testing.T) {
	t.Setenv("CONTROLLER_KEY", "")
	t.Setenv("AUTH_KEY", "")
	if got := controllerAPIKey(map[string]string{"AUTH_KEY": "from-meta"}); got != "from-meta" {
		t.Fatalf("got %q, want discoverd meta during mixed-version rollout", got)
	}
}

func TestControllerKeyFromJobEnvPrefersControllerKey(t *testing.T) {
	if os.Getenv("CONTROLLER_KEY") != "" {
		t.Setenv("CONTROLLER_KEY", "")
	}
	got := controllerKeyFromJobEnv(map[string]string{"AUTH_KEY": "auth", "CONTROLLER_KEY": "ctrl"})
	if got != "ctrl" {
		t.Fatalf("got %q", got)
	}
}
