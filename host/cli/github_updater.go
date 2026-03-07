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
	"github.com/flynn/flynn/pkg/status"
	"github.com/flynn/flynn/pkg/version"
	updater "github.com/flynn/flynn/updater/types"
	"github.com/flynn/go-docopt"
	"github.com/inconshreveable/log15"
)

// runGitHubUpdate performs an update using GitHub Releases
func runGitHubUpdate(args *docopt.Args, repo, configDir string, log log15.Logger) error {
	client := ghrelease.NewClient(repo, log)
	binDir := args.String["--bin-dir"]
	targetVersion := args.String["--version"]
	checkOnly := args.Bool["--check"]
	force := args.Bool["--force"]
	skipImages := args.Bool["--skip-images"]
	imagesOnly := args.Bool["--images-only"]

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

		// Propagate binaries to all other cluster nodes and restart their daemons
		if err := updateRemoteBinaries(repo, binDir, configDir, release.TagName, "", args.Bool["--no-restart"], log); err != nil {
			return err
		}
	}

	// Update container images and system apps unless --skip-images was specified
	if !skipImages {
		if err := updateImages(repo, configDir, release.TagName, "", log); err != nil {
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
		if id := getDaemonID(localIPs, log); id != "" {
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
func updateRemoteBinaries(repo, binDir, configDir, version, baseURL string, noRestart bool, log log15.Logger) error {
	// Retry discoverd lookup — after a systemctl restart the local daemon
	// may not have re-registered with discoverd yet.
	clusterClient := cluster.NewClient()
	var hosts []*cluster.Host
	var err error
	for i := 0; i < 10; i++ {
		if i > 0 {
			time.Sleep(3 * time.Second)
		}
		hosts, err = clusterClient.Hosts()
		if err == nil && len(hosts) > 0 {
			break
		}
		if err != nil {
			log.Debug("discoverd not ready for remote binary update, retrying", "attempt", i+1, "err", err)
		} else {
			log.Debug("no hosts found via discoverd yet, retrying", "attempt", i+1)
		}
	}
	if err != nil {
		log.Warn("could not discover cluster hosts for remote binary update", "err", err)
		fmt.Println("Could not discover cluster hosts. Remote nodes were NOT updated.")
		return nil // non-fatal: local update succeeded
	}
	if len(hosts) <= 1 {
		log.Info("single-node cluster, no remote hosts to update")
		return nil
	}

	// Determine local host to skip
	localHostname, _ := os.Hostname()
	localIPs := getLocalIPs()
	daemonID := getDaemonID(localIPs, log)
	localHost := findLocalHost(hosts, localHostname, daemonID, localIPs, log)

	var localHostID string
	if localHost != nil {
		localHostID = localHost.ID()
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
			return fmt.Errorf("failed to update binaries on host %s: %w", h.ID(), err)
		}
		hostLog.Info("binaries updated on remote host")
		fmt.Printf("Binaries updated on %s\n", h.ID())

		if !noRestart {
			hostLog.Info("restarting daemon on remote host via systemctl")
			fmt.Printf("Restarting flynn-host daemon on %s via systemctl...\n", h.ID())

			if err := h.SystemctlRestart(); err != nil {
				hostLog.Error("error requesting systemctl restart on remote host", "err", err)
				return fmt.Errorf("failed to restart daemon on host %s: %w", h.ID(), err)
			}

			hostLog.Info("systemctl restart requested on remote host")
			fmt.Printf("Flynn daemon restart initiated on %s\n", h.ID())

			// Wait for the remote daemon to come back up before
			// proceeding to the next host (rolling restart).
			// The systemctl restart has a 2s delay before it runs,
			// plus time for the daemon to start up.
			time.Sleep(15 * time.Second)
		}
	}

	log.Info("all remote hosts updated")
	return nil
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

// getDaemonID tries to get the running daemon's host ID by querying
// the local flynn-host API on each local IP address. The daemon binds
// to the external IP (not 127.0.0.1), so we try all local IPs.
func getDaemonID(localIPs map[string]struct{}, log log15.Logger) string {
	// Try each local IP on the default flynn-host port
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
		log.Info("got daemon ID from local API", "id", status.ID, "addr", addr)
		return status.ID
	}
	log.Debug("could not get daemon ID from any local IP")
	return ""
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

// updateImages downloads the images manifest and updates system apps.
// If baseURL is non-empty, images are fetched from that URL instead of GitHub.
func updateImages(repo, configDir, targetVersion, baseURL string, log log15.Logger) error {
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

	// Get all hosts in the cluster
	clusterClient := cluster.NewClient()
	hosts, err := clusterClient.Hosts()
	if err != nil {
		log.Error("error discovering cluster hosts", "err", err)
		return fmt.Errorf("error discovering cluster hosts: %w", err)
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
	// We re-discover the status-web instance on each attempt because
	// container IPs change after daemon restarts (flannel reassigns them).
	log.Info("waiting for cluster to be ready after daemon restart")
	const healthCheckMaxRetries = 30
	const healthCheckRetryDelay = 5 * time.Second

	var statuses map[string]status.Status
	clusterHealthy := false
	for i := 0; i < healthCheckMaxRetries; i++ {
		if i > 0 {
			time.Sleep(healthCheckRetryDelay)
		}

		// Re-discover status-web on each attempt — instances may change
		// after daemon restarts as containers get new overlay IPs.
		statusInstances, err := discoverd.GetInstances("status-web", 5*time.Second)
		if err != nil || len(statusInstances) == 0 {
			if err != nil {
				log.Debug("status-web not discoverable yet", "attempt", i+1, "err", err)
			} else {
				log.Debug("no status-web instances yet", "attempt", i+1)
			}
			continue
		}

		statusAddr := statusInstances[0].Addr
		log.Info("checking cluster status", "addr", statusAddr, "attempt", i+1)
		req, err := http.NewRequest("GET", "http://"+statusAddr, nil)
		if err != nil {
			log.Debug("error creating status request", "attempt", i+1, "err", err)
			continue
		}
		req.Header.Set("Accept", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Debug("error reaching status endpoint", "attempt", i+1, "addr", statusAddr, "err", err)
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
			log.Debug("error decoding status response", "attempt", i+1, "err", decodeErr)
			continue
		}

		if res.StatusCode == 200 {
			if i > 0 {
				log.Info("cluster is now healthy", "attempts", i+1)
			}
			statuses = statusWrapper.Data.Detail
			clusterHealthy = true
			break
		}

		// Log which services are unhealthy for debugging
		var unhealthyServices []string
		for name, svc := range statusWrapper.Data.Detail {
			if svc.Status != status.CodeHealthy {
				unhealthyServices = append(unhealthyServices, name)
			}
		}
		log.Debug("cluster not yet healthy", "attempt", i+1, "code", res.StatusCode, "unhealthy", unhealthyServices)
		statuses = statusWrapper.Data.Detail
	}

	if !clusterHealthy {
		// Log which services are still unhealthy
		var unhealthyServices []string
		for name, svc := range statuses {
			if svc.Status != status.CodeHealthy {
				unhealthyServices = append(unhealthyServices, name)
			}
		}
		log.Warn("cluster health check did not pass after retries, continuing with update", "unhealthy_services", unhealthyServices)
		fmt.Printf("Warning: cluster health check did not pass (unhealthy services: %v). The update will continue.\n", unhealthyServices)
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

	// Create image artifacts for common images
	log.Info("creating image artifacts")
	redisImage := images["redis"]
	if err := client.CreateArtifact(redisImage); err != nil {
		log.Error("error creating redis image artifact", "err", err)
		return err
	}
	slugRunner := images["slugrunner"]
	if err := client.CreateArtifact(slugRunner); err != nil {
		log.Error("error creating slugrunner image artifact", "err", err)
		return err
	}
	slugBuilder := images["slugbuilder"]
	if err := client.CreateArtifact(slugBuilder); err != nil {
		log.Error("error creating slugbuilder image artifact", "err", err)
		return err
	}

	// Deploy system apps in order
	log.Info("deploying system apps")
	for _, appInfo := range updater.SystemApps {
		if appInfo.ImageOnly {
			continue // skip ImageOnly updates
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

		if err := deployApp(client, app, images[appInfo.Name], appInfo.UpdateRelease, appLog); err != nil {
			if e, ok := err.(errDeploySkipped); ok {
				appLog.Info("skipped deploy of system app", "reason", e.reason)
				continue
			}
			return err
		}
		appLog.Info("finished deploy of system app")
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
			if err := deployApp(client, app, redisImage, nil, appLog); err != nil {
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
		if err := deployApp(client, app, slugRunner, nil, appLog); err != nil {
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

func deployApp(client controller.Client, app *ct.App, image *ct.Artifact, updateFn updater.UpdateReleaseFn, log log15.Logger) error {
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
	if skipDeploy {
		return errDeploySkipped{"app is already using latest images"}
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
	}

	// Start a temporary HTTP server to serve the extracted tarball contents.
	// This is needed for both remote binary propagation and image updates.
	needRemoteBinaries := !imagesOnly
	needImages := !skipImages
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
		if needRemoteBinaries {
			if err := updateRemoteBinaries("", binDir, configDir, tarballVersion, baseURL, args.Bool["--no-restart"], log); err != nil {
				return err
			}
		}

		// Update container images and system apps
		if needImages {
			if err := updateImages("", configDir, tarballVersion, baseURL, log); err != nil {
				return err
			}
		}
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
// If discoverd is not ready yet (e.g. after a daemon restart), it retries
// a few times, then falls back to detecting a suitable external IP from
// local network interfaces.
func getCoordinatorIP(log log15.Logger) (string, error) {
	localIPs := getLocalIPs()
	daemonID := getDaemonID(localIPs, log)

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

	// Fallback: find a suitable external IP from local interfaces
	log.Warn("could not find local host via discoverd, falling back to interface IP detection")
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
