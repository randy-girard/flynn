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
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Provider    *Provider         `json:"provider,omitempty"`
	App         AppSpec           `json:"app"`
	InjectEnv   []string          `json:"inject_env,omitempty"`
	ImageEnv    map[string]string `json:"image_env,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	GenerateEnv []string          `json:"generate_env,omitempty"`
	CLI         *CLI              `json:"cli,omitempty"`
	Hooks       *Hooks            `json:"hooks,omitempty"`
	Wait        string            `json:"wait,omitempty"`
	// Setup is operator questions asked during flynn-host plugin install
	// when stdin is a TTY. Non-interactive installs use Default, Generate,
	// existing env, or FLYNN_PLUGIN_SETUP_<ENV>.
	Setup []SetupPrompt `json:"setup,omitempty"`
	// Resources are provider names (e.g. "postgres") attached on first
	// install. Their env (DATABASE_URL, …) is merged into the plugin release.
	Resources []string `json:"resources,omitempty"`
	// Routes are HTTP/TCP routes created after deploy. Domain may use
	// ${CLUSTER_DOMAIN}.
	Routes []RouteSpec `json:"routes,omitempty"`
	// Webhooks are registered on every flynn-host after deploy (same API as
	// `flynn-host webhooks add`). URL and header values expand ${KEY} from
	// cluster + release env. Flynn does not special-case plugin names.
	Webhooks       []WebhookSpec   `json:"webhooks,omitempty"`
	Aliases        []string        `json:"aliases,omitempty"`
	GitHubRepo     string          `json:"github_repo,omitempty"`
	ClusterBackup  *BackupSpec     `json:"cluster_backup,omitempty"`
	ClusterRestore *RestoreSpec    `json:"cluster_restore,omitempty"`
	Status         *StatusSpec     `json:"status,omitempty"`
	Build          json.RawMessage `json:"build,omitempty"`
	Artifacts      *Artifacts      `json:"artifacts,omitempty"`
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
// cluster (app meta flynn-plugin-cli). Actions with Args run as controller
// jobs against the plugin/resource image. Actions with Flynn run a built-in
// laptop command against the plugin app (flynn -a <app> route …).
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

// CLIAction is one docopt command. Cluster jobs set Args (and run in the
// plugin/resource image). Flynn names a compiled-in laptop command
// (`route`, `env`, …) run as `flynn -a <plugin-app> <flynn> …`.
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
	// Flynn, if set, runs that built-in flynn command against the plugin
	// app instead of starting a cluster job. Mutually exclusive with Args.
	Flynn string `json:"flynn,omitempty"`
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

// MatchFlynnDelegate reports a compiled-in flynn command the user invoked as
// `flynn <plugin> <action> …` (for example `flynn dashboard route add http`).
func (c *CLI) MatchFlynnDelegate(args []string) (*CLIAction, []string, bool) {
	if c == nil || len(args) == 0 {
		return nil, nil, false
	}
	var best *CLIAction
	bestN := -1
	for i := range c.Actions {
		a := &c.Actions[i]
		if strings.TrimSpace(a.Flynn) == "" {
			continue
		}
		parts := strings.Fields(a.Name)
		if len(parts) == 0 {
			parts = strings.Fields(a.Flynn)
		}
		if len(parts) == 0 || len(args) < len(parts) {
			continue
		}
		ok := true
		for j, p := range parts {
			if args[j] != p {
				ok = false
				break
			}
		}
		if ok && len(parts) > bestN {
			best = a
			bestN = len(parts)
		}
	}
	if best == nil {
		return nil, nil, false
	}
	return best, args[bestN:], true
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

// SetupPrompt is one install-time question whose answer becomes release env.
type SetupPrompt struct {
	Env      string `json:"env"`
	Prompt   string `json:"prompt"`
	Default  string `json:"default,omitempty"`
	Secret   bool   `json:"secret,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	Generate bool   `json:"generate,omitempty"`
}

// RouteSpec is a cluster route created from the plugin manifest.
type RouteSpec struct {
	Type    string `json:"type"`
	Domain  string `json:"domain,omitempty"`
	Service string `json:"service"`
	Leader  bool   `json:"leader,omitempty"`
	// AutoTLS requests Let's Encrypt on this HTTP route (same as
	// `flynn route add http --auto-tls`). Cluster ACME must already be
	// configured; otherwise install logs a warning and leaves the route
	// HTTP-only unless the operator passed --auto-tls.
	AutoTLS bool `json:"auto_tls,omitempty"`
}

// WebhookSpec is a host webhook created from the plugin manifest. SecretEnv,
// if set, sends X-Flynn-Webhook-Secret from that release/cluster env key.
type WebhookSpec struct {
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	SecretEnv string            `json:"secret_env,omitempty"`
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
	for i, p := range m.Setup {
		if strings.TrimSpace(p.Env) == "" {
			return fmt.Errorf("%s: setup[%d].env is required", ManifestName, i)
		}
		if strings.TrimSpace(p.Prompt) == "" {
			return fmt.Errorf("%s: setup[%d].prompt is required", ManifestName, i)
		}
	}
	for i, r := range m.Routes {
		if strings.TrimSpace(r.Service) == "" {
			return fmt.Errorf("%s: routes[%d].service is required", ManifestName, i)
		}
		typ := strings.TrimSpace(r.Type)
		if typ == "" {
			typ = "http"
			m.Routes[i].Type = typ
		}
		if typ != "http" && typ != "tcp" {
			return fmt.Errorf("%s: routes[%d].type must be http or tcp", ManifestName, i)
		}
		if typ == "http" && strings.TrimSpace(r.Domain) == "" {
			return fmt.Errorf("%s: routes[%d].domain is required for http routes", ManifestName, i)
		}
		if r.AutoTLS && typ != "http" {
			return fmt.Errorf("%s: routes[%d].auto_tls is only valid for http routes", ManifestName, i)
		}
	}
	for i, w := range m.Webhooks {
		url := strings.TrimSpace(w.URL)
		if url == "" {
			return fmt.Errorf("%s: webhooks[%d].url is required", ManifestName, i)
		}
		if !strings.Contains(url, "${") && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return fmt.Errorf("%s: webhooks[%d].url must be http(s)", ManifestName, i)
		}
		m.Webhooks[i].URL = url
		m.Webhooks[i].SecretEnv = strings.TrimSpace(w.SecretEnv)
	}
	if m.CLI != nil {
		for i, a := range m.CLI.Actions {
			flynnCmd := strings.TrimSpace(a.Flynn)
			if flynnCmd == "" {
				continue
			}
			if strings.ContainsAny(flynnCmd, " \t") {
				return fmt.Errorf("%s: cli.actions[%d].flynn must be a single command", ManifestName, i)
			}
			if len(a.Args) > 0 {
				return fmt.Errorf("%s: cli.actions[%d]: flynn and args are mutually exclusive", ManifestName, i)
			}
			m.CLI.Actions[i].Flynn = flynnCmd
			if strings.TrimSpace(a.Name) == "" {
				m.CLI.Actions[i].Name = flynnCmd
			}
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
	fresh := m.AppMeta()
	if meta == nil {
		meta = map[string]string{}
	}
	for k, v := range fresh {
		meta[k] = v
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
	if m == nil {
		return ""
	}
	if m.Wait != "" {
		if pingURLAllowed(m.Wait) {
			return m.Wait
		}
		return ""
	}
	if m.Provider == nil || m.Provider.URL == "" {
		return ""
	}
	u, err := url.Parse(m.Provider.URL)
	if err != nil || u.Host == "" || !httpOrHTTPS(u.Scheme) {
		return ""
	}
	u.Path = "/ping"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func pingURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return httpOrHTTPS(u.Scheme)
}

func httpOrHTTPS(scheme string) bool {
	return scheme == "http" || scheme == "https"
}
