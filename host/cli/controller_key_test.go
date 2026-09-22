package cli

import (
	"os"
	"testing"

	host "github.com/randy-girard/flynn/host/types"
)

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

func TestControllerKeyFromActiveJobsDelegates(t *testing.T) {
	jobs := map[string]host.ActiveJob{
		"web": {
			Status: host.StatusRunning,
			Job: &host.Job{
				Metadata: map[string]string{"flynn-controller.app_name": "controller"},
				Config:   host.ContainerConfig{Env: map[string]string{"AUTH_KEY": "job-key"}},
			},
		},
	}
	if got := controllerKeyFromActiveJobs(jobs); got != "job-key" {
		t.Fatalf("got %q", got)
	}
	if os.Getenv("CONTROLLER_KEY") != "" {
		t.Setenv("CONTROLLER_KEY", "")
	}
}
