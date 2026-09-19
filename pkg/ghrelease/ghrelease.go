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

	// APIBase overrides GitHubAPIBase (tests inject httptest servers).
	APIBase string
	// CachePath overrides DefaultUpdateCheckCachePath.
	CachePath string
	// Channel overrides ChannelFromVersion (stable or prerelease).
	Channel string
	// TTL overrides UpdateCheckTTL. A pointer so 0 can mean always refresh.
	TTL *time.Duration
	// Now overrides time.Now (tests freeze expiry).
	Now func() time.Time

	fetchLatest func(channel string) (*Release, error)
}

// NewClient creates a new GitHub Release client
func NewClient(repo string, log log15.Logger) *Client {
	return &Client{
		repo:       repo,
		httpClient: &http.Client{Timeout: DefaultTimeout},
		log:        log,
	}
}

// SetHTTPClient replaces the HTTP client (tests inject httptest clients).
func (c *Client) SetHTTPClient(h *http.Client) {
	if c == nil {
		return
	}
	c.httpClient = h
}

func (c *Client) api() string {
	if c != nil && strings.TrimSpace(c.APIBase) != "" {
		return strings.TrimRight(c.APIBase, "/")
	}
	return GitHubAPIBase
}

func (c *Client) http() *http.Client {
	if c != nil && c.httpClient != nil {
		return c.httpClient
	}
	return &http.Client{Timeout: DefaultTimeout}
}

// GetLatestRelease fetches the latest release info (uncached; used by install).
func (c *Client) GetLatestRelease() (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", c.api(), c.repo)
	return c.getRelease(url)
}

// GetReleaseByTag fetches a specific release by tag
func (c *Client) GetReleaseByTag(tag string) (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", c.api(), c.repo, tag)
	return c.getRelease(url)
}

// ListReleases fetches all releases (for channel support)
func (c *Client) ListReleases() ([]Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases", c.api(), c.repo)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	c.setAPIHeaders(req)

	resp, err := c.http().Do(req)
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

// CheckForUpdate compares current version with the latest release for the
// client's channel. Fresh cache hits do not hit the network. Expired or
// missing entries fetch, store, and return. Failures after a previous
// successful lookup return the stale entry so CLI startup notify stays quiet.
func (c *Client) CheckForUpdate(currentVersion string) (*Release, bool, error) {
	return c.CheckForUpdateForce(currentVersion, false)
}

// CheckForUpdateForce is CheckForUpdate, optionally bypassing a fresh cache.
// FLYNN_UPDATE_CHECK_TTL=0 is treated as force.
func (c *Client) CheckForUpdateForce(currentVersion string, force bool) (*Release, bool, error) {
	currentVersion = strings.TrimSpace(currentVersion)
	channel := c.channel(currentVersion)
	ttl := c.ttlDuration()
	if ttl == 0 {
		force = true
	}
	path := c.cachePath()
	key := updateCheckKey(c.repo, currentVersion, channel)
	cache := loadUpdateCheckCache(path)
	entry, hit := cache.Entries[key]
	now := c.now()
	if !force && hit && entry.Release != nil && !entry.CheckedAt.IsZero() && now.Sub(entry.CheckedAt) < ttl {
		return slimRelease(entry.Release), entry.HasUpdate, nil
	}

	latest, err := c.fetchLatestForChannel(channel)
	if err != nil {
		if !force && hit && entry.Release != nil {
			return slimRelease(entry.Release), entry.HasUpdate, nil
		}
		return nil, false, err
	}

	hasUpdate := shouldPrintUpdate(currentVersion, latest.TagName)
	if cache.Entries == nil {
		cache.Entries = map[string]updateCheckEntry{}
	}
	cache.Entries[key] = updateCheckEntry{
		Repo:           c.repo,
		CurrentVersion: currentVersion,
		Channel:        channel,
		Release:        slimRelease(latest),
		HasUpdate:      hasUpdate,
		CheckedAt:      now.UTC(),
	}
	saveUpdateCheckCache(path, cache)
	return latest, hasUpdate, nil
}

func (c *Client) fetchLatestForChannel(channel string) (*Release, error) {
	if c != nil && c.fetchLatest != nil {
		return c.fetchLatest(channel)
	}
	if channel == ChannelPrerelease {
		return c.latestIncludingPrerelease()
	}
	return c.GetLatestRelease()
}

func (c *Client) latestIncludingPrerelease() (*Release, error) {
	all, err := c.ListReleases()
	if err != nil {
		return nil, err
	}
	var best *Release
	for i := range all {
		r := &all[i]
		if r.Draft || strings.TrimSpace(r.TagName) == "" {
			continue
		}
		if best == nil || CompareVersions(best.TagName, r.TagName) {
			cp := *r
			best = &cp
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no published releases")
	}
	return best, nil
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

func (c *Client) setAPIHeaders(req *http.Request) {
	req.Header.Set("User-Agent", UserAgent)
	if tok := githubAPIToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

func githubAPIToken() string {
	for _, k := range []string{"FLYNN_GITHUB_TOKEN", "FLYNN_PLUGIN_GITHUB_TOKEN", "GITHUB_TOKEN"} {
		if t := strings.TrimSpace(os.Getenv(k)); t != "" {
			return t
		}
	}
	return ""
}

// getRelease is a helper to fetch a single release from a URL
func (c *Client) getRelease(url string) (*Release, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	c.setAPIHeaders(req)

	resp, err := c.http().Do(req)
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
