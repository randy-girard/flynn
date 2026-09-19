package ghrelease

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// SkipUpdateCheckEnv disables GitHub upgrade notices when set to a
	// non-empty value (used by tests and scripted installs).
	SkipUpdateCheckEnv = "FLYNN_SKIP_UPDATE_CHECK"
	// DefaultNotifyTimeout bounds a single GitHub lookup so CLI commands
	// are not delayed when GitHub is slow or unreachable.
	DefaultNotifyTimeout = 3 * time.Second
	// DefaultNotifyInterval is how often MaybeNotify hits GitHub when
	// NotifyOptions.MinInterval is unset. Matches DefaultUpdateCheckTTL.
	DefaultNotifyInterval = DefaultUpdateCheckTTL
	defaultNotifyRepo     = "randy-girard/flynn"
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

// MaybeNotify writes a one-line upgrade hint to stderr (or opts.Writer) when
// GitHub has a newer published release than CurrentVersion.
//
// It runs on every flynn / flynn-host command except:
//   - FLYNN_SKIP_UPDATE_CHECK is set (tests and scripted installs)
//   - CurrentVersion is empty
//   - CurrentVersion contains "-smoke" (Vagrant smoke tarballs)
//   - GitHub latest is missing or not newer (already up to date)
//
// "dev" (unstamped local builds) is treated as older than any published tag.
// Lookups are cached on disk (see DefaultUpdateCheckCachePath) for
// DefaultUpdateCheckTTL; a cached newer tag is still printed on every call.
// Network and parse errors are ignored so CLI startup never fails.
func MaybeNotify(opts NotifyOptions) {
	if strings.TrimSpace(os.Getenv(SkipUpdateCheckEnv)) != "" {
		return
	}
	current := strings.TrimSpace(opts.CurrentVersion)
	if current == "" || strings.Contains(current, "-smoke") {
		return
	}
	w := opts.Writer
	if w == nil {
		w = os.Stderr
	}

	repo := strings.TrimSpace(opts.Repo)
	if repo == "" {
		if r := strings.TrimSpace(os.Getenv("FLYNN_GITHUB_REPO")); r != "" {
			repo = r
		} else {
			repo = defaultNotifyRepo
		}
	}

	client := NewClient(repo, nil)
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultNotifyTimeout}
	}
	client.SetHTTPClient(httpClient)
	client.APIBase = opts.APIBase
	if strings.TrimSpace(opts.CheckFile) != "" {
		client.CachePath = opts.CheckFile
	}
	if opts.MinInterval > 0 {
		ttl := opts.MinInterval
		client.TTL = &ttl
	}

	rel, hasUpdate, err := client.CheckForUpdate(current)
	if err != nil || !hasUpdate || rel == nil {
		return
	}
	latest := strings.TrimSpace(rel.TagName)
	if latest == "" {
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
	fmt.Fprintf(w, "A newer %s is available (%s; this is %s). Run `%s` to upgrade.\n", product, latest, current, upgradeCmd)
}

// shouldPrintUpdate reports whether latest is a published tag newer than current.
// "dev" is always older than a published tag. A -<git> suffix is ignored for
// comparison (v20260917.1-gabcdef compares as v20260917.1).
func shouldPrintUpdate(current, latest string) bool {
	current = strings.TrimSpace(current)
	latest = strings.TrimSpace(latest)
	if current == "" || latest == "" {
		return false
	}
	if current == "dev" {
		return true
	}
	if i := strings.Index(current, "-"); i >= 0 {
		current = current[:i]
	}
	return CompareVersions(current, latest)
}
