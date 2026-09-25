package plugin

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"text/tabwriter"
)

//go:embed official-plugins.json
var officialPluginsJSON []byte

// KnownPlugin is one first-party plugin shipped in official-plugins.json.
// flynn-host plugin:install <name> uses Repo unless the operator passes a
// path, git URL, --github-org, or /etc/flynn/plugins.json override.
type KnownPlugin struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"`
	Kind        string   `json:"kind"`
	Repo        string   `json:"repo"`
	Description string   `json:"description"`
}

// PluginGroup is a named set of plugins installed together.
// plugin:install <name> installs each member. TenancyMode, when set, is the
// cluster tenancy mode applied after every member installs (hosted does not
// enable public signup).
type PluginGroup struct {
	Name        string   `json:"name"`
	Plugins     []string `json:"plugins"`
	TenancyMode string   `json:"tenancy_mode,omitempty"`
}

type officialFile struct {
	GitHubOrg      string          `json:"github_org"`
	Plugins        []KnownPlugin   `json:"plugins"`
	PrivatePlugins []PrivatePlugin `json:"private_plugins,omitempty"`
	PluginGroups   []PluginGroup   `json:"plugin_groups,omitempty"`
}

// PrivatePlugin is a first-party plugin that exists but is not in the public
// install catalog. It is listed for operators so they know the name, but
// plugin:install <name> will not resolve it.
type PrivatePlugin struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

var (
	officialOnce sync.Once
	official     officialFile
	officialErr  error
)

func mustOfficial() officialFile {
	officialOnce.Do(func() {
		official, officialErr = parseOfficial(officialPluginsJSON)
	})
	if officialErr != nil {
		panic("plugin: official-plugins.json: " + officialErr.Error())
	}
	return official
}

func parseOfficial(data []byte) (officialFile, error) {
	var f officialFile
	if err := json.Unmarshal(data, &f); err != nil {
		return f, err
	}
	f.GitHubOrg = strings.TrimSpace(f.GitHubOrg)
	seen := map[string]string{}
	for i := range f.Plugins {
		p := &f.Plugins[i]
		p.Name = strings.TrimSpace(p.Name)
		p.Kind = strings.TrimSpace(p.Kind)
		p.Repo = strings.TrimSpace(strings.TrimSuffix(p.Repo, ".git"))
		p.Description = strings.TrimSpace(p.Description)
		if p.Name == "" {
			return f, fmt.Errorf("plugins[%d]: name is required", i)
		}
		if p.Repo == "" {
			return f, fmt.Errorf("plugin %s: repo is required", p.Name)
		}
		if p.Description == "" {
			return f, fmt.Errorf("plugin %s: description is required", p.Name)
		}
		switch p.Kind {
		case KindResourceProvider, KindApp, KindScheduler:
		default:
			return f, fmt.Errorf("plugin %s: kind must be %q, %q, or %q", p.Name, KindResourceProvider, KindApp, KindScheduler)
		}
		var aliases []string
		for _, a := range p.Aliases {
			a = strings.TrimSpace(a)
			if a == "" || a == p.Name {
				continue
			}
			aliases = append(aliases, a)
		}
		p.Aliases = aliases
		for _, name := range p.Names() {
			if owner, ok := seen[name]; ok {
				return f, fmt.Errorf("duplicate plugin name %q (%s and %s)", name, owner, p.Name)
			}
			seen[name] = p.Name
		}
	}
	for i := range f.PrivatePlugins {
		p := &f.PrivatePlugins[i]
		p.Name = strings.TrimSpace(p.Name)
		p.Description = strings.TrimSpace(p.Description)
		if p.Name == "" {
			return f, fmt.Errorf("private_plugins[%d]: name is required", i)
		}
		if p.Description == "" {
			return f, fmt.Errorf("private plugin %s: description is required", p.Name)
		}
		if owner, ok := seen[p.Name]; ok {
			return f, fmt.Errorf("duplicate plugin name %q (%s and private %s)", p.Name, owner, p.Name)
		}
		seen[p.Name] = p.Name
	}
	groupNames := map[string]struct{}{}
	for i := range f.PluginGroups {
		g := &f.PluginGroups[i]
		g.Name = strings.TrimSpace(g.Name)
		g.TenancyMode = strings.TrimSpace(g.TenancyMode)
		if g.Name == "" {
			return f, fmt.Errorf("plugin_groups[%d]: name is required", i)
		}
		if _, ok := groupNames[g.Name]; ok {
			return f, fmt.Errorf("duplicate plugin group %q", g.Name)
		}
		groupNames[g.Name] = struct{}{}
		if _, ok := seen[g.Name]; ok {
			return f, fmt.Errorf("plugin group %q collides with a plugin name", g.Name)
		}
		if len(g.Plugins) == 0 {
			return f, fmt.Errorf("plugin group %s: plugins is required", g.Name)
		}
		if g.TenancyMode != "" && g.TenancyMode != "self_hosted" && g.TenancyMode != "hosted" {
			return f, fmt.Errorf("plugin group %s: tenancy_mode must be self_hosted or hosted", g.Name)
		}
		var members []string
		memberSeen := map[string]struct{}{}
		for _, name := range g.Plugins {
			name = strings.TrimSpace(name)
			if name == "" {
				return f, fmt.Errorf("plugin group %s: empty plugin name", g.Name)
			}
			if _, ok := seen[name]; !ok {
				return f, fmt.Errorf("plugin group %s: unknown plugin %q", g.Name, name)
			}
			if _, ok := memberSeen[name]; ok {
				return f, fmt.Errorf("plugin group %s: duplicate plugin %q", g.Name, name)
			}
			memberSeen[name] = struct{}{}
			members = append(members, name)
		}
		g.Plugins = members
	}
	return f, nil
}

// PluginGroups is the embedded install-group catalog.
func PluginGroups() []PluginGroup {
	groups := mustOfficial().PluginGroups
	out := make([]PluginGroup, len(groups))
	copy(out, groups)
	return out
}

// LookupGroup returns a plugin group by name (plugin:install hosted).
func LookupGroup(name string) (PluginGroup, bool) {
	name = strings.TrimSpace(name)
	for _, g := range mustOfficial().PluginGroups {
		if g.Name == name {
			return g, true
		}
	}
	return PluginGroup{}, false
}

// Names is the install aliases this catalog entry answers to.
func (p KnownPlugin) Names() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	add(p.Name)
	for _, a := range p.Aliases {
		add(a)
	}
	return out
}

// RepoSlug is owner/name for display and clone URLs.
func (p KnownPlugin) RepoSlug(org string) string {
	repo := strings.TrimSpace(p.Repo)
	if repo == "" {
		return ""
	}
	if strings.Contains(repo, "/") {
		return strings.TrimSuffix(repo, ".git")
	}
	org = strings.TrimSpace(org)
	if org == "" {
		org = DefaultGitHubOrg()
	}
	return org + "/" + repo
}

// KnownPlugins is the first-party catalog embedded in flynn-host.
func KnownPlugins() []KnownPlugin {
	plugins := mustOfficial().Plugins
	out := make([]KnownPlugin, len(plugins))
	copy(out, plugins)
	return out
}

// PrivatePlugins are first-party plugins that exist but are not installable
// from plugin:list --known / plugin:install <name>.
func PrivatePlugins() []PrivatePlugin {
	priv := mustOfficial().PrivatePlugins
	out := make([]PrivatePlugin, len(priv))
	copy(out, priv)
	return out
}

// IsPrivatePluginName is true when name is listed as private (not a catalog install alias).
func IsPrivatePluginName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, p := range PrivatePlugins() {
		if p.Name == name {
			return true
		}
	}
	return false
}

// PrivateCatalogError is returned when plugin:install is given a private plugin name.
type PrivateCatalogError struct {
	Name string
}

func (e *PrivateCatalogError) Error() string {
	if e == nil {
		return "private plugin is not in the public catalog"
	}
	return e.Name + " is a private first-party plugin. It is not in the public catalog (plugin:list --known) and cannot be installed with plugin:install " + e.Name
}

func officialGitHubOrg() string {
	return strings.TrimSpace(mustOfficial().GitHubOrg)
}

func officialAliases() map[string]Alias {
	out := map[string]Alias{}
	for _, p := range mustOfficial().Plugins {
		a := Alias{Repo: p.Repo}
		for _, name := range p.Names() {
			out[name] = a
		}
	}
	return out
}

// WriteKnownPlugins prints the official catalog (flynn-host plugin:list --known).
func WriteKnownPlugins(w io.Writer, org string, plugins []KnownPlugin) error {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tALIASES\tKIND\tREPO\tDESCRIPTION")
	for _, p := range plugins {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Name, strings.Join(p.Aliases, ", "), p.Kind, p.RepoSlug(org), p.Description)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	priv := PrivatePlugins()
	if len(priv) == 0 {
		return nil
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Private first-party plugins (not in this catalog; plugin:install <name> will not resolve them):")
	pt := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(pt, "NAME\tDESCRIPTION")
	for _, p := range priv {
		fmt.Fprintf(pt, "%s\t%s\n", p.Name, p.Description)
	}
	return pt.Flush()
}
