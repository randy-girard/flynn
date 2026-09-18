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

type officialFile struct {
	GitHubOrg string        `json:"github_org"`
	Plugins   []KnownPlugin `json:"plugins"`
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
		case KindResourceProvider, KindApp:
		default:
			return f, fmt.Errorf("plugin %s: kind must be %q or %q", p.Name, KindResourceProvider, KindApp)
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
	return f, nil
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
	return tw.Flush()
}
