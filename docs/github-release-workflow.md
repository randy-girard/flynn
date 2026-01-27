# Flynn GitHub Release Workflow

This document outlines how to create a **new, parallel** GitHub Releases pipeline for distributing Flynn components. The existing TUF/S3 infrastructure will remain intact and fully functional.

## Goals

1. **Keep existing TUF/S3 pipeline intact** - No changes to current release process
2. **Add new GitHub Releases pipeline** - Alternative distribution method
3. **Track installation source** - Remember how Flynn was installed (TUF vs GitHub)
4. **Consistent update behavior** - Updates use the same source as the original installation
5. **Manual updates only** - Updates are triggered by explicit command, never automatic

---

## Current Architecture (Unchanged)

The existing TUF/S3 pipeline will continue to work as-is:

1. **Build Phase** (`make` / `script/build-flynn`)
   - Compiles Go binaries (flynn-host, flynn-init, CLI for multiple platforms)
   - Builds container images and squashfs layers
   - Generates manifests (images.json, bootstrap-manifest.json)

2. **Export Phase** (`script/export-components` / `builder/export.go`)
   - Gzips binaries and adds them to a TUF repository
   - Exports container image layers as squashfs files
   - Signs all artifacts using TUF keys

3. **Release Phase** (`script/release-components`)
   - Downloads existing TUF metadata from S3
   - Adds new version's components to TUF repo
   - Uploads everything to S3/CloudFront

4. **Install Phase** (`script/install-flynn`)
   - Downloads `flynn-host` binary from TUF repository (using checksum)
   - Uses `flynn-host download` to fetch remaining components from TUF

---

## New GitHub Release Pipeline (Parallel System)

### Overview

Add GitHub Releases as an **additional** distribution channel:

```
Build (GitHub Actions)  →  Package Artifacts  →  Create GitHub Release  →  Install from Release
```

### Release Artifact Structure

Each GitHub Release (e.g., `v2024.01.27.0`) should contain:

```
flynn-v2024.01.27.0/
├── flynn-host-linux-amd64.gz              # Main host binary
├── flynn-init-linux-amd64.gz              # Init binary
├── flynn-linux-amd64.gz                   # CLI (Linux)
├── flynn-darwin-amd64.gz                  # CLI (macOS)
├── flynn-windows-amd64.exe.gz             # CLI (Windows)
├── images.json.gz                         # Container image manifests
├── bootstrap-manifest.json.gz             # Bootstrap configuration
├── layers.tar.gz                          # All squashfs layers bundled
├── checksums.sha512                       # SHA512 checksums for all files
└── install-flynn                          # Install script
```

---

## GitHub Actions Workflow

Create `.github/workflows/release.yml`:

```yaml
name: Build and Release

on:
  push:
    tags:
      - 'v*'
  workflow_dispatch:
    inputs:
      version:
        description: 'Release version (e.g., v2024.01.27.0)'
        required: true

env:
  GO_VERSION: '1.21'

jobs:
  build:
    runs-on: ubuntu-22.04
    steps:
      - name: Checkout code
        uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: ${{ env.GO_VERSION }}

      - name: Determine version
        id: version
        run: |
          if [ "${{ github.event_name }}" = "workflow_dispatch" ]; then
            echo "VERSION=${{ github.event.inputs.version }}" >> $GITHUB_OUTPUT
          else
            echo "VERSION=${GITHUB_REF#refs/tags/}" >> $GITHUB_OUTPUT
          fi

      - name: Build binaries
        run: |
          export FLYNN_VERSION="${{ steps.version.outputs.VERSION }}"
          make release

      - name: Package release artifacts
        run: |
          VERSION="${{ steps.version.outputs.VERSION }}"
          mkdir -p release-artifacts

          # Compress binaries
          gzip -c build/bin/flynn-host > release-artifacts/flynn-host-linux-amd64.gz
          gzip -c build/bin/flynn-init > release-artifacts/flynn-init-linux-amd64.gz
          gzip -c build/bin/flynn-linux-amd64 > release-artifacts/flynn-linux-amd64.gz
          gzip -c build/bin/flynn-darwin-amd64 > release-artifacts/flynn-darwin-amd64.gz
          gzip -c build/bin/flynn-windows-amd64.exe > release-artifacts/flynn-windows-amd64.exe.gz

          # Compress manifests
          gzip -c build/manifests/images.json > release-artifacts/images.json.gz
          gzip -c build/manifests/bootstrap-manifest.json > release-artifacts/bootstrap-manifest.json.gz

          # Bundle layers (if they exist)
          if [ -d "build/layers" ]; then
            tar -czvf release-artifacts/layers.tar.gz -C build/layers .
          fi

          # Generate checksums
          cd release-artifacts
          sha512sum * > checksums.sha512

          # Copy install script
          cp ../script/install-flynn install-flynn

      - name: Create GitHub Release
        uses: softprops/action-gh-release@v1
        with:
          tag_name: ${{ steps.version.outputs.VERSION }}
          name: Flynn ${{ steps.version.outputs.VERSION }}
          draft: false
          prerelease: false
          files: |
            release-artifacts/*
          body: |
            ## Flynn Release ${{ steps.version.outputs.VERSION }}

            ### Installation
            ```bash
            curl -fsSL https://github.com/${{ github.repository }}/releases/download/${{ steps.version.outputs.VERSION }}/install-flynn | sudo bash
            ```

            ### Components
            - flynn-host: Host daemon binary
            - flynn-init: Container init binary
            - flynn CLI: Command-line interface (Linux, macOS, Windows)
            - Container images and layers
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

---

## New Install Script for GitHub Releases

Create a **new** script `script/install-flynn-github` (the existing `script/install-flynn` remains unchanged):

```bash
#!/bin/bash
# Install Flynn from GitHub Releases
# This is a NEW script - does not modify the existing install-flynn

set -eo pipefail

REPO="${FLYNN_GITHUB_REPO:-flynn/flynn}"
VERSION="${FLYNN_VERSION:-latest}"
CONFIG_DIR="/etc/flynn"

# Determine version
if [ "$VERSION" = "latest" ]; then
  info "fetching latest release version..."
  VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | jq -r .tag_name)
fi

info "installing Flynn ${VERSION} from GitHub (${REPO})"

RELEASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

# Download and verify flynn-host
info "downloading flynn-host binary..."
curl -fsSL -o /tmp/flynn-host.gz "${RELEASE_URL}/flynn-host-linux-amd64.gz"
curl -fsSL -o /tmp/checksums.sha512 "${RELEASE_URL}/checksums.sha512"

# Verify checksum
info "verifying checksum..."
cd /tmp
grep "flynn-host-linux-amd64.gz" checksums.sha512 | sha512sum --check --status

# Install
gunzip -f flynn-host.gz
chmod +x flynn-host
mv flynn-host /usr/local/bin/

# Download remaining components using flynn-host
info "downloading remaining components..."
flynn-host download --github-repo "${REPO}" --version "${VERSION}"

# Record installation source for future updates
info "recording installation source..."
mkdir -p "${CONFIG_DIR}"
cat > "${CONFIG_DIR}/install-source.json" <<EOF
{
  "source": "github",
  "repository": "${REPO}",
  "version": "${VERSION}",
  "installed_at": "$(date -Iseconds)"
}
EOF

info "installation complete!"
info "Flynn will use GitHub Releases for future updates."
info "Run 'flynn-host update' to check for and install updates."
```

**Key difference**: This script writes `/etc/flynn/install-source.json` with `"source": "github"`, so future `flynn-host update` commands will automatically use GitHub Releases.

---

## Key Changes Required

### 1. Modify `builder/export.go`

Update the exporter to create release artifacts instead of TUF targets:

```go
// Add a new export mode for GitHub releases
func (e *Exporter) ExportForGitHubRelease(outputDir string) error {
    // Export all binaries to outputDir (gzipped)
    // Export manifests to outputDir (gzipped)
    // Create checksums file
    return nil
}
```

### 2. Modify `host/downloader/downloader.go`

Add support for downloading from GitHub Releases:

```go
// Add a new downloader mode that uses GitHub Release URLs
type GitHubReleaseDownloader struct {
    repo    string   // e.g., "flynn/flynn"
    version string   // e.g., "v2024.01.27.0"
}

func (d *GitHubReleaseDownloader) DownloadBinaries(dir string) error {
    baseURL := fmt.Sprintf("https://github.com/%s/releases/download/%s", d.repo, d.version)
    // Download each binary from the release
    return nil
}
```

### 3. Modify `host/cli/download.go`

Add a `--github-repo` flag to support GitHub Release downloads:

```go
Register("download", runDownload, `
usage: flynn-host download [--repository=<uri>] [--github-repo=<owner/repo>] [--version=<ver>] ...

Options:
  --github-repo=<owner/repo>  Download from GitHub Release instead of TUF
  --version=<ver>             Version to download (required with --github-repo)
`)
```

### 4. Create `script/package-release`

New script to package artifacts for GitHub Release:

```bash
#!/bin/bash
set -eo pipefail

VERSION="${1:?Usage: $0 <version>}"
OUTPUT_DIR="${2:-release-artifacts}"

mkdir -p "${OUTPUT_DIR}"

# Package binaries
for bin in flynn-host flynn-init flynn-linux-amd64 flynn-darwin-amd64; do
  if [ -f "build/bin/${bin}" ]; then
    gzip -c "build/bin/${bin}" > "${OUTPUT_DIR}/${bin}.gz"
  fi
done

# Package manifests
for manifest in images.json bootstrap-manifest.json; do
  if [ -f "build/manifests/${manifest}" ]; then
    gzip -c "build/manifests/${manifest}" > "${OUTPUT_DIR}/${manifest}.gz"
  fi
done

# Generate checksums
cd "${OUTPUT_DIR}"
sha512sum *.gz > checksums.sha512
```

---

## Version Numbering

GitHub Release tags should follow the pattern:
- `v{YEAR}.{MONTH}.{DAY}.{BUILD}` (e.g., `v2024.01.27.0`)
- Or semantic versioning: `v1.0.0`, `v1.1.0`, etc.

The GitHub Actions workflow automatically extracts the version from the git tag.

---

## Installation Source Tracking

When Flynn is installed, the installation method is recorded so that future updates use the same source.

### Installation Source File

Create `/etc/flynn/install-source.json` during installation:

```json
{
  "source": "github",
  "repository": "flynn/flynn",
  "version": "v2024.01.27.0",
  "installed_at": "2024-01-27T10:00:00Z"
}
```

Or for TUF installations:

```json
{
  "source": "tuf",
  "repository": "https://dl.flynn.io/tuf",
  "version": "v2024.01.27.0",
  "installed_at": "2024-01-27T10:00:00Z"
}
```

### How It Works

1. **During Installation**: The install script writes `install-source.json`
2. **During Updates**: The `flynn-host update` command reads this file
3. **Automatic Routing**: Updates are fetched from the original source
4. **Override Option**: Users can force a different source with `--source` flag

---

## Update Process (Manual Only)

### Overview

**Updates are never automatic.** They must be explicitly triggered by running:

```bash
flynn-host update
```

The update command will:
1. Read `/etc/flynn/install-source.json` to determine the installation source
2. Check for new versions from that source (GitHub or TUF)
3. Download and install the update if a newer version is available

### 1. Create `pkg/ghrelease/ghrelease.go`

New package for GitHub Release operations:

```go
package ghrelease

import (
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "os"
    "path/filepath"
    "strings"
    "time"

    "github.com/inconshrevitable/log15"
)

const (
    GitHubAPIBase = "https://api.github.com"
    UserAgent     = "flynn-updater"
)

// Release represents a GitHub release
type Release struct {
    TagName     string    `json:"tag_name"`
    Name        string    `json:"name"`
    Draft       bool      `json:"draft"`
    Prerelease  bool      `json:"prerelease"`
    PublishedAt time.Time `json:"published_at"`
    Assets      []Asset   `json:"assets"`
}

// Asset represents a release asset
type Asset struct {
    Name               string `json:"name"`
    Size               int64  `json:"size"`
    BrowserDownloadURL string `json:"browser_download_url"`
}

// Client handles GitHub Release operations
type Client struct {
    repo       string // e.g., "flynn/flynn"
    httpClient *http.Client
    log        log15.Logger
}

// NewClient creates a new GitHub Release client
func NewClient(repo string, log log15.Logger) *Client {
    return &Client{
        repo:       repo,
        httpClient: &http.Client{Timeout: 30 * time.Second},
        log:        log,
    }
}

// GetLatestRelease fetches the latest release info
func (c *Client) GetLatestRelease() (*Release, error) {
    url := fmt.Sprintf("%s/repos/%s/releases/latest", GitHubAPIBase, c.repo)
    return c.getRelease(url)
}

// GetRelease fetches a specific release by tag
func (c *Client) GetReleaseByTag(tag string) (*Release, error) {
    url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", GitHubAPIBase, c.repo, tag)
    return c.getRelease(url)
}

// ListReleases fetches all releases (for channel support)
func (c *Client) ListReleases() ([]Release, error) {
    url := fmt.Sprintf("%s/repos/%s/releases", GitHubAPIBase, c.repo)

    req, err := http.NewRequest("GET", url, nil)
    if err != nil {
        return nil, err
    }
    req.Header.Set("User-Agent", UserAgent)

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
    }

    var releases []Release
    if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
        return nil, err
    }
    return releases, nil
}

// CheckForUpdate compares current version with latest release
func (c *Client) CheckForUpdate(currentVersion string) (*Release, bool, error) {
    latest, err := c.GetLatestRelease()
    if err != nil {
        return nil, false, err
    }

    // Compare versions (strip 'v' prefix for comparison)
    current := strings.TrimPrefix(currentVersion, "v")
    latestTag := strings.TrimPrefix(latest.TagName, "v")

    if latestTag > current {
        return latest, true, nil
    }
    return latest, false, nil
}

// DownloadAsset downloads a release asset to the specified directory
func (c *Client) DownloadAsset(asset *Asset, destDir string) (string, error) {
    destPath := filepath.Join(destDir, asset.Name)

    c.log.Info("downloading asset", "name", asset.Name, "size", asset.Size)

    resp, err := c.httpClient.Get(asset.BrowserDownloadURL)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return "", fmt.Errorf("download failed with status %d", resp.StatusCode)
    }

    out, err := os.Create(destPath)
    if err != nil {
        return "", err
    }
    defer out.Close()

    _, err = io.Copy(out, resp.Body)
    return destPath, err
}

func (c *Client) getRelease(url string) (*Release, error) {
    req, err := http.NewRequest("GET", url, nil)
    if err != nil {
        return nil, err
    }
    req.Header.Set("User-Agent", UserAgent)

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode == http.StatusNotFound {
        return nil, fmt.Errorf("release not found")
    }
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
    }

    var release Release
    if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
        return nil, err
    }
    return &release, nil
}
```

### 2. Create `pkg/installsource/installsource.go`

Package to manage installation source tracking:

```go
package installsource

import (
    "encoding/json"
    "os"
    "path/filepath"
    "time"
)

const (
    SourceGitHub = "github"
    SourceTUF    = "tuf"

    DefaultConfigDir = "/etc/flynn"
    SourceFileName   = "install-source.json"
)

// InstallSource records how Flynn was installed
type InstallSource struct {
    Source      string    `json:"source"`      // "github" or "tuf"
    Repository  string    `json:"repository"`  // repo URL or owner/repo
    Version     string    `json:"version"`     // installed version
    InstalledAt time.Time `json:"installed_at"`
}

// Load reads the installation source from config directory
func Load(configDir string) (*InstallSource, error) {
    path := filepath.Join(configDir, SourceFileName)
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }

    var source InstallSource
    if err := json.Unmarshal(data, &source); err != nil {
        return nil, err
    }
    return &source, nil
}

// Save writes the installation source to config directory
func Save(configDir string, source *InstallSource) error {
    path := filepath.Join(configDir, SourceFileName)
    data, err := json.MarshalIndent(source, "", "  ")
    if err != nil {
        return err
    }
    return os.WriteFile(path, data, 0644)
}

// IsGitHub returns true if installed from GitHub Releases
func (s *InstallSource) IsGitHub() bool {
    return s.Source == SourceGitHub
}

// IsTUF returns true if installed from TUF repository
func (s *InstallSource) IsTUF() bool {
    return s.Source == SourceTUF
}
```

### 3. Modify `host/cli/update.go`

Update the existing update command to detect installation source:

```go
// Add to existing update.go - modify runUpdate function

func runUpdate(args *docopt.Args) error {
    log := log15.New()
    configDir := args.String["--config-dir"]

    // Check for explicit source override
    sourceOverride := args.String["--source"]

    // Load installation source
    source, err := installsource.Load(configDir)
    if err != nil {
        // If no install-source.json exists, assume TUF (legacy installation)
        log.Warn("no install-source.json found, assuming TUF installation")
        source = &installsource.InstallSource{
            Source:     installsource.SourceTUF,
            Repository: args.String["--repository"],
        }
    }

    // Allow override via command line
    if sourceOverride != "" {
        source.Source = sourceOverride
    }

    log.Info("update source detected", "source", source.Source, "repository", source.Repository)

    // Route to appropriate update handler
    switch source.Source {
    case installsource.SourceGitHub:
        return runGitHubUpdate(args, source, log)
    case installsource.SourceTUF:
        return runTUFUpdate(args, source, log)
    default:
        return fmt.Errorf("unknown installation source: %s", source.Source)
    }
}
```

Update the command registration to include new options:

```go
func init() {
    Register("update", runUpdate, `
usage: flynn-host update [options]

Options:
  -r --repository=<uri>    TUF repository URI [default: https://dl.flynn.io/tuf]
  -t --tuf-db=<path>       Local TUF file [default: /etc/flynn/tuf.db]
  -c --config-dir=<dir>    Config directory [default: /etc/flynn]
  -b --bin-dir=<dir>       Binary directory [default: /usr/local/bin]
  -v --version=<ver>       Update to specific version (default: latest)
  --check                  Only check for updates, don't install
  --source=<source>        Override update source (github or tuf)
  --force                  Force update even if on latest version

Update Flynn to the latest version.

The update source is automatically detected from the installation method.
Use --source to override if needed.`)
}
```

### 4. Create `host/cli/github_updater.go`

GitHub-specific update logic (called by update.go):

```go
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

    "github.com/flynn/flynn/pkg/ghrelease"
    "github.com/flynn/flynn/pkg/installsource"
    "github.com/flynn/flynn/pkg/version"
    "github.com/flynn/go-docopt"
    "github.com/inconshreveable/log15"
)

func runGitHubUpdate(args *docopt.Args) error {
    log := log15.New()

    repo := args.String["--repo"]
    binDir := args.String["--bin-dir"]
    configDir := args.String["--config-dir"]
    checkOnly := args.Bool["--check"]
    force := args.Bool["--force"]
    targetVersion := args.String["--version"]

    client := ghrelease.NewClient(repo, log)
    currentVersion := version.String()

    log.Info("checking for updates", "current_version", currentVersion, "repo", repo)

    var release *ghrelease.Release
    var err error

    if targetVersion != "" {
        release, err = client.GetReleaseByTag(targetVersion)
        if err != nil {
            return fmt.Errorf("failed to get release %s: %s", targetVersion, err)
        }
    } else {
        var hasUpdate bool
        release, hasUpdate, err = client.CheckForUpdate(currentVersion)
        if err != nil {
            return fmt.Errorf("failed to check for updates: %s", err)
        }

        if !hasUpdate && !force {
            log.Info("already running latest version", "version", currentVersion)
            return nil
        }
    }

    log.Info("update available", "current", currentVersion, "latest", release.TagName)

    if checkOnly {
        fmt.Printf("Update available: %s -> %s\n", currentVersion, release.TagName)
        return nil
    }

    // Download and install update
    return installGitHubUpdate(client, release, binDir, configDir, log)
}

func installGitHubUpdate(client *ghrelease.Client, release *ghrelease.Release, binDir, configDir string, log log15.Logger) error {
    tmpDir, err := os.MkdirTemp("", "flynn-update-")
    if err != nil {
        return err
    }
    defer os.RemoveAll(tmpDir)

    // Download checksums first
    var checksumAsset *ghrelease.Asset
    for i := range release.Assets {
        if release.Assets[i].Name == "checksums.sha512" {
            checksumAsset = &release.Assets[i]
            break
        }
    }

    checksums := make(map[string]string)
    if checksumAsset != nil {
        checksumPath, err := client.DownloadAsset(checksumAsset, tmpDir)
        if err != nil {
            return fmt.Errorf("failed to download checksums: %s", err)
        }
        checksums, err = parseChecksums(checksumPath)
        if err != nil {
            return fmt.Errorf("failed to parse checksums: %s", err)
        }
    }

    // Download and verify each binary
    binaries := []string{"flynn-host-linux-amd64.gz", "flynn-init-linux-amd64.gz", "flynn-linux-amd64.gz"}

    for _, binName := range binaries {
        var asset *ghrelease.Asset
        for i := range release.Assets {
            if release.Assets[i].Name == binName {
                asset = &release.Assets[i]
                break
            }
        }
        if asset == nil {
            log.Warn("asset not found in release", "name", binName)
            continue
        }

        // Download
        path, err := client.DownloadAsset(asset, tmpDir)
        if err != nil {
            return fmt.Errorf("failed to download %s: %s", binName, err)
        }

        // Verify checksum
        if expectedSum, ok := checksums[binName]; ok {
            if err := verifyChecksum(path, expectedSum); err != nil {
                return fmt.Errorf("checksum verification failed for %s: %s", binName, err)
            }
            log.Info("checksum verified", "file", binName)
        }

        // Decompress and install
        destName := strings.TrimSuffix(binName, ".gz")
        destName = strings.TrimSuffix(destName, "-linux-amd64")
        if err := decompressAndInstall(path, filepath.Join(binDir, destName), log); err != nil {
            return fmt.Errorf("failed to install %s: %s", binName, err)
        }
    }

    // Save version info
    versionFile := filepath.Join(configDir, "github-version.txt")
    if err := os.WriteFile(versionFile, []byte(release.TagName), 0644); err != nil {
        log.Warn("failed to save version file", "err", err)
    }

    log.Info("update complete", "version", release.TagName)
    return nil
}

func parseChecksums(path string) (map[string]string, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }

    checksums := make(map[string]string)
    for _, line := range strings.Split(string(data), "\n") {
        parts := strings.Fields(line)
        if len(parts) == 2 {
            filename := strings.TrimPrefix(parts[1], "*")
            checksums[filename] = parts[0]
        }
    }
    return checksums, nil
}

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
```

### 5. Update install-source.json After Successful Update

After a successful update, update the install-source.json to record the new version:

```go
// Add to end of installGitHubUpdate function
func updateInstallSource(configDir string, release *ghrelease.Release, repo string) error {
    source := &installsource.InstallSource{
        Source:      installsource.SourceGitHub,
        Repository:  repo,
        Version:     release.TagName,
        InstalledAt: time.Now(),
    }
    return installsource.Save(configDir, source)
}
```

---

## Deployment Modes

### Mode 1: TUF/S3 Only (Current - Unchanged)
```bash
# Install using existing TUF repository
./script/install-flynn --repo https://dl.flynn.io
```

### Mode 2: GitHub Releases Only (New)
```bash
# Install using GitHub Releases
./script/install-flynn-github --repo flynn/flynn --version v2024.01.27.0
```

### Mode 3: Hybrid (Both Available)
```bash
# Install from TUF, but check GitHub for updates
./script/install-flynn --repo https://dl.flynn.io
flynn-host github-update --repo flynn/flynn --check
```

---

## Handling Container Images/Layers

Container image layers (squashfs files) can be:

**Option A: Bundle in release**
- Package all layers into `layers.tar.gz`
- Simple but may create large releases

**Option B: Use GitHub Container Registry (ghcr.io)**
- Push container images to ghcr.io
- Update manifests to reference ghcr.io URLs
- Better for large images

**Option C: Keep layers in separate storage**
- Use GitHub LFS or external storage for large layers
- Reference via URLs in manifests

---

## Environment Variables for CI

Required secrets for GitHub Actions:

| Secret | Description |
|--------|-------------|
| `GITHUB_TOKEN` | Auto-provided, for creating releases |
| `GHCR_TOKEN` | Optional: For pushing to GitHub Container Registry |

---

## Testing the Workflow

1. Create a test tag:
   ```bash
   git tag v2024.01.27.0-test
   git push origin v2024.01.27.0-test
   ```

2. Monitor the Actions tab for build progress

3. Verify the release artifacts are correct

4. Test installation:
   ```bash
   curl -fsSL https://github.com/your-org/flynn/releases/download/v2024.01.27.0-test/install-flynn | sudo bash
   ```

---

## Benefits of GitHub Releases (as Additional Channel)

1. **No infrastructure changes**: Existing TUF/S3 remains fully functional
2. **Redundancy**: Two distribution channels for reliability
3. **Built-in versioning**: GitHub handles release management
4. **CI/CD integration**: Native GitHub Actions support
5. **Download analytics**: GitHub provides download statistics
6. **CDN**: GitHub has built-in CDN for release downloads
7. **Easy migration path**: Users can switch channels at their own pace

---

## File Summary

New files to create:

| File | Purpose |
|------|---------|
| `.github/workflows/release.yml` | GitHub Actions workflow for building releases |
| `pkg/ghrelease/ghrelease.go` | GitHub Release API client |
| `pkg/installsource/installsource.go` | Installation source tracking |
| `host/cli/github_updater.go` | GitHub-specific update logic |
| `script/install-flynn-github` | Install script for GitHub Releases |
| `script/package-github-release` | Package artifacts for GitHub Release |

Files to modify (add new functionality, don't change existing):

| File | Changes |
|------|---------|
| `host/cli/update.go` | Add source detection and routing to GitHub or TUF updater |

---

## Update Command Usage

```bash
# Check for updates (uses detected installation source)
flynn-host update --check

# Update to latest version (uses detected installation source)
flynn-host update

# Update to specific version
flynn-host update --version v2024.01.27.0

# Force update even if already on latest
flynn-host update --force

# Override source (use GitHub even if installed via TUF)
flynn-host update --source github

# Override source (use TUF even if installed via GitHub)
flynn-host update --source tuf
```

---

## Implementation Order

1. **Phase 1**: Create `pkg/installsource` package
2. **Phase 2**: Create `pkg/ghrelease` package
3. **Phase 3**: Modify `host/cli/update.go` for source detection
4. **Phase 4**: Create `host/cli/github_updater.go`
5. **Phase 5**: Create GitHub Actions workflow
6. **Phase 6**: Create `install-flynn-github` script
7. **Phase 7**: Testing and documentation

