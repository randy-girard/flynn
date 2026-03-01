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
			if err := restartDaemon(binDir, log); err != nil {
				return err
			}
			fmt.Printf("Flynn daemon restarted with version %s\n", release.TagName)
		} else {
			log.Info("skipping daemon restart (--no-restart specified)")
			fmt.Println("Daemon restart skipped. Restart manually to activate the new version.")
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

// restartDaemon connects to the running flynn-host daemon and triggers a
// zero-downtime restart by calling the /host/update API. The running daemon
// spawns the new binary as a child process, hands off the HTTP listener and
// state, then shuts down gracefully.
func restartDaemon(binDir string, log log15.Logger) error {
	log.Info("connecting to running daemon for zero-downtime restart")

	clusterClient := cluster.NewClient()
	hosts, err := clusterClient.Hosts()
	if err != nil {
		log.Error("error discovering hosts, cannot restart daemon automatically", "err", err)
		return fmt.Errorf("failed to connect to cluster (is discoverd running?): %s\nRestart manually with: systemctl restart flynn-host", err)
	}
	if len(hosts) == 0 {
		log.Warn("no hosts found, skipping daemon restart")
		fmt.Println("No running hosts found. Restart manually with: systemctl restart flynn-host")
		return nil
	}

	// Find the local host by matching the hostname
	localHostname, err := os.Hostname()
	if err != nil {
		log.Error("error getting local hostname", "err", err)
		return fmt.Errorf("failed to get local hostname: %s\nRestart manually with: systemctl restart flynn-host", err)
	}
	log.Info("looking for local host", "hostname", localHostname, "num_hosts", len(hosts))

	// Normalize hostname for comparison (remove dashes, lowercase)
	normalizedHostname := normalizeHostname(localHostname)

	var localHost *cluster.Host
	for _, h := range hosts {
		hostID := h.ID()
		normalizedHostID := normalizeHostname(hostID)
		log.Debug("checking host", "host_id", hostID, "normalized_id", normalizedHostID, "normalized_hostname", normalizedHostname)

		// Exact match
		if hostID == localHostname {
			localHost = h
			break
		}
		// Case-insensitive match
		if strings.EqualFold(hostID, localHostname) {
			localHost = h
			break
		}
		// Normalized match (handles dashes, underscores, case differences)
		// e.g., "flynn-test-node-3" matches "flynntestnode3"
		if normalizedHostID == normalizedHostname {
			localHost = h
			break
		}
	}

	// If no match by hostname, try single-node fallback
	if localHost == nil && len(hosts) == 1 {
		log.Info("single host cluster, using the only available host")
		localHost = hosts[0]
	}

	if localHost == nil {
		log.Error("could not identify local host in cluster", "hostname", localHostname, "available_hosts", hostIDs(hosts))
		return fmt.Errorf("could not identify local host '%s' in cluster. Available hosts: %v\nRestart manually with: systemctl restart flynn-host", localHostname, hostIDs(hosts))
	}

	log.Info("triggering zero-downtime daemon restart", "host", localHost.ID())
	fmt.Printf("Restarting flynn-host daemon on %s...\n", localHost.ID())

	status, err := localHost.GetStatus()
	if err != nil {
		log.Error("error getting host status", "err", err)
		return fmt.Errorf("failed to get host status: %s\nRestart manually with: systemctl restart flynn-host", err)
	}

	binaryPath := filepath.Join(binDir, "flynn-host")
	_, err = localHost.UpdateWithShutdownDelay(
		binaryPath,
		30*time.Second,
		append([]string{"daemon"}, status.Flags...)...,
	)
	if err != nil {
		log.Error("error triggering daemon restart", "err", err)
		return fmt.Errorf("failed to restart daemon: %s\nRestart manually with: systemctl restart flynn-host", err)
	}

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

	// Wait for cluster to be ready after daemon restart
	// This can take a few seconds as the daemon needs to fully start and
	// discoverd needs to reconnect and register services
	// We use discoverd client directly instead of DNS-based HTTP requests
	// because the host may not have discoverd DNS configured in /etc/resolv.conf
	log.Info("waiting for cluster to be ready after daemon restart")
	const maxRetries = 30
	const retryDelay = 2 * time.Second

	// First, wait for status-web service to be available via discoverd
	var statusInstances []*discoverd.Instance
	for i := 0; i < maxRetries; i++ {
		var err error
		statusInstances, err = discoverd.GetInstances("status-web", 5*time.Second)
		if err == nil && len(statusInstances) > 0 {
			if i > 0 {
				log.Info("status-web service is now available", "attempts", i+1, "instances", len(statusInstances))
			}
			break
		}
		if i < maxRetries-1 {
			if err != nil {
				log.Debug("status-web not ready, retrying", "attempt", i+1, "max", maxRetries, "err", err)
			} else {
				log.Debug("status-web not ready, no instances yet", "attempt", i+1, "max", maxRetries)
			}
			time.Sleep(retryDelay)
		} else {
			if err != nil {
				log.Error("status-web still not ready after max retries", "err", err)
				return fmt.Errorf("status-web not ready after %d attempts: %w", maxRetries, err)
			}
			log.Error("no status-web instances found after max retries")
			return fmt.Errorf("no status-web instances found after %d attempts", maxRetries)
		}
	}

	// Now check cluster status using the discovered instance address
	statusAddr := statusInstances[0].Addr
	log.Info("checking cluster status", "addr", statusAddr)
	req, err := http.NewRequest("GET", "http://"+statusAddr, nil)
	if err != nil {
		log.Error("error creating status request", "err", err)
		return fmt.Errorf("error creating status request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Error("error getting cluster status", "err", err)
		return fmt.Errorf("error getting cluster status: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		log.Error("cluster status is unhealthy", "code", res.StatusCode)
		return fmt.Errorf("cluster is unhealthy (status code %d)", res.StatusCode)
	}

	var statusWrapper struct {
		Data struct {
			Detail map[string]status.Status
		}
	}
	if err := decodeJSON(res.Body, &statusWrapper); err != nil {
		log.Error("error decoding cluster status", "err", err)
		return fmt.Errorf("error decoding cluster status: %w", err)
	}
	statuses := statusWrapper.Data.Detail

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
			if err := restartDaemon(binDir, log); err != nil {
				return err
			}
			fmt.Printf("Flynn daemon restarted with version %s\n", tarballVersion)
		} else {
			log.Info("skipping daemon restart (--no-restart specified)")
			fmt.Println("Daemon restart skipped. Restart manually to activate the new version.")
		}
	}

	// Update container images and system apps unless --skip-images was specified
	if !skipImages {
		// Start a temporary HTTP server to serve the extracted tarball contents
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

		if err := updateImages("", configDir, tarballVersion, baseURL, log); err != nil {
			return err
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
func getCoordinatorIP(log log15.Logger) (string, error) {
	clusterClient := cluster.NewClient()
	hosts, err := clusterClient.Hosts()
	if err != nil {
		return "", fmt.Errorf("error discovering cluster hosts: %w", err)
	}

	localHostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("error getting hostname: %w", err)
	}

	normalizedHostname := normalizeHostname(localHostname)

	for _, h := range hosts {
		hostID := h.ID()
		normalizedHostID := normalizeHostname(hostID)

		if hostID == localHostname ||
			strings.EqualFold(hostID, localHostname) ||
			normalizedHostID == normalizedHostname {
			ip, _, err := net.SplitHostPort(h.Addr())
			if err != nil {
				return "", fmt.Errorf("error parsing host address %s: %w", h.Addr(), err)
			}
			log.Info("found coordinator IP", "ip", ip, "host_id", hostID)
			return ip, nil
		}
	}

	// Single-node fallback
	if len(hosts) == 1 {
		ip, _, err := net.SplitHostPort(hosts[0].Addr())
		if err != nil {
			return "", fmt.Errorf("error parsing host address: %w", err)
		}
		log.Info("single host cluster, using the only available host", "ip", ip)
		return ip, nil
	}

	return "", fmt.Errorf("could not identify local host '%s' in cluster", localHostname)
}
