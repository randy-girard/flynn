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

// DefaultAliases map short names to sibling checkouts relative to PLUGIN_REPO_ROOT
// (default "..", i.e. next to the Flynn repo). This is a catalog, not installer
// logic: adding a plugin is an entry here plus a flynn-plugin.json in that repo.
// When the checkout is missing, Resolve falls back to GitHub
// github.com/<org>/<alias-repo>.
var DefaultAliases = map[string]string{
	"redis":      "flynn-plugin-redis",
	"mariadb":    "flynn-plugin-mariadb",
	"mysql":      "flynn-plugin-mariadb",
	"mongodb":    "flynn-plugin-mongodb",
	"kafka":      "flynn-plugin-kafka",
	"clickhouse": "flynn-plugin-clickhouse",
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
