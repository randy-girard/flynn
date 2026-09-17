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

// MaybeNotify writes a one-line upgrade hint to opts.Writer when GitHub has a
// newer release than CurrentVersion. Network and parse errors are ignored.
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
	if opts.CheckFile != "" {
		if info, err := os.Stat(opts.CheckFile); err == nil && time.Since(info.ModTime()) < interval {
			return
		}
		if err := os.MkdirAll(filepath.Dir(opts.CheckFile), 0755); err == nil {
			_ = os.WriteFile(opts.CheckFile, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0644)
		}
	}

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
	product := strings.TrimSpace(opts.Product)
	if product == "" {
		product = "Flynn"
	}
	upgradeCmd := strings.TrimSpace(opts.UpgradeCommand)
	if upgradeCmd == "" {
		upgradeCmd = "flynn update"
	}

	req, err := http.NewRequest(http.MethodGet, api+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return
	}
	latest := strings.TrimSpace(release.TagName)
	if latest == "" || !CompareVersions(current, latest) {
		return
	}
	fmt.Fprintf(opts.Writer, "A newer %s is available (%s; this is %s). Run `%s` to upgrade.\n", product, latest, current, upgradeCmd)
}
