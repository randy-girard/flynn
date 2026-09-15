package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	EnvGitHubOrg     = "FLYNN_PLUGIN_GITHUB_ORG"
	EnvFlynnRepo     = "FLYNN_GITHUB_REPO"
	defaultGitHubOrg = "randy-girard"
)

// Config is /etc/flynn/plugins.json: GitHub org/API plus per-plugin aliases.
// Values may be a local path, a git URL, or an object with url/path/ref/repo.
type Config struct {
	GitHubOrg string
	GitHubAPI string
	Aliases   map[string]Alias
}

// Alias is one install source. Path is a local checkout; URL is a git remote.
// Repo is "owner/name" when you do not want the default flynn-plugin-<alias>.
type Alias struct {
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
	Ref  string `json:"ref,omitempty"`
	Repo string `json:"repo,omitempty"`
}

func DefaultGitHubOrg() string {
	if v := strings.TrimSpace(os.Getenv(EnvGitHubOrg)); v != "" {
		return v
	}
	if repo := strings.TrimSpace(os.Getenv(EnvFlynnRepo)); repo != "" {
		if i := strings.Index(repo, "/"); i > 0 {
			return repo[:i]
		}
	}
	return defaultGitHubOrg
}

func pluginRepoRoot() string {
	root := os.Getenv(EnvPluginRepoRoot)
	if root == "" {
		root = ".."
	}
	return root
}

func defaultConfig() *Config {
	cfg := &Config{
		GitHubOrg: DefaultGitHubOrg(),
		Aliases:   make(map[string]Alias, len(DefaultAliases)),
	}
	root := pluginRepoRoot()
	for name, repo := range DefaultAliases {
		cfg.Aliases[name] = Alias{Path: filepath.Join(root, repo)}
	}
	return cfg
}

// LoadConfig reads pluginsFile (default /etc/flynn/plugins.json). A missing
// file is not an error: builtin aliases and FLYNN_PLUGIN_GITHUB_ORG still apply.
func LoadConfig(pluginsFile string) (*Config, error) {
	cfg := defaultConfig()
	if pluginsFile == "" {
		pluginsFile = DefaultPluginsFile
	}
	data, err := os.ReadFile(pluginsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", pluginsFile, err)
	}
	for k, v := range raw {
		switch k {
		case "github_org", "github.org":
			if err := json.Unmarshal(v, &cfg.GitHubOrg); err != nil {
				return nil, fmt.Errorf("%s: %s: %w", pluginsFile, k, err)
			}
			cfg.GitHubOrg = strings.TrimSpace(cfg.GitHubOrg)
		case "github_api", "github.api":
			if err := json.Unmarshal(v, &cfg.GitHubAPI); err != nil {
				return nil, fmt.Errorf("%s: %s: %w", pluginsFile, k, err)
			}
			cfg.GitHubAPI = strings.TrimSpace(cfg.GitHubAPI)
		default:
			a, err := parseAlias(v)
			if err != nil {
				return nil, fmt.Errorf("%s: plugin %s: %w", pluginsFile, k, err)
			}
			if existing, ok := cfg.Aliases[k]; ok {
				a = mergeAlias(existing, a)
			}
			cfg.Aliases[k] = a
		}
	}
	return cfg, nil
}

func parseAlias(v json.RawMessage) (Alias, error) {
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		s = strings.TrimSpace(s)
		if looksLikeGitURL(s) {
			return Alias{URL: s}, nil
		}
		return Alias{Path: s}, nil
	}
	var a Alias
	if err := json.Unmarshal(v, &a); err != nil {
		return Alias{}, err
	}
	a.Path = strings.TrimSpace(a.Path)
	a.URL = strings.TrimSpace(a.URL)
	a.Ref = strings.TrimSpace(a.Ref)
	a.Repo = strings.TrimSpace(a.Repo)
	return a, nil
}

func mergeAlias(base, over Alias) Alias {
	if over.Path != "" {
		base.Path = over.Path
	}
	if over.URL != "" {
		base.URL = over.URL
	}
	if over.Ref != "" {
		base.Ref = over.Ref
	}
	if over.Repo != "" {
		base.Repo = over.Repo
	}
	return base
}

func (c *Config) alias(name string) Alias {
	if c == nil || c.Aliases == nil {
		return Alias{}
	}
	return c.Aliases[name]
}

func (c *Config) gitHubOrg() string {
	if c != nil && strings.TrimSpace(c.GitHubOrg) != "" {
		return strings.TrimSpace(c.GitHubOrg)
	}
	return DefaultGitHubOrg()
}

// GitHubURL is the git remote for an alias when no local checkout exists.
func (c *Config) GitHubURL(name string) string {
	a := c.alias(name)
	if a.URL != "" {
		return a.URL
	}
	if a.Repo != "" {
		if strings.Contains(a.Repo, "/") {
			return "https://github.com/" + strings.TrimSuffix(a.Repo, ".git") + ".git"
		}
		return fmt.Sprintf("https://github.com/%s/%s.git", c.gitHubOrg(), a.Repo)
	}
	repo := DefaultAliases[name]
	if repo == "" {
		repo = "flynn-plugin-" + name
	}
	return fmt.Sprintf("https://github.com/%s/%s.git", c.gitHubOrg(), filepath.Base(repo))
}
