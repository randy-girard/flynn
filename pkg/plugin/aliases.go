package plugin

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultPluginsFile = "/etc/flynn/plugins.json"
	EnvPluginRepoRoot  = "PLUGIN_REPO_ROOT"
)

// DiscoverLocalPlugins scans root for checkouts that contain flynn-plugin.json
// and returns install aliases from each manifest (name, provider, CLI command,
// and aliases). Sibling checkouts override the official catalog path/repo.
func DiscoverLocalPlugins(root string) map[string]Alias {
	out := map[string]Alias{}
	if root == "" {
		return out
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return out
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(abs, e.Name())
		m, err := LoadManifest(dir)
		if err != nil {
			continue
		}
		repo := e.Name()
		if r := strings.TrimSpace(m.GitHubRepo); r != "" {
			repo = r
		}
		a := Alias{Path: dir, Repo: repo}
		for _, name := range m.aliasNames() {
			out[name] = a
		}
	}
	return out
}

func mergeInstalledAliases(aliases map[string]Alias, installed []Installed) {
	for _, p := range installed {
		repo := strings.TrimSpace(p.GitHubRepo)
		if repo == "" && p.Name != "" {
			repo = "flynn-plugin-" + p.Name
		}
		for _, name := range p.ResolveNames() {
			existing := aliases[name]
			if repo != "" {
				existing.Repo = repo
			}
			aliases[name] = existing
		}
	}
}

// ResolveSource turns a CLI argument into a local plugin directory.
// GitHub remotes are resolved by Resolve / Install, not this helper.
func ResolveSource(source, cwd, pluginsFile string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", os.ErrInvalid
	}
	r, err := Resolve(InstallOptions{Source: source, Cwd: cwd, PluginsFile: pluginsFile})
	if err != nil {
		return "", err
	}
	if r.Dir != "" {
		return r.Dir, nil
	}
	tried := source
	if r.GitHub != nil {
		tried = r.GitHub.URL
	}
	return "", &NotFoundError{Source: source, Tried: tried}
}

func existingDir(p, cwd string) (string, bool) {
	if !filepath.IsAbs(p) && cwd != "" {
		p = filepath.Join(cwd, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(abs, ManifestName)); err != nil {
		return "", false
	}
	return abs, true
}

type NotFoundError struct {
	Source string
	Tried  string
}

func (e *NotFoundError) Error() string {
	return "plugin " + e.Source + " not found at " + e.Tried + " (local checkout with " + ManifestName + ", or a GitHub URL / alias with --ref)"
}
