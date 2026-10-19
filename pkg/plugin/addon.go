package plugin

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
)

const MetaPluginAddon = "flynn-plugin-addon"

const (
	BackendResource = "resource"
	BackendPlugin   = "plugin"

	VerbCreate   = "create"
	VerbDestroy  = "destroy"
	VerbAttach   = "attach"
	VerbDetach   = "detach"
	VerbExpose   = "expose"
	VerbUnexpose = "unexpose"
)

// AddonSpec is the add-on type a plugin publishes in flynn-plugin.json.
// Core owns the generic lifecycle. The plugin owns verbs, create options,
// runtimes, expose defaults, and destroy rules. A third-party plugin adds a
// type by shipping this block; the flynn binary does not grow a case for it.
type AddonSpec struct {
	Type        string         `json:"type"`
	Description string         `json:"description,omitempty"`
	Aliases     []string       `json:"aliases,omitempty"`
	Verbs       []string       `json:"verbs"`
	Backend     string         `json:"backend"`
	API         string         `json:"api,omitempty"`
	Create      string         `json:"create,omitempty"`
	Provider    string         `json:"provider,omitempty"`
	Wait        string         `json:"wait,omitempty"`
	Env         *AddonEnv      `json:"env,omitempty"`
	Runtimes    *AddonRuntimes `json:"runtimes,omitempty"`
	Expose      *AddonExpose   `json:"expose,omitempty"`
	Destroy     *AddonDestroy  `json:"destroy,omitempty"`
	Reject      []AddonFlag    `json:"reject,omitempty"`
	Requires    []AddonRequire `json:"requires,omitempty"`
	// InstancePort asks the controller to stamp FLYNN_INSTANCE_PORT at provision.
	InstancePort bool `json:"instance_port,omitempty"`
	// PassApp asks the controller to add the tenant app name to the provision
	// config. Generic providers do not get that field.
	PassApp bool `json:"pass_app,omitempty"`
	// App is the installed plugin app. It is not part of flynn-plugin.json.
	App string `json:"app,omitempty"`
}

// AddonEnv is how this type names attachment env vars.
type AddonEnv struct {
	Prefix       string `json:"prefix,omitempty"`
	Conventional string `json:"conventional,omitempty"`
}

// AddonRuntimes is either the cluster database-runtime catalog or an explicit list.
type AddonRuntimes struct {
	Source       string         `json:"source"`
	Engine       string         `json:"engine,omitempty"`
	Default      string         `json:"default,omitempty"`
	CustomSizing bool           `json:"custom_sizing,omitempty"`
	Names        []AddonRuntime `json:"names,omitempty"`
}

// AddonRuntime is one named size when source is "list".
type AddonRuntime struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// AddonExpose is the opt-in for addons:expose. Empty TLS mode stays the
// route default (passthrough) unless DefaultTLS is set.
type AddonExpose struct {
	Service    string   `json:"service,omitempty"`
	Protocol   string   `json:"protocol,omitempty"`
	TLSModes   []string `json:"tls_modes,omitempty"`
	DefaultTLS string   `json:"default_tls,omitempty"`
	Hostname   string   `json:"hostname,omitempty"`
}

// AddonDestroy lists env keys that point at a leader. Core uses them to
// refuse destroy while a follower is still linked. The message text is generic;
// the key names come from the plugin.
type AddonDestroy struct {
	FollowerEnv []string `json:"follower_env,omitempty"`
}

// AddonFlag is a create flag the type rejects, with the plugin's message.
type AddonFlag struct {
	Flag    string `json:"flag"`
	Message string `json:"message"`
}

// AddonRequire is a create flag that needs another flag.
type AddonRequire struct {
	Flag    string `json:"flag"`
	Needs   string `json:"needs"`
	Message string `json:"message"`
}

// Normalize trims fields and fills defaults. Manifest.Validate calls it.
func (a *AddonSpec) Normalize() {
	if a == nil {
		return
	}
	a.Type = strings.ToLower(strings.TrimSpace(a.Type))
	a.Description = strings.TrimSpace(a.Description)
	a.Backend = strings.ToLower(strings.TrimSpace(a.Backend))
	a.API = strings.TrimRight(strings.TrimSpace(a.API), "/")
	a.Create = strings.TrimSpace(a.Create)
	a.Provider = strings.ToLower(strings.TrimSpace(a.Provider))
	a.Wait = strings.TrimSpace(a.Wait)
	a.App = strings.TrimSpace(a.App)
	if a.Provider == "" {
		a.Provider = a.Type
	}
	var aliases []string
	seen := map[string]struct{}{a.Type: {}}
	for _, alias := range a.Aliases {
		alias = strings.ToLower(strings.TrimSpace(alias))
		if alias == "" {
			continue
		}
		if _, ok := seen[alias]; ok {
			continue
		}
		seen[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	a.Aliases = aliases
	var verbs []string
	seenVerb := map[string]struct{}{}
	for _, verb := range a.Verbs {
		verb = strings.ToLower(strings.TrimSpace(verb))
		if verb == "" {
			continue
		}
		if _, ok := seenVerb[verb]; ok {
			continue
		}
		seenVerb[verb] = struct{}{}
		verbs = append(verbs, verb)
	}
	if _, ok := seenVerb[VerbExpose]; ok {
		if _, ok := seenVerb[VerbUnexpose]; !ok {
			verbs = append(verbs, VerbUnexpose)
		}
	}
	a.Verbs = verbs
	if a.Env != nil {
		a.Env.Prefix = strings.TrimSpace(a.Env.Prefix)
		a.Env.Conventional = strings.TrimSpace(a.Env.Conventional)
	}
	if a.Runtimes != nil {
		a.Runtimes.Source = strings.ToLower(strings.TrimSpace(a.Runtimes.Source))
		a.Runtimes.Engine = strings.ToLower(strings.TrimSpace(a.Runtimes.Engine))
		a.Runtimes.Default = strings.ToLower(strings.TrimSpace(a.Runtimes.Default))
		if a.Runtimes.Default == "" {
			a.Runtimes.Default = "small"
		}
	}
	if a.Expose != nil {
		a.Expose.Service = strings.TrimSpace(a.Expose.Service)
		a.Expose.Protocol = strings.ToLower(strings.TrimSpace(a.Expose.Protocol))
		a.Expose.DefaultTLS = strings.ToLower(strings.TrimSpace(a.Expose.DefaultTLS))
		a.Expose.Hostname = strings.TrimSpace(a.Expose.Hostname)
		for i, mode := range a.Expose.TLSModes {
			a.Expose.TLSModes[i] = strings.ToLower(strings.TrimSpace(mode))
		}
	}
}

// Validate checks the addon contract. It does not know provider names.
func (a *AddonSpec) Validate() error {
	if a == nil {
		return fmt.Errorf("%s: addon is required", ManifestName)
	}
	if a.Type == "" {
		return fmt.Errorf("%s: addon.type is required", ManifestName)
	}
	switch a.Backend {
	case BackendResource, BackendPlugin:
	default:
		return fmt.Errorf("%s: addon.backend must be %q or %q", ManifestName, BackendResource, BackendPlugin)
	}
	if len(a.Verbs) == 0 || !a.Supports(VerbCreate) {
		return fmt.Errorf("%s: addon.verbs must include %q", ManifestName, VerbCreate)
	}
	for _, verb := range a.Verbs {
		switch verb {
		case VerbCreate, VerbDestroy, VerbAttach, VerbDetach, VerbExpose, VerbUnexpose:
		default:
			return fmt.Errorf("%s: addon.verbs contains unknown %q", ManifestName, verb)
		}
	}
	if a.Backend == BackendPlugin {
		if a.API == "" {
			return fmt.Errorf("%s: addon.api is required for backend %q", ManifestName, BackendPlugin)
		}
		u, err := url.Parse(a.API)
		if err != nil || u.Host == "" || !httpOrHTTPS(u.Scheme) {
			return fmt.Errorf("%s: addon.api must be http(s)", ManifestName)
		}
	}
	if a.Supports(VerbExpose) && a.Expose == nil {
		return fmt.Errorf("%s: addon.expose is required when verbs include expose", ManifestName)
	}
	if a.Expose != nil && !a.Supports(VerbExpose) {
		return fmt.Errorf("%s: addon.expose requires the expose verb", ManifestName)
	}
	if a.Runtimes != nil {
		switch a.Runtimes.Source {
		case "catalog":
			if a.Runtimes.Engine == "" {
				return fmt.Errorf("%s: addon.runtimes.engine is required for source catalog", ManifestName)
			}
		case "list":
			if len(a.Runtimes.Names) == 0 {
				return fmt.Errorf("%s: addon.runtimes.names is required for source list", ManifestName)
			}
		default:
			return fmt.Errorf("%s: addon.runtimes.source must be catalog or list", ManifestName)
		}
	}
	for i, r := range a.Reject {
		if strings.TrimSpace(r.Flag) == "" || strings.TrimSpace(r.Message) == "" {
			return fmt.Errorf("%s: addon.reject[%d] needs flag and message", ManifestName, i)
		}
	}
	for i, r := range a.Requires {
		if strings.TrimSpace(r.Flag) == "" || strings.TrimSpace(r.Needs) == "" || strings.TrimSpace(r.Message) == "" {
			return fmt.Errorf("%s: addon.requires[%d] needs flag, needs, and message", ManifestName, i)
		}
	}
	return nil
}

// Supports reports whether this type declares verb.
func (a *AddonSpec) Supports(verb string) bool {
	if a == nil {
		return false
	}
	verb = strings.ToLower(strings.TrimSpace(verb))
	for _, v := range a.Verbs {
		if v == verb {
			return true
		}
	}
	return false
}

// MatchesType is true for the type name or an alias.
func (a *AddonSpec) MatchesType(name string) bool {
	if a == nil {
		return false
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	if name == a.Type || name == a.Provider {
		return true
	}
	for _, alias := range a.Aliases {
		if name == alias {
			return true
		}
	}
	return false
}

// VerbError is the message for a verb this type does not declare.
func (a *AddonSpec) VerbError(verb string) error {
	typ := "this"
	if a != nil && a.Type != "" {
		typ = a.Type
	}
	return fmt.Errorf("%s addons do not support %s", typ, verb)
}

// CheckArgv applies plugin-declared reject and requires rules to raw argv.
func (a *AddonSpec) CheckArgv(argv []string) error {
	if a == nil {
		return nil
	}
	has := func(flag string) bool {
		flag = strings.TrimSpace(flag)
		if flag == "" {
			return false
		}
		for _, arg := range argv {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return true
			}
		}
		return false
	}
	for _, r := range a.Reject {
		if has(r.Flag) {
			return fmt.Errorf("%s", r.Message)
		}
	}
	for _, r := range a.Requires {
		if has(r.Flag) && !has(r.Needs) {
			return fmt.Errorf("%s", r.Message)
		}
	}
	return nil
}

// CreateUsage is the docopt line for addons:create of this type.
// --as is included when the type can be attached or names an env prefix.
func (a *AddonSpec) CreateUsage() string {
	if a == nil {
		return "usage: flynn addons:create <type>"
	}
	parts := []string{"usage: flynn addons:create <type>"}
	if a.Supports(VerbAttach) || (a.Env != nil && a.Env.Prefix != "") {
		parts = append(parts, "[--as <name>]")
	}
	if a.Create != "" {
		parts = append(parts, a.Create)
	}
	return strings.Join(parts, " ")
}

// AcceptsRuntime is true when create offers --runtime.
func (a *AddonSpec) AcceptsRuntime() bool {
	return a != nil && a.Runtimes != nil
}

// DefaultTLS is the expose TLS mode when the user omits --tls-mode.
func (a *AddonSpec) DefaultTLS() string {
	if a == nil || a.Expose == nil {
		return ""
	}
	return a.Expose.DefaultTLS
}

// TLSModeAllowed reports whether mode is one this type exposes.
// An empty mode list allows the modes the route already accepts.
func (a *AddonSpec) TLSModeAllowed(mode string) bool {
	if a == nil || a.Expose == nil || len(a.Expose.TLSModes) == 0 {
		return true
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = a.DefaultTLS()
	}
	if mode == "" {
		mode = "passthrough"
	}
	for _, m := range a.Expose.TLSModes {
		if m == mode {
			return true
		}
	}
	return false
}

// AddonFromApp reads the installed add-on type. Manifest meta wins. An
// official plugin installed before this contract was stamped falls back to
// the copy in official-plugins.json so the CLI still works.
func AddonFromApp(app *ct.App) *AddonSpec {
	if app == nil || !app.Plugin() {
		return nil
	}
	if spec := addonFromMeta(app.Meta); spec != nil {
		spec.App = app.Name
		return spec
	}
	rec := RecordFromApp(app)
	if rec.Addon != nil {
		spec := *rec.Addon
		spec.Normalize()
		if spec.Validate() == nil {
			spec.App = app.Name
			return &spec
		}
	}
	if spec := OfficialAddonFor(rec); spec != nil {
		spec.App = app.Name
		return spec
	}
	return nil
}

func addonFromMeta(meta map[string]string) *AddonSpec {
	if meta == nil {
		return nil
	}
	raw := strings.TrimSpace(meta[MetaPluginAddon])
	if raw == "" {
		return nil
	}
	var spec AddonSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return nil
	}
	spec.Normalize()
	if spec.Validate() != nil {
		return nil
	}
	return &spec
}

// OfficialAddonFor returns the embedded contract for an installed official plugin.
func OfficialAddonFor(rec Installed) *AddonSpec {
	candidates := []string{rec.Provider, rec.Name, strings.TrimSuffix(rec.Name, "-plugin")}
	seen := map[string]struct{}{}
	for _, name := range candidates {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		if spec := OfficialAddon(name); spec != nil {
			return spec
		}
	}
	return nil
}

// OfficialAddon returns a copy of the embedded add-on spec for an official plugin name or alias.
func OfficialAddon(name string) *AddonSpec {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil
	}
	for _, p := range mustOfficial().Plugins {
		if p.Addon == nil {
			continue
		}
		if p.Name != name && !containsFold(p.Aliases, name) && !p.Addon.MatchesType(name) {
			continue
		}
		spec := *p.Addon
		spec.Normalize()
		if spec.Validate() != nil {
			return nil
		}
		return &spec
	}
	return nil
}

func containsFold(list []string, name string) bool {
	for _, item := range list {
		if strings.EqualFold(item, name) {
			return true
		}
	}
	return false
}
