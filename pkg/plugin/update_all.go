package plugin

import (
	"fmt"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
)

// OfficialInstalled returns installed plugins that are in the official catalog
// (by app name, aliases, provider, or CLI command).
func OfficialInstalled(apps []*ct.App) []Installed {
	out := []Installed{}
	for _, rec := range ListInstalled(apps) {
		if LookupOfficial(rec) != nil {
			out = append(out, rec)
		}
	}
	return out
}

// LookupOfficial finds the catalog entry for an installed plugin, or nil.
func LookupOfficial(rec Installed) *KnownPlugin {
	plugins := KnownPlugins()
	for i := range plugins {
		p := &plugins[i]
		for _, name := range p.Names() {
			if rec.MatchesName(name) {
				return p
			}
		}
		if rec.GitHubRepo != "" {
			repo := strings.TrimSuffix(strings.TrimSpace(rec.GitHubRepo), ".git")
			if repo == p.Repo || strings.HasSuffix(repo, "/"+p.Repo) {
				return p
			}
		}
	}
	return nil
}

// UpdateAll updates every installed plugin that has a resolvable GitHub
// source (official catalog, stamped github_repo, git Source URL, or the
// flynn-plugin-<name> convention). Individual failures are logged and the
// rest continue; a non-nil error is returned if any plugin failed.
func (in *Installer) UpdateAll(opts InstallOptions) error {
	if in == nil || in.Client == nil {
		return fmt.Errorf("missing controller client")
	}
	apps, err := in.Client.AppList()
	if err != nil {
		return fmt.Errorf("list apps: %w", err)
	}
	plugins := ListInstalled(apps)
	if len(plugins) == 0 {
		in.logf("no plugins installed")
		return nil
	}
	return runInstalledUpdates(plugins, opts, in.Update, in.logf)
}

func updateSourceForInstalled(p Installed) string {
	if LookupOfficial(p) != nil {
		if name := strings.TrimSpace(p.Name); name != "" {
			return name
		}
	}
	if repo := strings.TrimSpace(p.GitHubRepo); repo != "" {
		return repo
	}
	if src := strings.TrimSpace(p.Source); looksLikeGitURL(src) {
		return src
	}
	return strings.TrimSpace(p.Name)
}

func runInstalledUpdates(plugins []Installed, opts InstallOptions, update func(InstallOptions) error, logf func(string, ...interface{})) error {
	failed := 0
	for _, p := range plugins {
		name := updateSourceForInstalled(p)
		if name == "" {
			if logf != nil {
				logf("plugin %s: skipped: no update source", strings.TrimSpace(p.Name))
			}
			continue
		}
		err := update(InstallOptions{
			Source:              name,
			GitHubOrg:           opts.GitHubOrg,
			Cwd:                 opts.Cwd,
			AutoTLS:             opts.AutoTLS,
			PluginsFile:         opts.PluginsFile,
			CredsFile:           opts.CredsFile,
			AllowExternalLayers: opts.AllowExternalLayers,
			Yes:                 opts.Yes,
		})
		if err != nil {
			failed++
			if logf != nil {
				logf("plugin %s: failed: %s", name, err)
			}
			continue
		}
		if logf != nil {
			logf("plugin %s: updated", name)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d plugin(s) failed to update", failed, len(plugins))
	}
	return nil
}
