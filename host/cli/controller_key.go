package cli

import (
	"fmt"
	"os"

	controller "github.com/randy-girard/flynn/controller/client"
	hostconfig "github.com/randy-girard/flynn/host/config"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/controllerkey"
)

// controllerAPIKey is the cluster controller secret for flynn-host CLI.
// Discoverd no longer publishes AUTH_KEY on controller instances (SEC-028).
// Order: process env / host.json (already copied into env), then discoverd
// meta for mixed-version rollouts, then AUTH_KEY on a running controller job.
func controllerAPIKey(meta map[string]string) string {
	return controllerAPIKeyFromHosts(meta, nil)
}

func controllerAPIKeyFromHosts(meta map[string]string, hosts []*cluster.Host) string {
	if k := controller.KeyFromEnvOrMeta(meta); k != "" {
		return k
	}
	k := controllerkey.FromHosts(hosts)
	if k == "" {
		k = lookupControllerKeyFromCluster()
	}
	if k == "" {
		return ""
	}
	seedControllerKey(k)
	return k
}

func lookupControllerKeyFromCluster() string {
	hosts, err := cluster.NewClient().Hosts()
	if err != nil {
		return ""
	}
	return controllerkey.FromHosts(hosts)
}

func controllerKeyFromActiveJobs(jobs map[string]host.ActiveJob) string {
	return controllerkey.FromActiveJobs(jobs)
}

func seedControllerKey(k string) {
	if k == "" {
		return
	}
	if os.Getenv("AUTH_KEY") == "" {
		os.Setenv("AUTH_KEY", k)
	}
	if os.Getenv("CONTROLLER_KEY") == "" {
		os.Setenv("CONTROLLER_KEY", k)
	}
	// Persist so later flynn-host CLI invocations (and the next update)
	// authenticate without listing jobs again.
	_ = hostconfig.SetEnv(hostconfig.DefaultPath, map[string]string{
		"AUTH_KEY":       k,
		"CONTROLLER_KEY": k,
	})
}

func missingControllerKeyErr() error {
	return fmt.Errorf("controller AUTH_KEY is unavailable (discoverd no longer publishes it; set AUTH_KEY or CONTROLLER_KEY, or keep a running controller job)")
}
