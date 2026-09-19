package ghrelease

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// UpdateCheckTTLEnv overrides the on-disk GitHub update-check TTL.
	// A Go duration (1h, 30m) or integer seconds is accepted. 0 always
	// refreshes (same as --check --force).
	UpdateCheckTTLEnv = "FLYNN_UPDATE_CHECK_TTL"
	// UpdateCheckCacheEnv overrides the cache file path.
	UpdateCheckCacheEnv = "FLYNN_UPDATE_CHECK_CACHE"
	// UpdateChannelEnv selects the release channel (stable or prerelease).
	UpdateChannelEnv = "FLYNN_UPDATE_CHANNEL"

	// ChannelStable is GitHub's latest non-prerelease release.
	ChannelStable = "stable"
	// ChannelPrerelease includes published prerelease tags.
	ChannelPrerelease = "prerelease"

	// DefaultUpdateCheckTTL is how long a cached lookup is reused.
	DefaultUpdateCheckTTL = time.Hour
)

// updateCheckCacheFile is the on-disk JSON document.
type updateCheckCacheFile struct {
	Entries map[string]updateCheckEntry `json:"entries"`
}

// updateCheckEntry is one cached GitHub lookup.
type updateCheckEntry struct {
	Repo           string    `json:"repo"`
	CurrentVersion string    `json:"current_version"`
	Channel        string    `json:"channel"`
	Release        *Release  `json:"release"`
	HasUpdate      bool      `json:"has_update"`
	CheckedAt      time.Time `json:"checked_at"`
}

// DefaultUpdateCheckCachePath is ~/.flynn/update-check-cache.json.
// FLYNN_UPDATE_CHECK_CACHE overrides it. If the home directory is unknown,
// $XDG_CACHE_HOME/flynn/update-check-cache.json (or the OS cache dir) is used.
func DefaultUpdateCheckCachePath() string {
	if p := strings.TrimSpace(os.Getenv(UpdateCheckCacheEnv)); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".flynn", "update-check-cache.json")
	}
	if cache, err := os.UserCacheDir(); err == nil && cache != "" {
		return filepath.Join(cache, "flynn", "update-check-cache.json")
	}
	return filepath.Join(os.TempDir(), "flynn-update-check-cache.json")
}

// UpdateCheckTTL is the cache lifetime from FLYNN_UPDATE_CHECK_TTL, or
// DefaultUpdateCheckTTL. A value of 0 means always refresh.
func UpdateCheckTTL() time.Duration {
	raw := strings.TrimSpace(os.Getenv(UpdateCheckTTLEnv))
	if raw == "" {
		return DefaultUpdateCheckTTL
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d < 0 {
			return DefaultUpdateCheckTTL
		}
		return d
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return DefaultUpdateCheckTTL
	}
	return time.Duration(n) * time.Second
}

// NormalizeChannel maps user/env input to stable or prerelease.
func NormalizeChannel(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ChannelPrerelease, "pre", "unstable", "preview":
		return ChannelPrerelease
	default:
		return ChannelStable
	}
}

// ChannelFromVersion returns the channel for a version string. FLYNN_UPDATE_CHANNEL
// wins; otherwise tags that look like rc/beta/alpha use the prerelease channel.
func ChannelFromVersion(currentVersion string) string {
	if raw := strings.TrimSpace(os.Getenv(UpdateChannelEnv)); raw != "" {
		return NormalizeChannel(raw)
	}
	lv := strings.ToLower(currentVersion)
	for _, p := range []string{"-rc", "-pre", "-beta", "-alpha"} {
		if strings.Contains(lv, p) {
			return ChannelPrerelease
		}
	}
	return ChannelStable
}

func updateCheckKey(repo, currentVersion, channel string) string {
	return strings.ToLower(strings.TrimSpace(repo)) + "|" + strings.TrimSpace(currentVersion) + "|" + NormalizeChannel(channel)
}

func (c *Client) cachePath() string {
	if c != nil && strings.TrimSpace(c.CachePath) != "" {
		return c.CachePath
	}
	return DefaultUpdateCheckCachePath()
}

func (c *Client) ttlDuration() time.Duration {
	if c != nil && c.TTL != nil {
		return *c.TTL
	}
	return UpdateCheckTTL()
}

func (c *Client) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) channel(currentVersion string) string {
	if c != nil && strings.TrimSpace(c.Channel) != "" {
		return NormalizeChannel(c.Channel)
	}
	return ChannelFromVersion(currentVersion)
}

func slimRelease(r *Release) *Release {
	if r == nil {
		return nil
	}
	return &Release{
		TagName:     r.TagName,
		Name:        r.Name,
		Draft:       r.Draft,
		Prerelease:  r.Prerelease,
		PublishedAt: r.PublishedAt,
	}
}

func loadUpdateCheckCache(path string) updateCheckCacheFile {
	if path == "" {
		return updateCheckCacheFile{Entries: map[string]updateCheckEntry{}}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return updateCheckCacheFile{Entries: map[string]updateCheckEntry{}}
	}
	var cache updateCheckCacheFile
	if json.Unmarshal(data, &cache) != nil || cache.Entries == nil {
		return updateCheckCacheFile{Entries: map[string]updateCheckEntry{}}
	}
	return cache
}

func saveUpdateCheckCache(path string, cache updateCheckCacheFile) {
	if path == "" {
		return
	}
	if cache.Entries == nil {
		cache.Entries = map[string]updateCheckEntry{}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".update-check-cache-*.json")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return
	}
	_ = tmp.Chmod(0600)
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return
	}
	ok = true
}
