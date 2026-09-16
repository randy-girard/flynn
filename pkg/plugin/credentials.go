package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultCredentialsFile = "/etc/flynn/plugin-credentials.json"
	EnvGitHubToken         = "FLYNN_PLUGIN_GITHUB_TOKEN"
	EnvGitHubTokenAlt      = "GITHUB_TOKEN"
)

// HostCredentials is one git host (github.com or a GitHub Enterprise hostname).
type HostCredentials struct {
	Token string `json:"token"`
	API   string `json:"api,omitempty"`
}

type CredentialsFile map[string]HostCredentials

func credentialsPath(path string) string {
	if path != "" {
		return path
	}
	return DefaultCredentialsFile
}

func LoadCredentials(path string) (CredentialsFile, error) {
	path = credentialsPath(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CredentialsFile{}, nil
		}
		return nil, err
	}
	out := CredentialsFile{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return out, nil
}

func SaveCredentials(path string, creds CredentialsFile) error {
	path = credentialsPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0600)
}

// TokenForHost returns a GitHub token. Order: FLYNN_PLUGIN_GITHUB_TOKEN,
// GITHUB_TOKEN, then the credentials file for that hostname.
func TokenForHost(host, credsFile string) (string, string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "github.com"
	}
	if t := strings.TrimSpace(os.Getenv(EnvGitHubToken)); t != "" {
		return t, "", nil
	}
	if t := strings.TrimSpace(os.Getenv(EnvGitHubTokenAlt)); t != "" {
		return t, "", nil
	}
	creds, err := LoadCredentials(credsFile)
	if err != nil {
		return "", "", err
	}
	entry, ok := creds[host]
	if !ok {
		return "", "", nil
	}
	return strings.TrimSpace(entry.Token), strings.TrimSpace(entry.API), nil
}

func SetGitHubCredentials(path, host, token, api string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "github.com"
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("token is empty")
	}
	creds, err := LoadCredentials(path)
	if err != nil {
		return err
	}
	if creds == nil {
		creds = CredentialsFile{}
	}
	entry := creds[host]
	entry.Token = token
	if api != "" {
		entry.API = api
	}
	creds[host] = entry
	return SaveCredentials(path, creds)
}

func UnsetGitHubCredentials(path, host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "github.com"
	}
	creds, err := LoadCredentials(path)
	if err != nil {
		return err
	}
	if _, ok := creds[host]; !ok {
		return nil
	}
	delete(creds, host)
	return SaveCredentials(path, creds)
}

func CredentialsSet(path, host string) (bool, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "github.com"
	}
	if strings.TrimSpace(os.Getenv(EnvGitHubToken)) != "" || strings.TrimSpace(os.Getenv(EnvGitHubTokenAlt)) != "" {
		return true, nil
	}
	creds, err := LoadCredentials(path)
	if err != nil {
		return false, err
	}
	entry, ok := creds[host]
	return ok && strings.TrimSpace(entry.Token) != "", nil
}

func ReadToken(r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
