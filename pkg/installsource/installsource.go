// Package installsource tracks how Flynn was installed (GitHub vs TUF)
// to ensure updates use the same source as the original installation.
package installsource

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	// SourceGitHub indicates installation from GitHub Releases
	SourceGitHub = "github"
	// SourceTUF indicates installation from TUF repository
	SourceTUF = "tuf"

	// DefaultConfigDir is the default Flynn configuration directory
	DefaultConfigDir = "/etc/flynn"
	// SourceFileName is the name of the installation source file
	SourceFileName = "install-source.json"
)

// InstallSource records how Flynn was installed
type InstallSource struct {
	// Source is the installation source type ("github" or "tuf")
	Source string `json:"source"`
	// Repository is the source repository (GitHub owner/repo or TUF URL)
	Repository string `json:"repository"`
	// Version is the installed version
	Version string `json:"version"`
	// InstalledAt is when Flynn was installed
	InstalledAt time.Time `json:"installed_at"`
}

// Load reads the installation source from the config directory.
// Returns nil and an error if the file doesn't exist or can't be read.
func Load(configDir string) (*InstallSource, error) {
	if configDir == "" {
		configDir = DefaultConfigDir
	}
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

// Save writes the installation source to the config directory.
func Save(configDir string, source *InstallSource) error {
	if configDir == "" {
		configDir = DefaultConfigDir
	}

	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}

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

// NewGitHubSource creates an InstallSource for GitHub installations
func NewGitHubSource(repo, version string) *InstallSource {
	return &InstallSource{
		Source:      SourceGitHub,
		Repository:  repo,
		Version:     version,
		InstalledAt: time.Now(),
	}
}

// NewTUFSource creates an InstallSource for TUF installations
func NewTUFSource(repoURL, version string) *InstallSource {
	return &InstallSource{
		Source:      SourceTUF,
		Repository:  repoURL,
		Version:     version,
		InstalledAt: time.Now(),
	}
}

// GetSourceFilePath returns the full path to the install-source.json file
func GetSourceFilePath(configDir string) string {
	if configDir == "" {
		configDir = DefaultConfigDir
	}
	return filepath.Join(configDir, SourceFileName)
}

// Exists checks if the install-source.json file exists
func Exists(configDir string) bool {
	path := GetSourceFilePath(configDir)
	_, err := os.Stat(path)
	return err == nil
}

