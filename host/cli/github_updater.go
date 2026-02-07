package cli

import (
	"compress/gzip"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/flynn/pkg/ghrelease"
	"github.com/flynn/flynn/pkg/installsource"
	"github.com/flynn/flynn/pkg/version"
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
	if args.Bool["--no-restart"] {
		log.Info("skipping daemon restart (--no-restart specified)")
		fmt.Println("Daemon restart skipped. Restart manually to activate the new version.")
		return nil
	}

	if err := restartDaemon(binDir, log); err != nil {
		return err
	}

	log.Info("update complete", "version", release.TagName)
	fmt.Printf("Flynn daemon restarted with version %s\n", release.TagName)
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

	// Find the local host by matching the current PID or just use the first host
	// In a typical single-node setup there is only one host
	var localHost *cluster.Host
	for _, h := range hosts {
		status, err := h.GetStatus()
		if err != nil {
			continue
		}
		if status.PID == os.Getpid() || len(hosts) == 1 {
			localHost = h
			break
		}
	}
	if localHost == nil {
		// Fall back to the first host if we can't identify the local one
		localHost = hosts[0]
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
