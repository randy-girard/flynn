package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/flynn/go-docopt"
	"github.com/kardianos/osext"
	cfg "github.com/randy-girard/flynn/cli/config"
	"github.com/randy-girard/flynn/pkg/ghrelease"
	"github.com/randy-girard/flynn/pkg/version"
	"gopkg.in/inconshreveable/go-update.v0"
)

const (
	upcktimePath      = "cktime"
	defaultGitHubRepo = "randy-girard/flynn"
	updateTimeout     = 5 * time.Minute
)

var updateDir = filepath.Join(cfg.Dir(), "update")
var updater = &Updater{}

func init() {
	const body = `
Download the latest Flynn CLI from GitHub Releases and replace this binary.
Alias: flynn upgrade.

Options:
	--check           Show whether an update is available without installing
	--version=<tag>   Install this release tag instead of latest
`
	register("update", runUpdate, "usage: flynn update [--check] [--version=<tag>]\n"+body)
	register("upgrade", runUpdate, "usage: flynn upgrade [--check] [--version=<tag>]\n"+body)
}

func runUpdate(args *docopt.Args) error {
	return updater.run(updateOptions{
		Check:   args.Bool["--check"],
		Version: strings.TrimSpace(args.String["--version"]),
	})
}

type updateOptions struct {
	Check   bool
	Version string
}

type Updater struct {
	HTTP *http.Client
	Repo string
	API  string
	DL   string
	// Apply, if set, replaces the default self-replace step (tests inject a stub).
	Apply func(io.Reader) error
}

func (u *Updater) httpClient() *http.Client {
	if u != nil && u.HTTP != nil {
		return u.HTTP
	}
	return &http.Client{Timeout: updateTimeout}
}

func (u *Updater) repo() string {
	if u != nil && strings.TrimSpace(u.Repo) != "" {
		return strings.TrimSpace(u.Repo)
	}
	if r := strings.TrimSpace(os.Getenv("FLYNN_GITHUB_REPO")); r != "" {
		return r
	}
	return defaultGitHubRepo
}

func (u *Updater) apiBase() string {
	if u != nil && strings.TrimSpace(u.API) != "" {
		return strings.TrimRight(u.API, "/")
	}
	return "https://api.github.com"
}

func (u *Updater) dlBase() string {
	if u != nil && strings.TrimSpace(u.DL) != "" {
		return strings.TrimRight(u.DL, "/")
	}
	return "https://github.com"
}

func (u *Updater) notifyIfUpdateAvailable() {
	client := u.httpClient()
	notifyClient := *client
	if notifyClient.Timeout == 0 || notifyClient.Timeout > ghrelease.DefaultNotifyTimeout {
		notifyClient.Timeout = ghrelease.DefaultNotifyTimeout
	}
	ghrelease.MaybeNotify(ghrelease.NotifyOptions{
		Writer:         os.Stderr,
		CurrentVersion: version.String(),
		Product:        "Flynn CLI",
		UpgradeCommand: "flynn update",
		Repo:           u.repo(),
		CheckFile:      filepath.Join(updateDir, upcktimePath),
		HTTPClient:     &notifyClient,
		APIBase:        u.apiBase(),
	})
}

func (u *Updater) run(opts updateOptions) error {
	tag := opts.Version
	if tag == "" {
		var err error
		tag, err = u.latestTag()
		if err != nil {
			return err
		}
	}
	current := version.Release()
	if !cliNeedsUpdate(current, tag) {
		fmt.Printf("already up to date (%s)\n", tag)
		return nil
	}
	if opts.Check {
		fmt.Printf("update available: %s -> %s\n", current, tag)
		return nil
	}

	apply := u.Apply
	if apply == nil {
		up := update.New()
		if err := up.CanUpdate(); err != nil {
			return fmt.Errorf("cannot replace this binary (%s); try sudo flynn update: %w", selfPath(), err)
		}
		apply = applyCLIUpdate
	}

	asset := cliAssetName(runtime.GOOS, runtime.GOARCH)
	gz, err := u.downloadAsset(tag, asset)
	if err != nil {
		return err
	}
	sums, err := u.downloadChecksums(tag)
	if err != nil {
		return err
	}
	want, ok := sums[asset]
	if !ok {
		return fmt.Errorf("checksums.sha512 has no entry for %s", asset)
	}
	if err := verifySHA512(gz, want); err != nil {
		return err
	}

	gr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return fmt.Errorf("decompress %s: %w", asset, err)
	}
	defer gr.Close()

	if err := apply(gr); err != nil {
		return err
	}
	fmt.Printf("Updated %s -> %s.\n", current, tag)
	return nil
}

func cliNeedsUpdate(current, latest string) bool {
	current = strings.TrimSpace(current)
	latest = strings.TrimSpace(latest)
	if latest == "" {
		return false
	}
	return current != latest
}

func cliAssetName(goos, goarch string) string {
	if goos == "windows" {
		return fmt.Sprintf("flynn-%s-%s.exe.gz", goos, goarch)
	}
	return fmt.Sprintf("flynn-%s-%s.gz", goos, goarch)
}

type githubLatest struct {
	TagName string `json:"tag_name"`
}

func (u *Updater) latestTag() (string, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", u.apiBase(), u.repo())
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	u.setGitHubHeaders(req)
	res, err := u.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("check GitHub releases: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		hint := "check your network"
		if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			hint = "published releases only; set GITHUB_TOKEN if you are rate-limited"
		}
		return "", fmt.Errorf("GitHub latest release: %s (%s)", res.Status, hint)
	}
	var rel githubLatest
	if err := json.Unmarshal(body, &rel); err != nil || strings.TrimSpace(rel.TagName) == "" {
		return "", errors.New("failed to parse release version from GitHub")
	}
	return strings.TrimSpace(rel.TagName), nil
}

func (u *Updater) downloadChecksums(tag string) (map[string]string, error) {
	url := fmt.Sprintf("%s/%s/releases/download/%s/checksums.sha512", u.dlBase(), u.repo(), tag)
	data, err := u.getBytes(url)
	if err != nil {
		return nil, fmt.Errorf("download checksums.sha512: %w", err)
	}
	sums := parseSHA512Sums(data)
	if len(sums) == 0 {
		return nil, errors.New("checksums.sha512 is empty")
	}
	return sums, nil
}

func (u *Updater) downloadAsset(tag, name string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s/releases/download/%s/%s", u.dlBase(), u.repo(), tag, name)
	data, err := u.getBytes(url)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	return data, nil
}

func (u *Updater) getBytes(rawURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	u.setGitHubHeaders(req)
	res, err := u.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, 1<<30))
}

func (u *Updater) setGitHubHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "flynn-cli")
	if tok := githubUpdateToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

func githubUpdateToken() string {
	for _, k := range []string{"FLYNN_GITHUB_TOKEN", "FLYNN_PLUGIN_GITHUB_TOKEN", "GITHUB_TOKEN"} {
		if t := strings.TrimSpace(os.Getenv(k)); t != "" {
			return t
		}
	}
	return ""
}

func parseSHA512Sums(data []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimPrefix(parts[1], "*")
		name = strings.TrimPrefix(name, "./")
		out[name] = parts[0]
	}
	return out
}

func verifySHA512(data []byte, want string) error {
	sum := sha512.Sum512(data)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, strings.TrimSpace(want)) {
		return fmt.Errorf("checksum mismatch: got %s", got)
	}
	return nil
}

func applyCLIUpdate(r io.Reader) error {
	up := update.New()
	err, errRecover := up.FromStream(r)
	if errRecover != nil {
		return fmt.Errorf("update and recovery errors: %q %q", err, errRecover)
	}
	return err
}

func selfPath() string {
	p, err := osext.Executable()
	if err != nil {
		return "this flynn binary"
	}
	return p
}

func readTime(path string) time.Time {
	p, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return time.Time{}
	}
	if err != nil {
		return time.Now().Add(1000 * time.Hour)
	}
	t, err := time.Parse(time.RFC3339, string(p))
	if err != nil {
		return time.Now().Add(1000 * time.Hour)
	}
	return t
}

func writeTime(path string, t time.Time) bool {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false
	}
	return os.WriteFile(path, []byte(t.Format(time.RFC3339)), 0644) == nil
}
