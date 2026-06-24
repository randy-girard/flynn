package cli

import (
	"encoding/json"
	"os"
	"time"

	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/flynn/pkg/updaterdeploy"
	"github.com/inconshreveable/log15"
)

// isControllerPlacedJob reports whether a job was placed by the controller
// (directly or via the scheduler). Controller jobs carry flynn-controller.*
// metadata keys; FLYNN_APP_ID lives in Config.Env, not Metadata.
func isControllerPlacedJob(job *host.Job) bool {
	if job == nil {
		return false
	}
	if job.Metadata != nil && job.Metadata["flynn-controller.app"] != "" {
		return true
	}
	if job.Config.Env != nil && job.Config.Env["FLYNN_APP_ID"] != "" {
		return true
	}
	return false
}

// hostRestartSettleOptions controls post-restart waiting between rolling
// flynn-host daemon restarts on a multi-node cluster.
type hostRestartSettleOptions struct {
	Log               log15.Logger
	ClusterClient     *cluster.Client
	RestartedHost     *cluster.Host
	ExpectedHostCount int
	// FatalClusterSize aborts when discoverd has not repopulated the expected
	// number of hosts. This is enforced between rolling restarts; the final
	// settle after all hosts are done treats a short count as a warning only.
	FatalClusterSize bool
	InterHostDelay   bool
}

// settleAfterHostRestart waits for cluster health, sirenia leader DNS, and
// scheduler job placement to settle after a flynn-host daemon restart. Job
// containers normally survive systemctl restart (KillMode=process), but
// discoverd registration, postgres leader propagation, and controller
// scheduling still need time to catch up before restarting the next host.
func settleAfterHostRestart(opts hostRestartSettleOptions) error {
	log := opts.Log
	if log == nil {
		log = log15.New()
	}

	log.Info("waiting for cluster to be healthy after host restart", "timeout", updateHealthTimeout)
	if _, err := waitForClusterHealthy(updateHealthTimeout, log); err != nil {
		return err
	}

	if opts.ExpectedHostCount > 1 && opts.ClusterClient != nil {
		err := waitForClusterSize(opts.ClusterClient, opts.ExpectedHostCount, 3*time.Minute, log)
		if err != nil {
			if opts.FatalClusterSize {
				return err
			}
			log.Warn("cluster did not fully repopulate after restart, continuing anyway", "err", err)
		}
	}

	updaterdeploy.WaitSireniaApplianceLeadersStable(log)
	repairSireniaClusters(log)

	if opts.RestartedHost != nil {
		waitForJobsPlacedOnHost(opts.RestartedHost, updateWaitJobsTimeout, log)
	}

	if opts.InterHostDelay && updateInterHostDelay > 0 {
		log.Info("inter-host settle delay before next restart", "delay", updateInterHostDelay)
		time.Sleep(updateInterHostDelay)
	}
	return nil
}

// expectedClusterHostCount returns the configured cluster size when
// cluster-monitor metadata is available, otherwise the current discoverd
// host count.
func expectedClusterHostCount(log log15.Logger) int {
	if monitorMeta, err := discoverd.NewService("cluster-monitor").GetMeta(); err == nil {
		var meta struct {
			Hosts int `json:"hosts"`
		}
		if err := json.Unmarshal(monitorMeta.Data, &meta); err == nil && meta.Hosts > 0 {
			log.Debug("using cluster-monitor host count", "expected_hosts", meta.Hosts)
			return meta.Hosts
		}
	}
	n, err := clusterHostCount()
	if err != nil {
		return 0
	}
	return n
}

func localClusterHost(log log15.Logger) *cluster.Host {
	clusterClient := cluster.NewClient()
	hosts, err := clusterClient.Hosts()
	if err != nil || len(hosts) == 0 {
		return nil
	}
	localHostname, _ := os.Hostname()
	localIPs := getLocalIPs()
	daemonID, _ := getDaemonID(localIPs, log)
	return findLocalHost(hosts, localHostname, daemonID, localIPs, log)
}
