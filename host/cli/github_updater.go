package cli

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/host/downloader"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/flynn/pkg/dialer"
	"github.com/flynn/flynn/pkg/ghrelease"
	"github.com/flynn/flynn/pkg/installsource"
	sirenia "github.com/flynn/flynn/pkg/sirenia/state"
	"github.com/flynn/flynn/pkg/status"
	"github.com/flynn/flynn/pkg/updaterdeploy"
	"github.com/flynn/flynn/pkg/version"
	updater "github.com/flynn/flynn/updater/types"
	"github.com/flynn/go-docopt"
	"github.com/inconshreveable/log15"
)

// Rolling-restart resilience knobs. Tunable via flags on `flynn-host update`
// (see applyUpdateTimingFlags). Defaults reflect what is safe for a typical
// 3-node cluster running postgres+mariadb+mongodb sirenia appliances:
//
//   - updateHealthTimeout bounds the per-host wait inside the rolling restart
//     loop in updateRemoteBinaries. 10 minutes accommodates clusters where
//     sirenia replication or postgres leader propagation through discoverd
//     can take several minutes after a daemon restart; the previous 5-minute
//     ceiling was too tight and caused rolling updates to abort with
//     "cluster did not recover after restarting <host>" while the cluster
//     was actually still settling.
//
//   - updateInterHostDelay is an additional fixed settle delay applied AFTER
//     waitForClusterHealthy/waitForJobsPlacedOnHost succeed for one host, and
//     BEFORE the next host's restart begins. The cluster status endpoint can
//     return healthy a few seconds before the scheduler has fully processed
//     the host-up event and started pushing AddJob requests; restarting the
//     next host inside that window can collapse postgres quorum.
//
//   - updateWaitJobsTimeout caps the wait for the scheduler to actually
//     re-place an app job onto the freshly restarted host. This catches the
//     failure mode where the host is healthy and discoverable but the
//     scheduler hasn't observed it yet, so no jobs are scheduled back. The
//     wait is non-fatal: on timeout we log a warning and continue.
var (
	updateHealthTimeout   = 10 * time.Minute
	updateInterHostDelay  = 30 * time.Second
	updateWaitJobsTimeout = 3 * time.Minute
)

// clusterHostCount returns how many flynn-host peers are registered. If
// discoverd cannot be queried, it returns a non-nil error.
func clusterHostCount() (int, error) {
	hosts, err := cluster.NewClient().Hosts()
	if err != nil {
		return 0, err
	}
	return len(hosts), nil
}

// runGitHubUpdate performs an update using GitHub Releases
func runGitHubUpdate(args *docopt.Args, repo, configDir string, log log15.Logger) error {
	client := ghrelease.NewClient(repo, log)
	binDir := args.String["--bin-dir"]
	targetVersion := args.String["--version"]
	checkOnly := args.Bool["--check"]
	force := args.Bool["--force"]
	skipImages := args.Bool["--skip-images"]
	imagesOnly := args.Bool["--images-only"]
	allNodes := args.Bool["--all-nodes"]

	if imagesOnly && !allNodes {
		n, err := clusterHostCount()
		if err != nil {
			return fmt.Errorf("--all-nodes is required with --images-only when cluster hosts cannot be discovered: %w", err)
		}
		if n > 1 {
			return fmt.Errorf("--images-only requires --all-nodes when the cluster has more than one host")
		}
	}

	currentVersion := version.String()
	log.Info("checking for updates", "repo", repo, "current_version", currentVersion)

	// Get release (latest or specific version)
	var release *ghrelease.Release
	var err error
	if targetVersion != "" {
		log.Info("fetching specific version", "version", targetVersion)
		release, err = client.GetReleaseByTag(targetVersion)
	} else {
		release, err = client.GetLatestRelease()
	}
	if err != nil {
		log.Error("failed to get release info", "err", err)
		return err
	}

	log.Info("found release", "version", release.TagName, "published", release.PublishedAt)

	// Check if update is needed
	if !force && !ghrelease.CompareVersions(currentVersion, release.TagName) {
		log.Info("already on latest version", "version", currentVersion)
		if checkOnly {
			fmt.Printf("Already on latest version: %s\n", currentVersion)
		}
		return nil
	}

	if checkOnly {
		fmt.Printf("Update available: %s -> %s\n", currentVersion, release.TagName)
		return nil
	}

	log.Info("updating to version", "version", release.TagName)

	// Image rollout touches the whole cluster; require --all-nodes for multi-host,
	// but a single registered host is always "all" peers.
	rolloutCluster := allNodes
	if !rolloutCluster && !skipImages {
		if n, err := clusterHostCount(); err == nil && n <= 1 {
			rolloutCluster = true
			log.Info("single-node cluster: rolling out images without --all-nodes")
		}
	}

	// expectedHostCount is captured during the rolling binary update and
	// passed to updateImages so the image-pull step can wait for the
	// cluster to repopulate discoverd before fanning out, rather than
	// silently targeting whichever subset of hosts has rejoined raft.
	var expectedHostCount int

	// Update binaries unless --images-only was specified
	if !imagesOnly {
		// Create temp directory for downloads
		tmpDir, err := os.MkdirTemp("", "flynn-update-*")
		if err != nil {
			return fmt.Errorf("failed to create temp directory: %w", err)
		}
		defer os.RemoveAll(tmpDir)

		// Download checksums first
		checksumURL := ghrelease.GetReleaseURL(repo, release.TagName) + "/checksums.sha512"
		checksumPath := filepath.Join(tmpDir, "checksums.sha512")
		if err := client.DownloadFile(checksumURL, checksumPath); err != nil {
			log.Error("failed to download checksums", "err", err)
			return err
		}

		checksums, err := parseChecksums(checksumPath)
		if err != nil {
			log.Error("failed to parse checksums", "err", err)
			return err
		}

		// Download and install binaries
		binaries := []struct {
			name     string
			destName string
		}{
			{"flynn-host-linux-amd64.gz", "flynn-host"},
			{"flynn-init-linux-amd64.gz", "flynn-init"},
		}

		for _, bin := range binaries {
			if err := downloadAndInstallBinary(client, repo, release.TagName, bin.name, bin.destName, tmpDir, binDir, checksums, log); err != nil {
				return err
			}
		}

		// Update install-source.json
		source := installsource.NewGitHubSource(repo, release.TagName)
		if err := installsource.Save(configDir, source); err != nil {
			log.Warn("failed to update install-source.json", "err", err)
			// Don't fail the update for this
		}

		log.Info("binaries downloaded", "version", release.TagName)
		fmt.Printf("Flynn binaries updated to %s\n", release.TagName)

		// Trigger zero-downtime daemon restart unless --no-restart was specified
		if !args.Bool["--no-restart"] {
			restarted, err := restartDaemon(binDir, log)
			if err != nil {
				return err
			}
			if restarted {
				fmt.Printf("Flynn daemon restarted with version %s\n", release.TagName)
			}
		} else {
			log.Info("skipping daemon restart (--no-restart specified)")
			fmt.Println("Daemon restart skipped. Restart manually to activate the new version.")
		}

		if allNodes {
			// Wait for the cluster to be healthy before starting the
			// remote rolling restart — restarting the next host while
			// the local daemon's sirenia peer is still recovering can
			// collapse postgres quorum (see waitForClusterHealthy).
			if !args.Bool["--no-restart"] {
				log.Info("waiting for cluster to be healthy before remote restarts", "timeout", updateHealthTimeout)
				if _, err := waitForClusterHealthy(updateHealthTimeout, log); err != nil {
					return fmt.Errorf("cluster did not recover after local restart: %w", err)
				}
			}

			n, err := updateRemoteBinaries(repo, binDir, configDir, release.TagName, "", args.Bool["--no-restart"], log)
			if err != nil {
				return err
			}
			expectedHostCount = n
		} else {
			log.Info("skipping remote host binary updates (--all-nodes not set)")
			fmt.Println("Other cluster hosts were not updated. Run flynn-host update on each node with the same version, then run flynn-host update --all-nodes to pull images everywhere and deploy system apps—or pass --all-nodes on this command to update every host now.")
		}
	}

	// Update container images and system apps unless --skip-images was specified
	if !skipImages {
		if !rolloutCluster {
			log.Info("skipping container images and system app rollout (local-only update)")
			fmt.Println("Skipping container images and system apps on this run. After flynn-host matches on every node, run: flynn-host update --all-nodes")
		} else if err := updateImages(repo, configDir, release.TagName, "", force, expectedHostCount, log); err != nil {
			return err
		}
	}

	log.Info("update complete", "version", release.TagName)
	return nil
}

// restartDaemon restarts the local flynn-host daemon using systemctl.
// This ensures systemd properly tracks the new daemon process.
// restartDaemon returns true if the daemon was actually restarted, false if
// it was skipped (e.g. daemon not running locally).
func restartDaemon(binDir string, log log15.Logger) (bool, error) {
	log.Info("restarting local daemon via systemctl")

	// Check if the daemon is running before attempting restart
	statusCmd := exec.Command("systemctl", "is-active", "--quiet", "flynn-host")
	if err := statusCmd.Run(); err != nil {
		log.Warn("local flynn-host daemon is not active, skipping restart")
		fmt.Println("Local flynn-host daemon is not active. Start it with: systemctl start flynn-host")
		return false, nil
	}

	fmt.Println("Restarting local flynn-host daemon via systemctl...")
	cmd := exec.Command("systemctl", "restart", "flynn-host")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Error("systemctl restart failed", "err", err)
		return false, fmt.Errorf("failed to restart daemon via systemctl: %s", err)
	}

	// Wait for the daemon to be responsive after restart
	log.Info("waiting for daemon to become responsive after restart")
	localIPs := getLocalIPs()
	for i := 0; i < 15; i++ {
		time.Sleep(2 * time.Second)
		if id, _ := getDaemonID(localIPs, log); id != "" {
			log.Info("daemon is responsive after restart", "daemon_id", id)
			return true, nil
		}
	}

	log.Warn("daemon may still be starting up after systemctl restart")
	return true, nil
}

// updateRemoteBinaries pushes binary and config updates to all other cluster
// nodes and optionally restarts their daemons. Updates are performed one host
// at a time (rolling) to maintain cluster availability.
// For GitHub updates, repo should be set and baseURL empty.
// For tarball updates, baseURL should point to the temp HTTP server.
//
// It returns the expected cluster host count observed before the rolling
// restart so the caller can gate later steps (e.g. image pulls) on the
// cluster repopulating discoverd, rather than racing a partially-rejoined
// raft state.
func updateRemoteBinaries(repo, binDir, configDir, version, baseURL string, noRestart bool, log log15.Logger) (int, error) {
	// Retry discoverd lookup — after a systemctl restart the local daemon
	// may not have re-registered with discoverd yet.
	clusterClient := cluster.NewClient()
	var hosts []*cluster.Host
	var err error

	// Try to get the expected cluster size from the cluster-monitor metadata
	// so we can wait for all nodes to be visible before proceeding.
	var expectedClusterSize int
	if monitorMeta, err := discoverd.NewService("cluster-monitor").GetMeta(); err == nil {
		var meta struct {
			Enabled bool `json:"enabled"`
			Hosts   int  `json:"hosts"`
		}
		if err := json.Unmarshal(monitorMeta.Data, &meta); err == nil && meta.Hosts > 0 {
			expectedClusterSize = meta.Hosts
			log.Info("determined expected cluster size from cluster-monitor metadata", "expected_hosts", expectedClusterSize)
		}
	}

	for i := 0; i < 10; i++ {
		if i > 0 {
			time.Sleep(3 * time.Second)
		}
		hosts, err = clusterClient.Hosts()
		if err == nil && len(hosts) > 0 {
			// If we know the expected cluster size, only break if we have all hosts.
			// Otherwise, break as soon as we have any hosts.
			if expectedClusterSize == 0 || len(hosts) >= expectedClusterSize {
				break
			} else {
				log.Debug("waiting for all hosts to be visible in discoverd", "attempt", i+1, "found", len(hosts), "expected", expectedClusterSize)
				err = fmt.Errorf("only found %d of %d expected hosts", len(hosts), expectedClusterSize)
			}
		} else if err != nil {
			log.Debug("discoverd not ready for remote binary update, retrying", "attempt", i+1, "err", err)
		} else {
			log.Debug("no hosts found via discoverd yet, retrying", "attempt", i+1)
		}
	}
	if err != nil {
		log.Warn("could not discover cluster hosts for remote binary update", "err", err)
		fmt.Println("Could not discover cluster hosts. Remote nodes were NOT updated.")
		return 0, nil // non-fatal: local update succeeded
	}
	expectedHostCount := len(hosts)
	if len(hosts) <= 1 {
		log.Info("single-node cluster, no remote hosts to update")
		return expectedHostCount, nil
	}

	// Determine local host to skip
	localHostname, _ := os.Hostname()
	localIPs := getLocalIPs()
	daemonID, _ := getDaemonID(localIPs, log)
	localHost := findLocalHost(hosts, localHostname, daemonID, localIPs, log)

	var localHostID string
	if localHost != nil {
		localHostID = localHost.ID()
	} else if daemonID != "" {
		// findLocalHost didn't match — discoverd may not yet show
		// the local daemon after a recent restart. Skip by daemon ID
		// directly to avoid pushing binaries back to ourselves.
		localHostID = daemonID
		log.Info("local host not yet in discoverd, skipping by daemon ID", "daemon_id", daemonID)
	}

	log.Info("updating remote hosts", "total_hosts", len(hosts), "local_host", localHostID)

	for _, h := range hosts {
		if h.ID() == localHostID {
			continue
		}

		hostLog := log.New("remote_host", h.ID())
		hostLog.Info("pulling binaries on remote host")
		fmt.Printf("Updating binaries on %s...\n", h.ID())

		_, err := h.PullBinariesAndConfig(repo, binDir, configDir, version, baseURL, nil)
		if err != nil {
			hostLog.Error("failed to pull binaries on remote host", "err", err)
			return expectedHostCount, fmt.Errorf("failed to update binaries on host %s: %w", h.ID(), err)
		}
		hostLog.Info("binaries updated on remote host")
		fmt.Printf("Binaries updated on %s\n", h.ID())

		if !noRestart {
			hostLog.Info("restarting daemon on remote host via systemctl")
			fmt.Printf("Restarting flynn-host daemon on %s via systemctl...\n", h.ID())

			if err := h.SystemctlRestart(); err != nil {
				hostLog.Error("error requesting systemctl restart on remote host", "err", err)
				return expectedHostCount, fmt.Errorf("failed to restart daemon on host %s: %w", h.ID(), err)
			}

			hostLog.Info("systemctl restart requested on remote host")
			fmt.Printf("Flynn daemon restart initiated on %s\n", h.ID())

			// Wait for the remote daemon to actually come back up
			// and re-register with discoverd before moving to the
			// next host. A blind sleep here is unsafe: a restarted
			// daemon can take ~1–3 minutes to find its peers and
			// rejoin the raft cluster, and starting the next
			// restart inside that window collapses quorum and
			// leaves the cluster unable to schedule jobs.
			//
			// The remote systemctl-restart endpoint sleeps ~2s
			// before exec'ing systemctl, so wait briefly for the
			// old process to actually die before polling.
			time.Sleep(5 * time.Second)
			if err := waitForRemoteDaemon(h, 3*time.Minute, hostLog); err != nil {
				return expectedHostCount, fmt.Errorf("daemon on host %s did not become responsive after restart: %w", h.ID(), err)
			}
			if err := waitForClusterSize(clusterClient, expectedHostCount, 3*time.Minute, hostLog); err != nil {
				hostLog.Warn("cluster did not fully repopulate after restart, continuing anyway", "err", err)
			}

			// Wait for the cluster (and especially the sirenia-managed
			// databases) to be fully healthy again before restarting
			// the next host. Without this, a fast rolling restart can
			// kill the postgres primary while the new standbys are
			// still catching up via pg_basebackup, collapsing quorum
			// and leaving the controller unable to schedule jobs.
			hostLog.Info("waiting for cluster to be healthy before next restart", "timeout", updateHealthTimeout)
			if _, err := waitForClusterHealthy(updateHealthTimeout, hostLog); err != nil {
				return expectedHostCount, fmt.Errorf("cluster did not recover after restarting %s: %w", h.ID(), err)
			}

			// Status-web reporting healthy does not guarantee:
			//  (a) the postgres discoverd leader is reachable from
			//      every controller pod (DNS propagation is async), or
			//  (b) any sirenia peer that was deposed during the
			//      restart will rejoin without manual clearing, or
			//  (c) the controller-scheduler has observed the restarted
			//      host coming back up and started placing jobs on it.
			// Each of the next three helpers addresses one of those
			// gaps. They are individually no-ops when their target
			// state is already settled, so the total added latency in
			// the common case is only updateInterHostDelay.
			updaterdeploy.WaitSireniaApplianceLeadersStable(hostLog)
			repairSireniaClusters(hostLog)
			waitForJobsPlacedOnHost(h, updateWaitJobsTimeout, hostLog)

			if updateInterHostDelay > 0 {
				hostLog.Info("inter-host settle delay before next restart", "delay", updateInterHostDelay)
				time.Sleep(updateInterHostDelay)
			}
		}
	}

	log.Info("all remote hosts updated")
	return expectedHostCount, nil
}

// waitForRemoteDaemon polls a remote flynn-host daemon's status endpoint
// until it responds successfully or the timeout elapses. This is the
// remote analogue of restartDaemon's local responsiveness check and
// replaces the previous blind sleep in the rolling-restart loop.
func waitForRemoteDaemon(h *cluster.Host, timeout time.Duration, log log15.Logger) error {
	log.Info("waiting for remote daemon to become responsive after restart")
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, err := h.GetStatus(); err == nil {
			log.Info("remote daemon is responsive after restart")
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(2 * time.Second)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out after %s", timeout)
	}
	return lastErr
}

// waitForClusterSize polls discoverd until at least the expected number
// of hosts are registered, or the timeout elapses. Used between rolling
// restarts and before fan-out operations (e.g. image pulls) to ensure we
// don't silently target a subset of the cluster while restarted daemons
// are still rejoining.
func waitForClusterSize(client *cluster.Client, expected int, timeout time.Duration, log log15.Logger) error {
	if expected <= 1 {
		return nil
	}
	log.Info("waiting for cluster to repopulate in discoverd", "expected_hosts", expected)
	deadline := time.Now().Add(timeout)
	var lastCount int
	for time.Now().Before(deadline) {
		hosts, err := client.Hosts()
		if err == nil {
			lastCount = len(hosts)
			if lastCount >= expected {
				log.Info("cluster repopulated in discoverd", "hosts", lastCount)
				return nil
			}
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("only %d/%d hosts registered after %s", lastCount, expected, timeout)
}

// waitForJobsPlacedOnHost polls the freshly-restarted host's job list until
// the scheduler has placed at least one app job onto it (i.e. a job with a
// FLYNN_APP_ID set in its metadata). This catches the failure mode where
// status-web reports the cluster healthy and the host is up in discoverd,
// but the controller-scheduler has not yet observed the host coming back up
// (we have seen ~4 minute gaps in production), so no jobs are scheduled
// back onto it before the next host's restart begins.
//
// The wait is intentionally non-fatal: the scheduler may legitimately have
// nothing to place on a particular host (e.g. a single-replica formation
// pinned elsewhere via tags), and we'd rather emit a warning than abort an
// otherwise-successful rolling update. The inter-host settle delay that
// follows still gives the scheduler a final chance to catch up.
//
// "Bootstrap" jobs started directly by the host daemon (flannel, discoverd)
// don't carry the controller's FLYNN_APP_ID metadata key, so they don't
// satisfy this check by themselves.
func waitForJobsPlacedOnHost(h *cluster.Host, timeout time.Duration, log log15.Logger) {
	if timeout <= 0 {
		return
	}
	log.Info("waiting for scheduler to place jobs back on restarted host", "timeout", timeout)
	deadline := time.Now().Add(timeout)
	var lastJobCount, lastAppJobCount int
	var lastErr error
	for time.Now().Before(deadline) {
		jobs, err := h.ListActiveJobs()
		if err == nil {
			lastErr = nil
			lastJobCount = len(jobs)
			lastAppJobCount = 0
			for _, job := range jobs {
				if job.Job == nil || job.Job.Metadata == nil {
					continue
				}
				// FLYNN_APP_ID is set by the controller for every
				// app/system-app job it places; bootstrap jobs
				// (flannel, discoverd) started by the host
				// daemon's resurrection logic don't have it.
				if job.Job.Metadata["FLYNN_APP_ID"] != "" {
					lastAppJobCount++
				}
			}
			if lastAppJobCount > 0 {
				log.Info("scheduler placed jobs back on host",
					"app_jobs", lastAppJobCount, "total_jobs", lastJobCount)
				return
			}
			log.Debug("host has no app jobs yet, waiting",
				"total_jobs", lastJobCount)
		} else {
			lastErr = err
			log.Debug("error listing jobs on host, retrying", "err", err)
		}
		time.Sleep(5 * time.Second)
	}
	if lastErr != nil {
		log.Warn("scheduler did not place jobs on restarted host within timeout (last ListActiveJobs failed)",
			"timeout", timeout, "err", lastErr)
		return
	}
	log.Warn("scheduler did not place any app jobs on restarted host within timeout; continuing anyway",
		"timeout", timeout, "total_jobs", lastJobCount)
}

// waitForClusterHealthy polls the status-web endpoint until it reports
// the whole cluster as healthy (HTTP 200), or the timeout elapses.
//
// This is critical between rolling daemon restarts: a restart kills every
// job on a host, including its sirenia-managed database peer (postgres,
// mariadb, mongodb). Each peer takes a few seconds to rejoin and catch up
// to the cluster's primary; if we restart the next host before the
// previous peer has fully recovered, we collapse quorum and leave the
// database without a writable primary. That deadlocks the controller
// (which needs to write to postgres to schedule any new jobs), which then
// can't reschedule a replacement peer on the restarted host either.
//
// On success returns the latest service status map. On timeout returns
// an error describing which services are still unhealthy.
func waitForClusterHealthy(timeout time.Duration, log log15.Logger) (map[string]status.Status, error) {
	const retryDelay = 5 * time.Second
	deadline := time.Now().Add(timeout)
	var statuses map[string]status.Status
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		if attempt > 1 {
			time.Sleep(retryDelay)
		}

		// Re-discover status-web on each attempt — instances may
		// change after daemon restarts as containers get new overlay
		// IPs from flannel.
		statusInstances, err := discoverd.GetInstances("status-web", 5*time.Second)
		if err != nil || len(statusInstances) == 0 {
			if err != nil {
				log.Debug("status-web not discoverable yet", "attempt", attempt, "err", err)
			} else {
				log.Debug("no status-web instances yet", "attempt", attempt)
			}
			continue
		}

		statusAddr := statusInstances[0].Addr
		log.Debug("checking cluster status", "addr", statusAddr, "attempt", attempt)
		req, err := http.NewRequest("GET", "http://"+statusAddr, nil)
		if err != nil {
			log.Debug("error creating status request", "attempt", attempt, "err", err)
			continue
		}
		req.Header.Set("Accept", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Debug("error reaching status endpoint", "attempt", attempt, "addr", statusAddr, "err", err)
			continue
		}

		var statusWrapper struct {
			Data struct {
				Status status.Code              `json:"status"`
				Detail map[string]status.Status `json:"detail"`
			}
		}
		decodeErr := decodeJSON(res.Body, &statusWrapper)
		res.Body.Close()
		if decodeErr != nil {
			log.Debug("error decoding status response", "attempt", attempt, "err", decodeErr)
			continue
		}

		statuses = statusWrapper.Data.Detail
		if res.StatusCode == 200 {
			log.Info("cluster is healthy", "attempts", attempt)
			return statuses, nil
		}

		var unhealthyServices []string
		for name, svc := range statuses {
			if svc.Status != status.CodeHealthy {
				unhealthyServices = append(unhealthyServices, name)
			}
		}
		log.Debug("cluster not yet healthy", "attempt", attempt, "code", res.StatusCode, "unhealthy", unhealthyServices)
	}

	var unhealthyServices []string
	for name, svc := range statuses {
		if svc.Status != status.CodeHealthy {
			unhealthyServices = append(unhealthyServices, name)
		}
	}
	return statuses, fmt.Errorf("cluster did not become healthy within %s (unhealthy services: %v)", timeout, unhealthyServices)
}

// hostIDs returns a slice of host IDs for logging
func hostIDs(hosts []*cluster.Host) []string {
	ids := make([]string, len(hosts))
	for i, h := range hosts {
		ids[i] = h.ID()
	}
	return ids
}

// normalizeHostname removes dashes, underscores, and converts to lowercase
// to allow flexible matching between hostnames and host IDs.
// e.g., "flynn-test-node-3" -> "flynntestnode3"
func normalizeHostname(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "-", "")
	name = strings.ReplaceAll(name, "_", "")
	return name
}

// findLocalHost identifies the local host in a list of cluster hosts using
// multiple matching strategies in priority order:
//  1. Daemon ID match (if daemonID is non-empty)
//  2. IP address match (if localIPs is non-empty)
//  3. Hostname match (exact, case-insensitive, then normalized)
//  4. Single-node fallback (if only one host in cluster)
func findLocalHost(hosts []*cluster.Host, hostname, daemonID string, localIPs map[string]struct{}, log log15.Logger) *cluster.Host {
	// 1. Match by daemon ID (highest priority)
	if daemonID != "" {
		for _, h := range hosts {
			if h.ID() == daemonID {
				log.Info("matched host by daemon ID", "daemon_id", daemonID)
				return h
			}
		}
	}

	// 2. Match by IP address
	if len(localIPs) > 0 {
		for _, h := range hosts {
			hostIP, _, err := net.SplitHostPort(h.Addr())
			if err != nil {
				continue
			}
			if _, ok := localIPs[hostIP]; ok {
				log.Info("matched host by IP address", "host_id", h.ID(), "ip", hostIP)
				return h
			}
		}
	}

	// 3. Match by hostname (exact, case-insensitive, normalized)
	normalizedHostname := normalizeHostname(hostname)
	for _, h := range hosts {
		hostID := h.ID()
		if hostID == hostname {
			log.Info("matched host by exact hostname", "host_id", hostID)
			return h
		}
		if strings.EqualFold(hostID, hostname) {
			log.Info("matched host by case-insensitive hostname", "host_id", hostID)
			return h
		}
		if normalizeHostname(hostID) == normalizedHostname {
			log.Info("matched host by normalized hostname", "host_id", hostID, "normalized", normalizedHostname)
			return h
		}
	}

	// 4. Single-node fallback
	if len(hosts) == 1 {
		log.Info("single host cluster, using the only available host", "host_id", hosts[0].ID())
		return hosts[0]
	}

	return nil
}

// getLocalIPs returns a set of all unicast IP addresses on the local machine.
func getLocalIPs() map[string]struct{} {
	ips := make(map[string]struct{})
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && !ip.IsLoopback() {
			ips[ip.String()] = struct{}{}
		}
	}
	return ips
}

// getDaemonID tries to get the running daemon's host ID and publish IP
// by querying the local flynn-host API on each local IP address. The
// daemon binds to the external IP (not 127.0.0.1), so we try all local
// IPs. publishIP is parsed from status.URL — the daemon's own configured
// publish address — and is the authoritative cluster-routable IP regardless
// of which local NIC happened to respond to the probe (e.g. a VirtualBox
// NAT or flannel bridge IP can answer 1113 but is not reachable from peers).
func getDaemonID(localIPs map[string]struct{}, log log15.Logger) (id, publishIP string) {
	for ip := range localIPs {
		// Skip IPv6 link-local addresses (fe80::) as the daemon
		// typically listens on a routable address
		if strings.HasPrefix(ip, "fe80:") {
			continue
		}
		addr := net.JoinHostPort(ip, "1113")
		h := cluster.NewHost("", "http://"+addr, nil, nil)
		status, err := h.GetStatus()
		if err != nil {
			log.Debug("could not reach daemon", "addr", addr, "err", err)
			continue
		}
		publishIP = parseHostFromURL(status.URL)
		log.Info("got daemon ID from local API", "id", status.ID, "addr", addr, "publish_ip", publishIP)
		return status.ID, publishIP
	}
	log.Debug("could not get daemon ID from any local IP")
	return "", ""
}

// parseHostFromURL extracts the host (without port) from a URL like
// "http://192.168.56.20:1113". Returns "" on parse failure.
func parseHostFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(u.Host); err == nil {
		return h
	}
	return u.Host
}

// parseChecksums reads a SHA512 checksum file and returns a map of filename -> checksum
func parseChecksums(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	checksums := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 {
			// Strip common prefixes from filename (*, ./, etc.)
			filename := parts[1]
			filename = strings.TrimPrefix(filename, "*")
			filename = strings.TrimPrefix(filename, "./")
			checksums[filename] = parts[0]
		}
	}
	return checksums, nil
}

// downloadAndInstallBinary downloads, verifies, and installs a single binary
func downloadAndInstallBinary(client *ghrelease.Client, repo, version, assetName, destName, tmpDir, binDir string, checksums map[string]string, log log15.Logger) error {
	log.Info("downloading binary", "name", assetName)

	// Download the gzipped binary
	assetURL := ghrelease.GetReleaseURL(repo, version) + "/" + assetName
	gzPath := filepath.Join(tmpDir, assetName)
	if err := client.DownloadFile(assetURL, gzPath); err != nil {
		log.Error("failed to download binary", "name", assetName, "err", err)
		return err
	}

	// Verify checksum
	expectedChecksum, ok := checksums[assetName]
	if !ok {
		return fmt.Errorf("no checksum found for %s", assetName)
	}
	if err := verifyChecksum(gzPath, expectedChecksum); err != nil {
		log.Error("checksum verification failed", "name", assetName, "err", err)
		return err
	}
	log.Info("checksum verified", "name", assetName)

	// Decompress and install
	destPath := filepath.Join(binDir, destName)
	if err := decompressAndInstall(gzPath, destPath, log); err != nil {
		return err
	}

	return nil
}

// verifyChecksum verifies a file's SHA512 checksum
func verifyChecksum(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

// decompressAndInstall decompresses a gzipped file and installs it atomically
func decompressAndInstall(gzPath, destPath string, log log15.Logger) error {
	log.Info("installing binary", "dest", destPath)

	src, err := os.Open(gzPath)
	if err != nil {
		return err
	}
	defer src.Close()

	gz, err := gzip.NewReader(src)
	if err != nil {
		return err
	}
	defer gz.Close()

	// Write to temp file first, then rename (atomic)
	tmpPath := destPath + ".tmp"
	dst, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}

	if _, err := io.Copy(dst, gz); err != nil {
		dst.Close()
		os.Remove(tmpPath)
		return err
	}
	dst.Close()

	return os.Rename(tmpPath, destPath)
}

const deployTimeout = 30 * time.Minute

// updateImages downloads the images manifest, triggers image-layer pulls
// on every cluster host in parallel, then deploys system apps via the
// controller. If baseURL is non-empty, images are fetched from that URL
// instead of GitHub. When force is true, system apps are redeployed even
// if the image manifest matches the currently deployed artifact.
// expectedHosts is the cluster size observed before any rolling restart;
// when > 1, we wait for that many hosts to be visible in discoverd
// before fanning out, so a partially-rejoined cluster doesn't silently
// skip nodes.
func updateImages(repo, configDir, targetVersion, baseURL string, force bool, expectedHosts int, log log15.Logger) error {
	// Create downloader (without volume manager - we're just getting the manifest)
	var d *downloader.Downloader
	if baseURL != "" {
		log.Info("downloading images manifest from base URL", "base_url", baseURL, "version", targetVersion)
		d = downloader.NewWithBaseURL(baseURL, nil, targetVersion, log)
	} else {
		log.Info("downloading images manifest from GitHub", "repo", repo, "version", targetVersion)
		d = downloader.New(repo, nil, targetVersion, log)
	}

	// Download images manifest
	images, err := d.DownloadImagesManifest(configDir)
	if err != nil {
		log.Error("error downloading images manifest", "err", err)
		return err
	}

	log.Info("downloaded images manifest", "num_images", len(images))

	// Download image layers on ALL nodes in the cluster
	// The images.json contains file:// URIs that reference local paths,
	// so we need to download the actual layer files on every node before deploying
	log.Info("triggering image layer downloads on all cluster nodes")

	// Get all hosts in the cluster. If a rolling restart just ran,
	// wait for discoverd to repopulate so we don't fan out to only the
	// subset of hosts that has finished rejoining raft.
	clusterClient := cluster.NewClient()
	if expectedHosts > 1 {
		if err := waitForClusterSize(clusterClient, expectedHosts, 3*time.Minute, log); err != nil {
			log.Warn("cluster did not fully repopulate before image pull, continuing with subset", "err", err)
		}
	}
	hosts, err := clusterClient.Hosts()
	if err != nil {
		log.Error("error discovering cluster hosts", "err", err)
		return fmt.Errorf("error discovering cluster hosts: %w", err)
	}

	if expectedHosts > 0 && len(hosts) < expectedHosts {
		log.Warn("found fewer hosts than expected for image pull", "num_hosts", len(hosts), "expected", expectedHosts)
	}
	log.Info("found cluster hosts", "num_hosts", len(hosts))

	// Trigger image pull on all hosts in parallel
	var wg sync.WaitGroup
	errChan := make(chan error, len(hosts))

	for _, host := range hosts {
		wg.Add(1)
		go func(h *cluster.Host) {
			defer wg.Done()

			hostLog := log.New("host", h.ID())
			hostLog.Info("starting image pull on host")

			// Retry image pulls up to 3 times to handle transient
			// connection errors (e.g. "unexpected EOF" from network
			// hiccups or host daemon instability after binary update).
			// Layer downloads are idempotent — already-cached layers
			// are skipped on retry.
			const maxPullAttempts = 3
			var lastErr error
			for attempt := 1; attempt <= maxPullAttempts; attempt++ {
				if attempt > 1 {
					hostLog.Warn("retrying image pull", "attempt", attempt, "previous_err", lastErr)
					time.Sleep(5 * time.Second)
				}

				// Create a channel to consume ImagePullInfo events
				ch := make(chan *ct.ImagePullInfo)

				// Trigger the pull on this host
				stream, err := h.PullImages(repo, configDir, targetVersion, baseURL, nil, ch)
				if err != nil {
					hostLog.Error("error starting image pull", "err", err)
					lastErr = fmt.Errorf("error pulling images on host %s: %w", h.ID(), err)
					continue
				}

				// Consume all events from the channel, blocking until the
				// stream is fully drained and the channel is closed.
				// This must happen BEFORE calling stream.Err() because the
				// stream's error is only set after the SSE decoder goroutine
				// finishes and closes the channel.
				for info := range ch {
					if info.Type == ct.ImagePullTypeLayer {
						hostLog.Debug("downloading layer", "layer", info.Layer.ID)
					}
				}

				// Now it's safe to check for errors
				if err := stream.Err(); err != nil {
					hostLog.Error("image pull failed", "err", err)
					lastErr = fmt.Errorf("image pull failed on host %s: %w", h.ID(), err)
					continue
				}

				lastErr = nil
				hostLog.Info("finished image pull on host")
				break
			}

			if lastErr != nil {
				errChan <- lastErr
			}
		}(host)
	}

	// Wait for all hosts to finish
	wg.Wait()
	close(errChan)

	// Check for any errors
	for err := range errChan {
		if err != nil {
			return err
		}
	}

	log.Info("finished downloading image layers on all nodes")

	// Wait for cluster to be ready after daemon restart.
	log.Info("waiting for cluster to be ready after daemon restart")
	statuses, err := waitForClusterHealthy(10*time.Minute, log)
	if err != nil {
		log.Warn("cluster health check did not pass after retries, continuing with update", "err", err)
		fmt.Printf("Warning: %s. The update will continue.\n", err)
	}

	// Connect to controller
	log.Info("connecting to controller")
	instances, err := discoverd.GetInstances("controller", 10*time.Second)
	if err != nil {
		log.Error("error discovering controller", "err", err)
		return fmt.Errorf("error discovering controller: %w", err)
	}
	if len(instances) == 0 {
		return fmt.Errorf("no controller instances found")
	}

	// Create an HTTP client with a custom dialer that resolves .discoverd
	// hostnames through the discoverd HTTP API, since the host's system DNS
	// resolver (systemd-resolved) doesn't know about the .discoverd zone.
	// This also ensures that when the controller deploys itself (one-by-one
	// strategy), ResumingStream reconnections resolve to whichever controller
	// instance is currently alive, rather than retrying a dead pinned IP.
	discoverdDial := func(network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(host, ".discoverd") {
			service := strings.TrimSuffix(host, ".discoverd")
			addrs, err := discoverd.NewService(service).Addrs()
			if err != nil {
				return nil, err
			}
			if len(addrs) == 0 {
				return nil, fmt.Errorf("lookup %s: no such host", host)
			}
			addr = addrs[0]
		}
		return dialer.Default.Dial(network, addr)
	}
	httpClient := &http.Client{Transport: &http.Transport{Dial: discoverdDial}}
	client, err := controller.NewClientWithHTTP("http://controller.discoverd", instances[0].Meta["AUTH_KEY"], httpClient)
	if err != nil {
		log.Error("error creating controller client", "err", err)
		return fmt.Errorf("error creating controller client: %w", err)
	}

	// Validate images
	log.Info("validating images for system apps")
	for _, app := range updater.SystemApps {
		if v := version.Parse(statuses[app.Name].Version); !v.Dev && app.MinVersion != "" && v.Before(version.Parse(app.MinVersion)) {
			log.Info("skipping system app update (can't upgrade from running version)",
				"app", app.Name, "version", v)
			continue
		}
		if _, ok := images[app.Name]; !ok {
			err := fmt.Errorf("missing image: %s", app.Name)
			log.Error(err.Error())
			return err
		}
	}

	// Repair any sirenia clusters that have deposed peers.  After a
	// simultaneous daemon restart the old primary gets deposed by the
	// sync's takeover.  The deposed peer never automatically rejoins,
	// leaving the cluster with no asyncs and blocking deployment.
	// Clearing the Deposed list lets the primary re-add them as asyncs.
	// This must happen BEFORE creating image artifacts because the
	// controller's CreateArtifact depends on blobstore, which depends
	// on postgres being fully healthy (with asyncs).
	repairSireniaClusters(log)

	// Create image artifacts for common images, with retries since
	// blobstore may still be stabilizing after the sirenia repair.
	log.Info("creating image artifacts")
	createArtifactWithRetry := func(name string, img *ct.Artifact) error {
		for attempt := 1; attempt <= 6; attempt++ {
			if err := client.CreateArtifact(img); err != nil {
				log.Warn("error creating image artifact, retrying",
					"name", name, "attempt", attempt, "err", err)
				time.Sleep(10 * time.Second)
				continue
			}
			return nil
		}
		return fmt.Errorf("failed to create %s image artifact after retries", name)
	}
	redisImage := images["redis"]
	if err := createArtifactWithRetry("redis", redisImage); err != nil {
		log.Error(err.Error())
		return err
	}
	slugRunner := images["slugrunner"]
	if err := createArtifactWithRetry("slugrunner", slugRunner); err != nil {
		log.Error(err.Error())
		return err
	}
	slugBuilder := images["slugbuilder"]
	if err := createArtifactWithRetry("slugbuilder", slugBuilder); err != nil {
		log.Error(err.Error())
		return err
	}

	// Deploy system apps in order
	log.Info("deploying system apps")
	for _, appInfo := range updater.SystemApps {
		if appInfo.ImageOnly {
			continue // skip ImageOnly updates
		}
		// Skip discoverd and flannel — their lifecycle is managed by the
		// host daemon's resurrection logic.  Redeploying them through the
		// controller uses an all-at-once strategy that kills every instance
		// simultaneously, which takes down DNS and overlay networking
		// cluster-wide and causes cascading failures in all other services.
		if appInfo.Name == "discoverd" || appInfo.Name == "flannel" {
			log.Info("skipping deploy of infrastructure app (managed by host daemon)", "name", appInfo.Name)
			continue
		}
		appLog := log.New("name", appInfo.Name)
		appLog.Info("starting deploy of system app")

		app, err := client.GetApp(appInfo.Name)
		if err == controller.ErrNotFound && appInfo.Optional {
			appLog.Info("skipped deploy of system app (optional app not present)")
			continue
		} else if err != nil {
			appLog.Error("error getting app", "err", err)
			return err
		}

		var deployErr error
		for attempt := 1; ; attempt++ {
			deployErr = deployApp(client, app, images[appInfo.Name], appInfo.UpdateRelease, force, appLog)
			if deployErr == nil {
				break
			}
			if e, ok := deployErr.(errDeploySkipped); ok {
				appLog.Info("skipped deploy of system app", "reason", e.reason)
				deployErr = nil
				break
			}
			// Sirenia-based apps plus transient discoverd failures (e.g.
			// leader.postgres.discoverd NXDOMAIN immediately after postgres
			// rollout) settle within a few retries.
			maxUnsettled := updaterdeploy.MaxTransientDeployUnsettledAttempts()
			if updaterdeploy.ShouldRetryAfterUnsettledDiscoverdLeader(deployErr) && attempt < maxUnsettled {
				appLog.Warn("discovery or sirenia cluster not settled, retrying deploy",
					"err", deployErr, "attempt", attempt, "max_attempts", maxUnsettled)
				time.Sleep(updaterdeploy.TransientDeployRetryDelay())
				continue
			}
			return deployErr
		}
		if deployErr != nil {
			continue
		}
		appLog.Info("finished deploy of system app")
		if appInfo.Name == "postgres" || appInfo.Name == "mariadb" || appInfo.Name == "mongodb" {
			updaterdeploy.WaitSireniaLeaderStable(appInfo.Name, appLog.New("after_system_app_deploy", appInfo.Name))
		}
	}

	// Deploy all other apps (Redis appliances and slugrunner apps)
	apps, err := client.AppList()
	if err != nil {
		log.Error("error getting apps", "err", err)
		return err
	}

	for _, app := range apps {
		appLog := log.New("name", app.Name)

		if app.RedisAppliance() {
			appLog.Info("starting deploy of Redis app")
			if err := deployApp(client, app, redisImage, nil, force, appLog); err != nil {
				if e, ok := err.(errDeploySkipped); ok {
					appLog.Info("skipped deploy of Redis app", "reason", e.reason)
					continue
				}
				return err
			}
			appLog.Info("finished deploy of Redis app")
			continue
		}

		if app.System() {
			continue
		}

		appLog.Info("starting deploy of app to update slugrunner")
		if err := deployApp(client, app, slugRunner, nil, force, appLog); err != nil {
			if e, ok := err.(errDeploySkipped); ok {
				appLog.Info("skipped deploy of app", "reason", e.reason)
				continue
			}
			return err
		}
		appLog.Info("finished deploy of app")
	}

	fmt.Println("System apps and container images updated successfully")
	return nil
}

type errDeploySkipped struct {
	reason string
}

func (e errDeploySkipped) Error() string {
	return e.reason
}

func deployApp(client controller.Client, app *ct.App, image *ct.Artifact, updateFn updater.UpdateReleaseFn, force bool, log log15.Logger) error {
	release, err := client.GetAppRelease(app.ID)
	if err != nil {
		log.Error("error getting release", "err", err)
		return err
	}
	if len(release.ArtifactIDs) == 0 {
		return errDeploySkipped{"release has no artifacts"}
	}
	artifact, err := client.GetArtifact(release.ArtifactIDs[0])
	if err != nil {
		log.Error("error getting release artifact", "err", err)
		return err
	}
	if !app.System() && release.IsGitDeploy() {
		if artifact.Meta["flynn.component"] != "slugrunner" {
			return errDeploySkipped{"app not using slugrunner image"}
		}
	}
	skipDeploy := artifact.Manifest().ID() == image.Manifest().ID()
	if skipDeploy && !force {
		return errDeploySkipped{"app is already using latest images"}
	}
	if skipDeploy {
		log.Info("forcing redeploy with matching image manifest", "manifest.id", image.Manifest().ID())
	}
	if err := client.CreateArtifact(image); err != nil {
		log.Error("error creating artifact", "err", err)
		return err
	}
	release.ID = ""
	release.ArtifactIDs[0] = image.ID
	if updateFn != nil {
		updateFn(release)
	}
	if err := client.CreateRelease(app.ID, release); err != nil {
		log.Error("error creating new release", "err", err)
		return err
	}
	timeoutCh := make(chan struct{})
	time.AfterFunc(deployTimeout, func() { close(timeoutCh) })
	if err := client.DeployAppRelease(app.ID, release.ID, timeoutCh); err != nil {
		log.Error("error deploying app", "err", err)
		return err
	}
	return nil
}

func decodeJSON(r io.Reader, v interface{}) error {
	return jsonDecoder(r).Decode(v)
}

func jsonDecoder(r io.Reader) *jsonDecoderWrapper {
	return &jsonDecoderWrapper{dec: json.NewDecoder(r)}
}

type jsonDecoderWrapper struct {
	dec *json.Decoder
}

func (d *jsonDecoderWrapper) Decode(v interface{}) error {
	return d.dec.Decode(v)
}

// runTarballUpdate performs an update from a local tarball file.
// It extracts the tarball, installs binaries locally, restarts the daemon,
// starts a temporary HTTP server to serve the extracted contents to all
// cluster nodes, and then deploys system apps.
func runTarballUpdate(args *docopt.Args, tarballPath, configDir string, log log15.Logger) error {
	binDir := args.String["--bin-dir"]
	skipImages := args.Bool["--skip-images"]
	imagesOnly := args.Bool["--images-only"]
	allNodes := args.Bool["--all-nodes"]
	force := args.Bool["--force"]

	if imagesOnly && !allNodes {
		n, err := clusterHostCount()
		if err != nil {
			return fmt.Errorf("--all-nodes is required with --images-only when cluster hosts cannot be discovered: %w", err)
		}
		if n > 1 {
			return fmt.Errorf("--images-only requires --all-nodes when the cluster has more than one host")
		}
	}

	log.Info("starting tarball-based update", "tarball", tarballPath)

	// Verify tarball exists
	if _, err := os.Stat(tarballPath); err != nil {
		return fmt.Errorf("tarball not found: %s", tarballPath)
	}

	// Extract tarball to a temp directory
	extractDir, err := os.MkdirTemp("", "flynn-tarball-update-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(extractDir)

	log.Info("extracting tarball", "dest", extractDir)
	tarballVersion, contentDir, err := extractTarball(tarballPath, extractDir)
	if err != nil {
		return fmt.Errorf("failed to extract tarball: %w", err)
	}
	log.Info("extracted tarball", "version", tarballVersion, "content_dir", contentDir)

	rolloutCluster := allNodes
	if !rolloutCluster && !skipImages {
		if n, err := clusterHostCount(); err == nil && n <= 1 {
			rolloutCluster = true
			log.Info("single-node cluster: rolling out images without --all-nodes")
		}
	}

	// Update binaries unless --images-only was specified
	if !imagesOnly {
		// Parse checksums from the tarball contents
		checksumPath := filepath.Join(contentDir, "checksums.sha512")
		checksums, err := parseChecksums(checksumPath)
		if err != nil {
			log.Warn("no checksums file in tarball, skipping verification", "err", err)
			checksums = nil
		}

		// Install binaries from extracted files
		binaries := []struct {
			gzName   string
			destName string
		}{
			{"flynn-host-linux-amd64.gz", "flynn-host"},
			{"flynn-init-linux-amd64.gz", "flynn-init"},
		}

		for _, bin := range binaries {
			gzPath := filepath.Join(contentDir, bin.gzName)
			if _, err := os.Stat(gzPath); err != nil {
				return fmt.Errorf("binary %s not found in tarball: %w", bin.gzName, err)
			}

			// Verify checksum if available
			if checksums != nil {
				if expected, ok := checksums[bin.gzName]; ok {
					if err := verifyChecksum(gzPath, expected); err != nil {
						return fmt.Errorf("checksum verification failed for %s: %w", bin.gzName, err)
					}
					log.Info("checksum verified", "name", bin.gzName)
				}
			}

			destPath := filepath.Join(binDir, bin.destName)
			if err := decompressAndInstall(gzPath, destPath, log); err != nil {
				return fmt.Errorf("failed to install %s: %w", bin.destName, err)
			}
		}

		log.Info("binaries installed", "version", tarballVersion)
		fmt.Printf("Flynn binaries installed from tarball (%s)\n", tarballVersion)

		// Trigger zero-downtime daemon restart unless --no-restart was specified
		if !args.Bool["--no-restart"] {
			restarted, err := restartDaemon(binDir, log)
			if err != nil {
				return err
			}
			if restarted {
				fmt.Printf("Flynn daemon restarted with version %s\n", tarballVersion)
			}
		} else {
			log.Info("skipping daemon restart (--no-restart specified)")
			fmt.Println("Daemon restart skipped. Restart manually to activate the new version.")
		}

		if !allNodes {
			log.Info("skipping remote host binary updates (--all-nodes not set)")
			fmt.Println("Other cluster hosts were not updated. Run the same tarball update on each node, then run it again with --all-nodes to pull images everywhere and deploy system apps—or pass --all-nodes on this command to update every host now.")
		}
	}

	// Temporary HTTP server: only when pushing to other nodes or rolling out images cluster-wide.
	needRemoteBinaries := !imagesOnly && allNodes
	needImages := !skipImages && rolloutCluster
	if needRemoteBinaries || needImages {
		listener, err := net.Listen("tcp", ":0")
		if err != nil {
			return fmt.Errorf("failed to start HTTP server: %w", err)
		}
		defer listener.Close()

		// Determine the coordinator's cluster-facing IP
		coordinatorIP, err := getCoordinatorIP(log)
		if err != nil {
			return fmt.Errorf("failed to determine coordinator IP: %w", err)
		}

		_, port, _ := net.SplitHostPort(listener.Addr().String())
		baseURL := fmt.Sprintf("http://%s:%s", coordinatorIP, port)
		log.Info("starting temporary HTTP file server", "base_url", baseURL, "serving", contentDir)

		// Start serving files in a goroutine
		srv := &http.Server{Handler: http.FileServer(http.Dir(contentDir))}
		go srv.Serve(listener)
		defer srv.Close()

		fmt.Printf("Temporary file server started at %s\n", baseURL)

		// Propagate binaries to all other cluster nodes
		var expectedHostCount int
		if needRemoteBinaries {
			n, err := updateRemoteBinaries("", binDir, configDir, tarballVersion, baseURL, args.Bool["--no-restart"], log)
			if err != nil {
				return err
			}
			expectedHostCount = n
		}

		// Update container images and system apps
		if needImages {
			if err := updateImages("", configDir, tarballVersion, baseURL, force, expectedHostCount, log); err != nil {
				return err
			}
		}
	} else if !skipImages && !rolloutCluster {
		log.Info("skipping container images and system app rollout (local-only tarball update)")
		fmt.Println("Skipping container images and system apps on this run. After flynn-host matches on every node, run the same tarball command with --all-nodes.")
	}

	log.Info("tarball update complete", "version", tarballVersion)
	fmt.Printf("Flynn updated to %s from tarball\n", tarballVersion)
	return nil
}

// extractTarball extracts a .tar.gz tarball to the given directory.
// Returns the version string (from the top-level directory name) and
// the path to the content directory.
func extractTarball(tarballPath, destDir string) (version, contentDir string, err error) {
	f, err := os.Open(tarballPath)
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", "", fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", fmt.Errorf("error reading tarball: %w", err)
		}

		// Detect version from the top-level directory name (e.g., "flynn-v20260228.0/")
		if version == "" {
			parts := strings.SplitN(hdr.Name, "/", 2)
			if len(parts) > 0 {
				dirName := parts[0]
				if strings.HasPrefix(dirName, "flynn-") {
					version = strings.TrimPrefix(dirName, "flynn-")
				} else {
					version = dirName
				}
				contentDir = filepath.Join(destDir, dirName)
			}
		}

		// Security: prevent path traversal
		target := filepath.Join(destDir, hdr.Name)
		if !strings.HasPrefix(target, destDir+string(os.PathSeparator)) && target != destDir {
			return "", "", fmt.Errorf("tarball contains path outside target: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return "", "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return "", "", err
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return "", "", err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return "", "", err
			}
			outFile.Close()
		}
	}

	if version == "" {
		return "", "", fmt.Errorf("could not determine version from tarball")
	}
	if contentDir == "" {
		return "", "", fmt.Errorf("tarball appears to be empty")
	}

	return version, contentDir, nil
}

// getCoordinatorIP determines the cluster-facing IP of this node by
// finding the local host in the cluster and extracting its IP address.
// If discoverd is not ready yet (e.g. after a daemon restart, when the
// daemon has unregistered but not yet re-registered), it retries a few
// times, then falls back to the daemon's own publish IP (authoritative
// for the cluster-routable address), and finally to detecting a suitable
// external IP from local network interfaces.
func getCoordinatorIP(log log15.Logger) (string, error) {
	localIPs := getLocalIPs()
	daemonID, daemonPublishIP := getDaemonID(localIPs, log)

	localHostname, _ := os.Hostname()

	// Try discoverd a few times — after a systemctl restart the daemon
	// may not have re-registered with discoverd yet.
	clusterClient := cluster.NewClient()
	for i := 0; i < 10; i++ {
		if i > 0 {
			time.Sleep(3 * time.Second)
		}
		hosts, err := clusterClient.Hosts()
		if err != nil {
			log.Debug("discoverd not ready for host lookup, retrying", "attempt", i+1, "err", err)
			continue
		}
		h := findLocalHost(hosts, localHostname, daemonID, localIPs, log)
		if h != nil {
			ip, _, err := net.SplitHostPort(h.Addr())
			if err != nil {
				return "", fmt.Errorf("error parsing host address %s: %w", h.Addr(), err)
			}
			log.Info("found coordinator IP from cluster", "ip", ip, "host_id", h.ID())
			return ip, nil
		}
		log.Debug("local host not found in cluster yet, retrying", "attempt", i+1)
	}

	// Fallback 1: use the daemon's own publish IP. This is the address
	// the daemon advertises to peers, so it is reachable from other
	// cluster nodes by construction and doesn't depend on discoverd
	// re-registration timing.
	if daemonPublishIP != "" {
		log.Info("using daemon publish IP as coordinator IP", "ip", daemonPublishIP)
		return daemonPublishIP, nil
	}

	// Fallback 2: heuristic interface scan. Last resort — may pick an
	// IP that isn't routable from peers (e.g. a hypervisor NAT address).
	log.Warn("could not find local host via discoverd or daemon API, falling back to interface IP detection")
	ip := getExternalIP(localIPs)
	if ip == "" {
		return "", fmt.Errorf("could not determine coordinator IP: no suitable external IP found on local interfaces")
	}
	log.Info("found coordinator IP from local interfaces", "ip", ip)
	return ip, nil
}

// getExternalIP picks a non-loopback, non-link-local IPv4 address from
// the provided set of local IPs. It prefers public IPs over private ones.
func getExternalIP(localIPs map[string]struct{}) string {
	var privateIP string
	for ipStr := range localIPs {
		ip := net.ParseIP(ipStr)
		if ip == nil || ip.To4() == nil {
			continue // skip IPv6 and unparseable
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		// Prefer public IPs
		if !isPrivateIP(ip) {
			return ipStr
		}
		if privateIP == "" {
			privateIP = ipStr
		}
	}
	return privateIP
}

// isPrivateIP returns true if the IP is in a private range (RFC 1918).
func isPrivateIP(ip net.IP) bool {
	privateRanges := []struct {
		start net.IP
		end   net.IP
	}{
		{net.ParseIP("10.0.0.0"), net.ParseIP("10.255.255.255")},
		{net.ParseIP("172.16.0.0"), net.ParseIP("172.31.255.255")},
		{net.ParseIP("192.168.0.0"), net.ParseIP("192.168.255.255")},
		{net.ParseIP("100.64.0.0"), net.ParseIP("100.127.255.255")}, // CGNAT
	}
	for _, r := range privateRanges {
		if bytesInRange(ip.To4(), r.start.To4(), r.end.To4()) {
			return true
		}
	}
	return false
}

func bytesInRange(ip, start, end net.IP) bool {
	for i := 0; i < len(ip); i++ {
		if ip[i] < start[i] {
			return false
		}
		if ip[i] > end[i] {
			return false
		}
	}
	return true
}


// repairSireniaClusters clears deposed peers from sirenia-managed services
// (postgres, mariadb, mongodb).  After a daemon restart the old primary may
// have been deposed by a sync takeover; the deposed peer never automatically
// rejoins, leaving the cluster without asyncs.  By removing them from the
// Deposed list the primary's evalClusterState will see them as new peers
// and add them as asyncs.
func repairSireniaClusters(log log15.Logger) {
	appliances := []string{"postgres", "mariadb", "mongodb"}
	for _, svc := range appliances {
		svcLog := log.New("service", svc)
		service := discoverd.NewService(svc)

		meta, err := service.GetMeta()
		if err != nil {
			// Service may not exist (e.g. mariadb/mongodb not provisioned)
			continue
		}

		var state sirenia.State
		if err := json.Unmarshal(meta.Data, &state); err != nil {
			svcLog.Warn("failed to decode sirenia state", "err", err)
			continue
		}

		if len(state.Deposed) == 0 {
			continue
		}

		svcLog.Info("clearing deposed peers from sirenia cluster",
			"deposed_count", len(state.Deposed))

		state.Deposed = nil

		data, err := json.Marshal(&state)
		if err != nil {
			svcLog.Error("failed to encode repaired sirenia state", "err", err)
			continue
		}
		meta.Data = data
		if err := service.SetMeta(meta); err != nil {
			svcLog.Error("failed to write repaired sirenia state", "err", err)
			continue
		}

		svcLog.Info("cleared deposed peers, waiting for cluster to reform")
		// Give the primary time to re-evaluate state and add the
		// formerly-deposed peers as asyncs.
		time.Sleep(10 * time.Second)
	}
}