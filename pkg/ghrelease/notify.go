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
)

const (
	// SkipUpdateCheckEnv disables GitHub upgrade notices when set to a
	// non-empty value (used by tests and scripted installs).
	SkipUpdateCheckEnv = "FLYNN_SKIP_UPDATE_CHECK"
	// DefaultNotifyInterval is how often MaybeNotify hits GitHub.
	// A cached newer tag is still printed on every command.
	DefaultNotifyInterval = 12 * time.Hour
	// DefaultNotifyTimeout bounds a single GitHub lookup so CLI commands
	// are not delayed when GitHub is slow or unreachable.
	DefaultNotifyTimeout = 3 * time.Second
	defaultNotifyRepo    = "randy-girard/flynn"
)

// NotifyOptions controls MaybeNotify.
type NotifyOptions struct {
	Writer         io.Writer
	CurrentVersion string
	Product        string
	UpgradeCommand string
	Repo           string
	CheckFile      string
	HTTPClient     *http.Client
	APIBase        string
	MinInterval    time.Duration
}

type notifyCache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// MaybeNotify writes a one-line upgrade hint to opts.Writer when GitHub has a
// newer release than CurrentVersion. GitHub is polled at most once per
// MinInterval; if a newer tag is already cached it is printed on every call.
// Network and parse errors are ignored.
func MaybeNotify(opts NotifyOptions) {
	if strings.TrimSpace(os.Getenv(SkipUpdateCheckEnv)) != "" {
		return
	}
	current := strings.TrimSpace(opts.CurrentVersion)
	if current == "" || current == "dev" {
		return
	}
	if opts.Writer == nil {
		return
	}
	interval := opts.MinInterval
	if interval <= 0 {
		interval = DefaultNotifyInterval
	}

	cache := loadNotifyCache(opts.CheckFile)
	needFetch := opts.CheckFile == "" || cache.CheckedAt.IsZero() || time.Since(cache.CheckedAt) >= interval
	if needFetch {
		if latest, ok := fetchLatestTag(opts); ok {
			cache.Latest = latest
			cache.CheckedAt = time.Now().UTC()
			saveNotifyCache(opts.CheckFile, cache)
		} else if cache.Latest != "" {
			// Keep showing the last known newer tag; throttle retries.
			cache.CheckedAt = time.Now().UTC()
			saveNotifyCache(opts.CheckFile, cache)
		}
	}

	latest := strings.TrimSpace(cache.Latest)
	if latest == "" || !CompareVersions(current, latest) {
		return
	}
	product := strings.TrimSpace(opts.Product)
	if product == "" {
		product = "Flynn"
	}
	upgradeCmd := strings.TrimSpace(opts.UpgradeCommand)
	if upgradeCmd == "" {
		upgradeCmd = "flynn update"
	}
	fmt.Fprintf(opts.Writer, "A newer %s is available (%s; this is %s). Run `%s` to upgrade.\n", product, latest, current, upgradeCmd)
}

func fetchLatestTag(opts NotifyOptions) (string, bool) {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DefaultNotifyTimeout}
	}
	api := strings.TrimRight(opts.APIBase, "/")
	if api == "" {
		api = GitHubAPIBase
	}
	repo := strings.TrimSpace(opts.Repo)
	if repo == "" {
		if r := strings.TrimSpace(os.Getenv("FLYNN_GITHUB_REPO")); r != "" {
			repo = r
		} else {
			repo = defaultNotifyRepo
		}
	}
	req, err := http.NewRequest(http.MethodGet, api+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", UserAgent)
	if tok := githubNotifyToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", false
	}
	latest := strings.TrimSpace(release.TagName)
	if latest == "" {
		return "", false
	}
	return latest, true
}

func githubNotifyToken() string {
	for _, k := range []string{"FLYNN_GITHUB_TOKEN", "FLYNN_PLUGIN_GITHUB_TOKEN", "GITHUB_TOKEN"} {
		if t := strings.TrimSpace(os.Getenv(k)); t != "" {
			return t
		}
	}
	return ""
}

func loadNotifyCache(path string) notifyCache {
	if path == "" {
		return notifyCache{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return notifyCache{}
	}
	var c notifyCache
	if json.Unmarshal(data, &c) == nil && (!c.CheckedAt.IsZero() || strings.TrimSpace(c.Latest) != "") {
		return c
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data))); err == nil {
		return notifyCache{CheckedAt: t}
	}
	if info, err := os.Stat(path); err == nil {
		return notifyCache{CheckedAt: info.ModTime()}
	}
	return notifyCache{}
}

func saveNotifyCache(path string, c notifyCache) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(data, '\n'), 0644)
}
