// Package ghrelease provides a client for interacting with GitHub Releases API
// to check for updates and download release assets.
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

	"github.com/inconshreveable/log15"
)

const (
	// GitHubAPIBase is the base URL for GitHub API
	GitHubAPIBase = "https://api.github.com"
	// UserAgent is the user agent string for API requests
	UserAgent = "flynn-updater"
	// DefaultTimeout is the default HTTP client timeout
	DefaultTimeout = 30 * time.Second
)

// Release represents a GitHub release
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Body        string    `json:"body"`
	Assets      []Asset   `json:"assets"`
}

// Asset represents a release asset (downloadable file)
type Asset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	ContentType        string `json:"content_type"`
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
		httpClient: &http.Client{Timeout: DefaultTimeout},
		log:        log,
	}
}

// GetLatestRelease fetches the latest release info
func (c *Client) GetLatestRelease() (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", GitHubAPIBase, c.repo)
	return c.getRelease(url)
}

// GetReleaseByTag fetches a specific release by tag
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
		return nil, fmt.Errorf("failed to fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var releases []Release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("failed to decode releases: %w", err)
	}
	return releases, nil
}

// CheckForUpdate compares current version with latest release
// Returns the latest release, whether an update is available, and any error
func (c *Client) CheckForUpdate(currentVersion string) (*Release, bool, error) {
	latest, err := c.GetLatestRelease()
	if err != nil {
		return nil, false, err
	}

	hasUpdate := CompareVersions(currentVersion, latest.TagName)
	return latest, hasUpdate, nil
}

// CompareVersions returns true if latestVersion is newer than currentVersion
func CompareVersions(currentVersion, latestVersion string) bool {
	// Strip 'v' prefix for comparison
	current := strings.TrimPrefix(currentVersion, "v")
	latest := strings.TrimPrefix(latestVersion, "v")

	// Simple string comparison works for date-based versions like "20240127.0"
	// For more complex versioning, consider using a semver library
	return latest > current
}

// DownloadAsset downloads a release asset to the specified directory
func (c *Client) DownloadAsset(asset *Asset, destDir string) (string, error) {
	destPath := filepath.Join(destDir, asset.Name)

	c.log.Info("downloading asset", "name", asset.Name, "size", asset.Size)

	resp, err := c.httpClient.Get(asset.BrowserDownloadURL)
	if err != nil {
		return "", fmt.Errorf("failed to download asset: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	defer out.Close()

	written, err := io.Copy(out, resp.Body)
	if err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	c.log.Info("downloaded asset", "name", asset.Name, "bytes", written)
	return destPath, nil
}

// GetAssetByName finds an asset by name in a release
func (r *Release) GetAssetByName(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// GetReleaseURL returns the download URL for a specific release
func GetReleaseURL(repo, version string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s", repo, version)
}

// getRelease is a helper to fetch a single release from a URL
func (c *Client) getRelease(url string) (*Release, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch release: %w", err)
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
		return nil, fmt.Errorf("failed to decode release: %w", err)
	}
	return &release, nil
}

// DownloadFile downloads a file from a URL to the specified path.
// It writes to a temporary file and atomically renames on success,
// so a partial download never appears at the final path.
func (c *Client) DownloadFile(url, destPath string) error {
	c.log.Info("downloading file", "url", url, "dest", destPath)

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write to a temp file in the same directory so os.Rename is atomic
	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".download-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpPath) // no-op if rename succeeded
	}()

	_, err = io.Copy(tmp, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	// Ensure data is flushed to disk before renaming
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}
