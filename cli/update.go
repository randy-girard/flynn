package main

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	cfg "github.com/flynn/flynn/cli/config"
	"github.com/flynn/flynn/pkg/random"
	"github.com/flynn/flynn/pkg/version"
	"github.com/kardianos/osext"
	"gopkg.in/inconshreveable/go-update.v0"
)

const (
	upcktimePath      = "cktime"
	defaultGitHubRepo = "flynn/flynn"
)

var updateDir = filepath.Join(cfg.Dir(), "update")
var updater = &Updater{}

func runUpdate() error {
	if version.Dev() {
		return errors.New("Dev builds don't support auto-updates")
	}
	return updater.update()
}

type Updater struct{}

func (u *Updater) backgroundRun() {
	if u == nil {
		return
	}
	if !u.wantUpdate() {
		return
	}
	self, err := osext.Executable()
	if err != nil {
		// fail update, couldn't figure out path to self
		return
	}
	// TODO(titanous): logger isn't on Windows. Replace with proper error reports.
	l := exec.Command("logger", "-tflynn")
	c := exec.Command(self, "update")
	if w, err := l.StdinPipe(); err == nil && l.Start() == nil {
		c.Stdout = w
		c.Stderr = w
	}
	c.Start()
}

func (u *Updater) wantUpdate() bool {
	path := filepath.Join(updateDir, upcktimePath)
	if version.Dev() || readTime(path).After(time.Now()) {
		return false
	}
	wait := 12*time.Hour + randDuration(8*time.Hour)
	return writeTime(path, time.Now().Add(wait))
}

func (u *Updater) update() error {
	up := update.New()
	if err := up.CanUpdate(); err != nil {
		return err
	}

	if err := os.MkdirAll(updateDir, 0755); err != nil {
		return err
	}

	// Get latest version from GitHub
	latestVersion, err := u.getLatestVersion()
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	if latestVersion == version.Release() {
		return nil
	}

	// Download and apply update
	plat := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	assetName := fmt.Sprintf("flynn-%s.gz", plat)
	assetURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s",
		defaultGitHubRepo, latestVersion, assetName)

	resp, err := http.Get(assetURL)
	if err != nil {
		return fmt.Errorf("failed to download update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download update: status %d", resp.StatusCode)
	}

	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	defer gr.Close()

	err, errRecover := up.FromStream(gr)
	if errRecover != nil {
		return fmt.Errorf("update and recovery errors: %q %q", err, errRecover)
	}
	if err != nil {
		return err
	}
	log.Printf("Updated %s -> %s.", version.Release(), latestVersion)
	return nil
}

// getLatestVersion fetches the latest release version from GitHub
func (u *Updater) getLatestVersion() (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", defaultGitHubRepo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "flynn-cli")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	// Simple JSON parsing for tag_name
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// Extract tag_name from JSON response
	// Format: "tag_name": "v1.2.3"
	var tagName string
	for i := 0; i < len(body)-12; i++ {
		if string(body[i:i+11]) == `"tag_name":` {
			// Find the opening quote
			j := i + 11
			for j < len(body) && body[j] != '"' {
				j++
			}
			j++ // skip opening quote
			// Find closing quote
			k := j
			for k < len(body) && body[k] != '"' {
				k++
			}
			tagName = string(body[j:k])
			break
		}
	}

	if tagName == "" {
		return "", errors.New("failed to parse release version from GitHub")
	}
	return tagName, nil
}

// returns a random duration in [0,n).
func randDuration(n time.Duration) time.Duration {
	return time.Duration(random.Math.Int63n(int64(n)))
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
	return os.WriteFile(path, []byte(t.Format(time.RFC3339)), 0644) == nil
}
