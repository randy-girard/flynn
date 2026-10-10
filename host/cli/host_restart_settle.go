package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/inconshreveable/log15"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/status"
	"github.com/randy-girard/flynn/pkg/updaterdeploy"
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
	// AllowDatastoreUnhealthy continues when status-web reports only sirenia
	// datastores unhealthy. Used at the start of flynn-host update so a
	// recycled-WAL postgres replica cannot block image pull and the deploy
	// that reseeds it.
	AllowDatastoreUnhealthy bool
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
	var allow func([]string) bool
	if opts.AllowDatastoreUnhealthy {
		allow = updateStartAllowsUnhealthy
	}
	statuses, err := waitForClusterHealthyExcept(updateHealthTimeout, log, allow)
	if err != nil {
		return err
	}

	if clusterIsFullyHealthy(statuses) {
		updaterdeploy.WaitSireniaApplianceLeadersStable(log)
	} else {
		log.Warn("cluster datastores are unhealthy; continuing so the update can deploy replacements",
			"unhealthy", unhealthyServiceNames(statuses))
	}

	if opts.ExpectedHostCount > 1 && opts.ClusterClient != nil {
		err := waitForClusterSize(opts.ClusterClient, opts.ExpectedHostCount, updateClusterSizeTimeout, log)
		if err != nil && opts.FatalClusterSize {
			// One more health pass before aborting — discoverd can lag
			// status-web briefly after a host rejoins raft.
			_, healthErr := waitForClusterHealthyExcept(updateHealthTimeout, log, allow)
			var retryErr error
			if healthErr == nil {
				retryErr = waitForClusterSize(opts.ClusterClient, opts.ExpectedHostCount, updateClusterSizeTimeout/4, log)
			}
			err = settleClusterSizeError(err, true, healthErr, retryErr)
		}
		if err != nil {
			if opts.FatalClusterSize {
				return err
			}
			log.Warn("cluster did not fully repopulate after restart, continuing anyway", "err", err)
		}
	}

	// Deposed clearing runs once after the full rolling restart / image
	// rollout, not between every host restart.

	if opts.RestartedHost != nil {
		waitForJobsPlacedOnHost(opts.RestartedHost, updateWaitJobsTimeout, log)
	}

	if opts.InterHostDelay && updateInterHostDelay > 0 {
		log.Info("inter-host settle delay before next restart", "delay", updateInterHostDelay)
		time.Sleep(updateInterHostDelay)
	}
	return nil
}

// settleClusterSizeError decides whether a failed first cluster-size wait
// should abort a rolling restart. When fatal, a successful health probe plus
// a successful size retry clears the error so a brief discoverd lag does not
// leave mixed binaries. Non-fatal waits always continue.
func settleClusterSizeError(firstErr error, fatal bool, healthErr, retryErr error) error {
	if firstErr == nil {
		return nil
	}
	if !fatal {
		return nil
	}
	if healthErr == nil && retryErr == nil {
		return nil
	}
	if healthErr == nil {
		return retryErr
	}
	return firstErr
}

// expectedClusterHostCount returns the configured cluster size when
// cluster-monitor metadata is available, otherwise the current discoverd
// host count. If a host was permanently drained, monitor metadata can still
// list the bootstrap size; prefer the live count so flynn-host update does
// not wait forever for the missing peer.
func expectedClusterHostCount(log log15.Logger) int {
	monitorHosts := 0
	if monitorMeta, err := discoverd.NewService("cluster-monitor").GetMeta(); err == nil {
		var meta struct {
			Hosts int `json:"hosts"`
		}
		if err := json.Unmarshal(monitorMeta.Data, &meta); err == nil && meta.Hosts > 0 {
			monitorHosts = meta.Hosts
			log.Debug("using cluster-monitor host count", "expected_hosts", meta.Hosts)
		}
	}
	liveHosts, err := clusterHostCount()
	if err != nil {
		liveHosts = 0
	}
	return pickExpectedHostCount(monitorHosts, liveHosts)
}

func pickExpectedHostCount(monitorHosts, liveHosts int) int {
	if liveHosts > 0 && monitorHosts > liveHosts {
		return liveHosts
	}
	if monitorHosts > 0 {
		return monitorHosts
	}
	return liveHosts
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

// settleLocalRestartBeforeRemotes runs the shared post-restart settle gates
// before rolling binaries onto remote hosts. Used by both GitHub and tarball
// --all-nodes paths so local restart recoverability is identical.
func settleLocalRestartBeforeRemotes(noRestart bool, log log15.Logger) error {
	if noRestart {
		return nil
	}
	clusterClient := cluster.NewClient()
	expectedHosts := expectedClusterHostCount(log)
	if err := settleAfterHostRestart(hostRestartSettleOptions{
		Log:                     log,
		ClusterClient:           clusterClient,
		RestartedHost:           localClusterHost(log),
		ExpectedHostCount:       expectedHosts,
		FatalClusterSize:        expectedHosts > 1,
		AllowDatastoreUnhealthy: true,
	}); err != nil {
		return fmt.Errorf("cluster did not recover after local restart: %w", err)
	}
	return nil
}

// updateStartAllowsUnhealthy is true when every unhealthy status-web service
// is a sirenia datastore the rest of flynn-host update will redeploy. A
// recycled-WAL postgres replica must not abort image pull.
func updateStartAllowsUnhealthy(unhealthy []string) bool {
	if len(unhealthy) == 0 {
		// HTTP 500 with an empty detail map is not "only postgres".
		return false
	}
	for _, name := range unhealthy {
		if !isUpdateRepairableDatastore(name) {
			return false
		}
	}
	return true
}

func isUpdateRepairableDatastore(name string) bool {
	switch name {
	case "postgres", "mariadb", "mongodb", "redis":
		return true
	default:
		return false
	}
}

func unhealthyServiceNames(statuses map[string]status.Status) []string {
	var names []string
	for name, svc := range statuses {
		if svc.Status != status.CodeHealthy {
			names = append(names, name)
		}
	}
	return names
}

func clusterIsFullyHealthy(statuses map[string]status.Status) bool {
	if len(statuses) == 0 {
		return false
	}
	return len(unhealthyServiceNames(statuses)) == 0
}

// allowRemoteRestartDatastoreUnhealth reports whether settleAfterHostRestart
// during updateRemoteBinaries may skip postgres. Only a snapshot of
// pre-existing datastore unhealth may skip; a healthy cluster still waits
// for HTTP 200 between hosts so HA does not collapse.
func allowRemoteRestartDatastoreUnhealth(preexisting bool) bool {
	return preexisting
}

// clusterHasRepairableDatastoreUnhealth is a one-shot status-web probe used
// before rolling remote flynn-host restarts. True means postgres (or another
// sirenia datastore) is already down, so settleAfterHostRestart must not wait
// 10 minutes for it before image pull.
func clusterHasRepairableDatastoreUnhealth(log log15.Logger) bool {
	if log == nil {
		log = log15.New()
	}
	statuses, err := waitForClusterHealthyExcept(time.Millisecond, log, updateStartAllowsUnhealthy)
	if err != nil {
		return false
	}
	return !clusterIsFullyHealthy(statuses)
}
