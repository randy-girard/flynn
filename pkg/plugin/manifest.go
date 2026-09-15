// Package plugin implements the Flynn plugin contract: flynn-plugin.json,
// local/git install, and the cluster CLI catalog. It is not specific to any
// one appliance (Redis, MariaDB, …); each plugin is a git repo with a manifest.
package plugin

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	ct "github.com/flynn/flynn/controller/types"
)

const (
	KindResourceProvider = "resource-provider"
	KindApp              = "app"

	ManifestName = "flynn-plugin.json"

	MetaSystemApp    = "flynn-system-app"
	MetaPlugin       = "flynn-plugin"
	MetaDatastore    = "flynn-datastore"
	MetaPluginCLI    = "flynn-plugin-cli"
	MetaPluginKind   = "flynn-plugin-kind"
	MetaPluginSource = "flynn-plugin-source"
	MetaPluginRef    = "flynn-plugin-ref"
	MetaPluginWait   = "flynn-plugin-wait"
	MetaPluginRecord = "flynn-plugin-record"

	// ImageSelf in image_env means "the artifact ID of this plugin's image".
	ImageSelf = "self"
)

// Manifest is flynn-plugin.json at the root of a plugin repo.
type Manifest struct {
	Name           string            `json:"name"`
	Kind           string            `json:"kind"`
	Provider       *Provider         `json:"provider,omitempty"`
	App            AppSpec           `json:"app"`
	InjectEnv      []string          `json:"inject_env,omitempty"`
	ImageEnv       map[string]string `json:"image_env,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	GenerateEnv    []string          `json:"generate_env,omitempty"`
	CLI            *CLI              `json:"cli,omitempty"`
	Hooks          *Hooks            `json:"hooks,omitempty"`
	Wait           string            `json:"wait,omitempty"`
	Aliases        []string          `json:"aliases,omitempty"`
	GitHubRepo     string            `json:"github_repo,omitempty"`
	ClusterBackup  *BackupSpec       `json:"cluster_backup,omitempty"`
	ClusterRestore *RestoreSpec      `json:"cluster_restore,omitempty"`
	Status         *StatusSpec       `json:"status,omitempty"`
	Build          json.RawMessage   `json:"build,omitempty"`
	Artifacts      *Artifacts        `json:"artifacts,omitempty"`
}

type Provider struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type AppSpec struct {
	Name          string                    `json:"name"`
	Strategy      string                    `json:"strategy,omitempty"`
	DeployTimeout int32                     `json:"deploy_timeout,omitempty"`
	Meta          map[string]string         `json:"meta,omitempty"`
	Processes     map[string]ct.ProcessType `json:"processes,omitempty"`
	// Scale is the formation for each process. Missing keys use the cluster
	// singleton web count (1 or 2). Use 0 for sirenia data processes that
	// stay down until the first resource provision.
	Scale map[string]int `json:"scale,omitempty"`
}

// CLI is the user-facing flynn command published by a plugin. The flynn binary
// does not compile plugin handlers; after install it reads this block from the
// cluster (app meta flynn-plugin-cli) and runs matching actions as controller
// jobs against the plugin/resource release image.
type CLI struct {
	Command     string   `json:"command"`
	Usage       string   `json:"usage,omitempty"`
	Subcommands []string `json:"subcommands,omitempty"`

	// App is the plugin system app name. catalogFrom fills this from the
	// controller app; it is not required in flynn-plugin.json.
	App string `json:"app,omitempty"`

	// Doc is the full docopt usage string (including "usage:" lines).
	Doc string `json:"doc,omitempty"`

	// ResourceEnv, when set, is an env key on the current app release whose
	// value is the provisioned appliance app (e.g. FLYNN_REDIS).
	ResourceEnv     string `json:"resource_env,omitempty"`
	ResourceMissing string `json:"resource_missing,omitempty"`

	Actions []CLIAction `json:"actions,omitempty"`
}

// CLIAction is one docopt command delegated to a cluster job.
type CLIAction struct {
	Name       string            `json:"name"`
	Args       []string          `json:"args"`
	Append     string            `json:"append,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	StdoutFile string            `json:"stdout_file,omitempty"`
	StdinFile  string            `json:"stdin_file,omitempty"`
	Progress   bool              `json:"progress,omitempty"`
	Quiet      string            `json:"quiet,omitempty"`
	// Passthrough appends the user argv after the plugin command (so
	// `flynn kafka topics create x` becomes job args + "topics create x").
	Passthrough bool `json:"passthrough,omitempty"`
	// ReleaseEnv copies the appliance release env into the job (TLS material).
	ReleaseEnv bool `json:"release_env,omitempty"`
	// Data requests a data volume on the job (mongodb restore).
	Data bool `json:"data,omitempty"`
}

// MatchAction returns the action whose Name tokens are all true in docopt
// bools, preferring the longest match (`topics create` over `topics`).
func (c *CLI) MatchAction(bools map[string]bool) *CLIAction {
	if c == nil {
		return nil
	}
	var best *CLIAction
	bestN := -1
	for i := range c.Actions {
		a := &c.Actions[i]
		parts := strings.Fields(a.Name)
		if len(parts) == 0 {
			continue
		}
		ok := true
		for _, p := range parts {
			if !bools[p] {
				ok = false
				break
			}
		}
		if ok && len(parts) > bestN {
			best = a
			bestN = len(parts)
		}
	}
	return best
}

func (c *CLI) Runnable() bool {
	return c != nil && c.Command != "" && strings.TrimSpace(c.Doc) != "" && len(c.Actions) > 0
}

func (c *CLI) Action(name string) *CLIAction {
	if c == nil {
		return nil
	}
	for i := range c.Actions {
		if c.Actions[i].Name == name {
			return &c.Actions[i]
		}
	}
	return nil
}

type Hooks struct {
	Install   string `json:"install,omitempty"`
	Upgrade   string `json:"upgrade,omitempty"`
	Uninstall string `json:"uninstall,omitempty"`
}

type Artifacts struct {
	Image string `json:"image,omitempty"`
}

// LoadManifest reads and validates flynn-plugin.json from a plugin checkout.
func LoadManifest(root string) (*Manifest, error) {
	path := filepath.Join(root, ManifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	m := &Manifest{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manifest) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("%s: name is required", ManifestName)
	}
	switch m.Kind {
	case KindResourceProvider, KindApp:
	default:
		return fmt.Errorf("%s: kind must be %q or %q", ManifestName, KindResourceProvider, KindApp)
	}
	if strings.TrimSpace(m.App.Name) == "" {
		m.App.Name = m.Name
	}
	if len(m.App.Processes) == 0 {
		return fmt.Errorf("%s: app.processes is required", ManifestName)
	}
	if m.Kind == KindResourceProvider {
		if m.Provider == nil || m.Provider.Name == "" || m.Provider.URL == "" {
			return fmt.Errorf("%s: kind %q requires provider.name and provider.url", ManifestName, KindResourceProvider)
		}
	}
	return nil
}

func (m *Manifest) AppMeta() map[string]string {
	meta := map[string]string{
		MetaSystemApp:  "true",
		MetaPlugin:     "true",
		MetaPluginKind: m.Kind,
	}
	for k, v := range m.App.Meta {
		meta[k] = v
	}
	meta[MetaSystemApp] = "true"
	meta[MetaPlugin] = "true"
	if m.CLI != nil {
		raw, err := json.Marshal(m.CLI)
		if err == nil {
			meta[MetaPluginCLI] = string(raw)
		}
	}
	if rec, err := json.Marshal(m.Record()); err == nil {
		meta[MetaPluginRecord] = string(rec)
	}
	return meta
}

// Record is the install inventory entry derived from this manifest.
func (m *Manifest) Record() Installed {
	if m == nil {
		return Installed{}
	}
	rec := Installed{
		Name:            m.App.Name,
		Kind:            m.Kind,
		Aliases:         m.aliasNames(),
		GitHubRepo:      m.githubRepo(),
		Backup:          m.ClusterBackup,
		Restore:         m.ClusterRestore,
		Status:          m.Status,
		Datastore:       m.App.Meta[MetaDatastore] == "true",
		Sirenia:         m.App.Strategy == "sirenia" || m.Env["SIRENIA_PROCESS"] != "",
		SireniaOptional: m.sireniaOptional(),
		Wait:            m.PingURL(),
		CLI:             m.CLI,
	}
	if rec.Name == "" {
		rec.Name = m.Name
	}
	if m.Provider != nil {
		rec.Provider = m.Provider.Name
	}
	return rec
}

func (m *Manifest) githubRepo() string {
	if r := strings.TrimSpace(m.GitHubRepo); r != "" {
		return r
	}
	if m.Name != "" {
		return "flynn-plugin-" + m.Name
	}
	return ""
}

func (m *Manifest) aliasNames() []string {
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
	add(m.Name)
	add(m.App.Name)
	if m.Provider != nil {
		add(m.Provider.Name)
	}
	if m.CLI != nil {
		add(m.CLI.Command)
	}
	for _, a := range m.Aliases {
		add(a)
	}
	return out
}

func (m *Manifest) sireniaOptional() bool {
	if m.App.Strategy != "sirenia" && m.Env["SIRENIA_PROCESS"] == "" {
		return false
	}
	proc := m.Env["SIRENIA_PROCESS"]
	if proc == "" {
		proc = m.App.Name
	}
	if proc == "" {
		proc = m.Name
	}
	n, ok := m.App.Scale[proc]
	return ok && n == 0
}

// AnnotateInstall records how this plugin was installed so cluster backup can
// list it. source is the operator argument (path, alias, or git URL).
func (m *Manifest) AnnotateInstall(meta map[string]string, source, ref string) map[string]string {
	if meta == nil {
		meta = m.AppMeta()
	}
	if source != "" {
		meta[MetaPluginSource] = source
	}
	if ref != "" {
		meta[MetaPluginRef] = ref
	}
	if ping := m.PingURL(); ping != "" {
		meta[MetaPluginWait] = ping
	}
	return meta
}

// PingURL is the HTTP URL flynn-host waits on after deploy. Manifest wait
// wins; otherwise resource-providers use http://<provider-host>/ping.
func (m *Manifest) PingURL() string {
	if m.Wait != "" {
		return m.Wait
	}
	if m.Provider == nil || m.Provider.URL == "" {
		return ""
	}
	u, err := url.Parse(m.Provider.URL)
	if err != nil || u.Host == "" {
		return ""
	}
	u.Path = "/ping"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
