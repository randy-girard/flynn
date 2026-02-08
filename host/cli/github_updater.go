package cli

import (
	"compress/gzip"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/host/downloader"
	"github.com/flynn/flynn/pkg/cluster"
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
		if err := updateImages(repo, configDir, release.TagName, log); err != nil {
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

// updateImages downloads the images manifest from GitHub and updates system apps
func updateImages(repo, configDir, targetVersion string, log log15.Logger) error {
	log.Info("downloading images manifest from GitHub", "repo", repo, "version", targetVersion)

	// Create downloader (without volume manager - we're just getting the manifest)
	d := downloader.New(repo, nil, targetVersion, log)

	// Download images manifest
	images, err := d.DownloadImagesManifest(configDir)
	if err != nil {
		log.Error("error downloading images manifest", "err", err)
		return err
	}

	log.Info("downloaded images manifest", "num_images", len(images))

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

	client, err := controller.NewClient("http://"+instances[0].Addr, instances[0].Meta["AUTH_KEY"])
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
