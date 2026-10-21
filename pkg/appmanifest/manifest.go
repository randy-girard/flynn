// Package appmanifest parses and applies flynn-app.json, the app manifest
// (Heroku app.json equivalent). v1 apply ignores catalog. Container builds
// use a separate flynn.yml; this file provisions the app.
package appmanifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/randy-girard/flynn/pkg/random"
)

const (
	// MetaStatus is pending while an apply is unfinished and applied when it
	// finished. A later push does not read the file again.
	MetaStatus = "flynn-app.json"
	// StatusPending is set before any add-on is created.
	StatusPending = "pending"
	// StatusApplied is set after formation, routes, postdeploy, and GitHub.
	StatusApplied = "applied"
	// MetaPostdeploy is done or failed. A failed postdeploy is not run again.
	MetaPostdeploy = "flynn-app.json.postdeploy"
	// PostdeployDone means the one-shot already exited 0.
	PostdeployDone = "done"
	// PostdeployFailed means the one-shot exited non-zero. The next push
	// finishes the rest of the apply and does not run it again.
	PostdeployFailed = "failed"
	// MetaAddons is a JSON list of add-ons this attempt already scheduled.
	MetaAddons = "flynn-app.json.addons"
	// MetaRoutes is a JSON list of routes this attempt already added.
	MetaRoutes = "flynn-app.json.routes"
	// MetaPreviewDestroy is the resolved scripts.preview_destroy command.
	MetaPreviewDestroy = "flynn-app.json.preview_destroy"
	// MetaProtected is "true" when the manifest protected the app.
	MetaProtected = "protected"
	// MetaDescription stores the manifest description.
	MetaDescription = "description"
	// MetaPreviewSeed marks the env release a pipeline writes before the
	// first preview receive. That release does not make the app configured.
	MetaPreviewSeed = "flynn-pipeline.preview-seed"
	// MetaPreviewRoute is the preview HTTP route domain. That route does not
	// make the app configured.
	MetaPreviewRoute = "flynn-pipeline.preview-route"
	// MetaPreviewBranch is the preview git branch, used for placeholders.
	MetaPreviewBranch = "flynn-pipeline.preview-branch"
	// MetaPreviewSHA is the preview commit.
	MetaPreviewSHA = "flynn-pipeline.preview-sha"

	// SkipLine is printed when the file is present and the app is already
	// configured. The deploy continues and the file is not parsed.
	SkipLine = "-----> flynn-app.json present, app already configured; skipping"
	// ApplyLine is printed when an apply or resume starts.
	ApplyLine = "-----> Applying flynn-app.json"

	stackHeroku24  = "heroku-24"
	stackContainer = "container"
	schemaVersion  = 1
)

// Action is what a receive or apps:create should do with the file.
type Action int

const (
	// ActionNone means there is nothing to apply.
	ActionNone Action = iota
	// ActionSkip means the file is present and the app is already configured.
	ActionSkip
	// ActionApply means the app is untouched and the file should be applied.
	ActionApply
	// ActionResume means a previous attempt left the app pending.
	ActionResume
)

// Manifest is a parsed flynn-app.json. Environment overlays replace whole
// root keys; they are not deep-merged.
type Manifest struct {
	SchemaVersion int                  `json:"schema_version,omitempty"`
	Name          string               `json:"name,omitempty"`
	Description   string               `json:"description,omitempty"`
	Catalog       *Catalog             `json:"catalog,omitempty"`
	Repository    string               `json:"repository,omitempty"`
	Stack         string               `json:"stack,omitempty"`
	Buildpacks    []Buildpack          `json:"buildpacks,omitempty"`
	Image         string               `json:"image,omitempty"`
	Env           map[string]EnvVar    `json:"env,omitempty"`
	Formation     map[string]Formation `json:"formation,omitempty"`
	Addons        []Addon              `json:"addons,omitempty"`
	Scripts       Scripts              `json:"scripts,omitempty"`
	Routes        []Route              `json:"routes,omitempty"`
	GitHub        *GitHub              `json:"github,omitempty"`
	Strategy      string               `json:"strategy,omitempty"`
	DeployTimeout *int                 `json:"deploy_timeout,omitempty"`
	Protected     *bool                `json:"protected,omitempty"`
	Environments  map[string]Overlay   `json:"-"`
}

// Catalog is deploy-button metadata. v1 apply ignores it.
type Catalog struct {
	Keywords   []string `json:"keywords,omitempty"`
	Website    string   `json:"website,omitempty"`
	Logo       string   `json:"logo,omitempty"`
	SuccessURL string   `json:"success_url,omitempty"`
}

// Buildpack is one buildpack URL. Official shorthand names wait on Flynn-owned
// forks; until then url is a git URL.
type Buildpack struct {
	URL string `json:"url"`
}

// EnvVar is a string value or an object. Required defaults to true.
type EnvVar struct {
	Description string
	Value       string
	Required    *bool
	Generator   string
}

// Formation is one process type. Quantity is required except on release,
// where it is ignored.
type Formation struct {
	Quantity *int              `json:"quantity"`
	Runtime  string            `json:"runtime,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
	Limits   *Limits           `json:"limits,omitempty"`
	Volumes  []Volume          `json:"volumes,omitempty"`
}

// Limits are raw limit:set values. They apply only when the cluster allows
// custom limits.
type Limits struct {
	CPU      string `json:"cpu,omitempty"`
	Memory   string `json:"memory,omitempty"`
	MaxFD    string `json:"max_fd,omitempty"`
	TempDisk string `json:"temp_disk,omitempty"`
}

// Volume is merged onto the process type. Path is required.
type Volume struct {
	Path         string `json:"path"`
	DeleteOnStop bool   `json:"delete_on_stop,omitempty"`
}

// Addon is a string type or an object. Options match addons:create flags.
type Addon struct {
	Type    string
	As      string
	Options map[string]any
}

// Scripts are one-shots. Postdeploy runs at most once after the first create.
// PreviewDestroy runs when a preview app is destroyed.
type Scripts struct {
	Postdeploy     Script `json:"postdeploy,omitempty"`
	PreviewDestroy Script `json:"preview_destroy,omitempty"`
}

// Script is a command string or {command, runtime}.
type Script struct {
	Command string
	Runtime string
}

// Route is an extra HTTP or TCP route. The included {app}.{cluster} HTTP
// route is not listed. Path-based HTTP routes are rejected.
type Route struct {
	Type              string `json:"type"`
	Domain            string `json:"domain,omitempty"`
	Service           string `json:"service,omitempty"`
	Port              *int   `json:"port,omitempty"`
	Sticky            bool   `json:"sticky,omitempty"`
	Leader            bool   `json:"leader,omitempty"`
	HTTPS             bool   `json:"https,omitempty"`
	TLSMode           string `json:"tls_mode,omitempty"`
	DrainBackends     *bool  `json:"drain_backends,omitempty"`
	DisableKeepAlives bool   `json:"disable_keep_alives,omitempty"`
	Path              string `json:"path,omitempty"`
}

// GitHub is applied only when flynn-plugin-github is installed.
type GitHub struct {
	Repository string `json:"repository,omitempty"`
	Branch     string `json:"branch,omitempty"`
	AutoDeploy bool   `json:"auto_deploy,omitempty"`
	WaitChecks bool   `json:"wait_checks,omitempty"`
}

// Overlay replaces whole root keys that are present in an environment block.
type Overlay struct {
	present       map[string]struct{}
	Env           map[string]EnvVar
	Formation     map[string]Formation
	Addons        []Addon
	Routes        []Route
	Scripts       Scripts
	Stack         string
	Buildpacks    []Buildpack
	Image         string
	GitHub        *GitHub
	Strategy      string
	DeployTimeout *int
	Protected     *bool
}

var rootKeys = map[string]struct{}{
	"schema_version": {},
	"name":           {},
	"description":    {},
	"catalog":        {},
	"repository":     {},
	"stack":          {},
	"buildpacks":     {},
	"image":          {},
	"env":            {},
	"formation":      {},
	"addons":         {},
	"scripts":        {},
	"routes":         {},
	"github":         {},
	"strategy":       {},
	"deploy_timeout": {},
	"protected":      {},
	"environments":   {},
}

var overlayKeys = map[string]struct{}{
	"env":            {},
	"formation":      {},
	"addons":         {},
	"routes":         {},
	"scripts":        {},
	"stack":          {},
	"buildpacks":     {},
	"image":          {},
	"github":         {},
	"strategy":       {},
	"deploy_timeout": {},
	"protected":      {},
}

var strategies = map[string]struct{}{
	"all-at-once":     {},
	"one-by-one":      {},
	"one-down-one-up": {},
	"in-batches":      {},
}

var envNames = map[string]struct{}{
	"development": {},
	"staging":     {},
	"production":  {},
	"preview":     {},
}

// Parse validates flynn-app.json. schema_version defaults to 1.
func Parse(raw []byte) (*Manifest, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("flynn-app.json is empty")
	}
	var keys map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&keys); err != nil {
		return nil, fmt.Errorf("flynn-app.json: %w", err)
	}
	for k := range keys {
		if _, ok := rootKeys[k]; !ok {
			return nil, fmt.Errorf("flynn-app.json: unknown field %q", k)
		}
	}
	if blob, ok := keys["schema_version"]; ok {
		var n int
		if err := json.Unmarshal(blob, &n); err != nil || n != schemaVersion {
			return nil, fmt.Errorf("flynn-app.json: unsupported schema_version")
		}
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("flynn-app.json: %w", err)
	}
	if blob, ok := keys["environments"]; ok {
		envs, err := parseEnvironments(blob)
		if err != nil {
			return nil, err
		}
		m.Environments = envs
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

func parseEnvironments(raw json.RawMessage) (map[string]Overlay, error) {
	var blocks map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("flynn-app.json: environments: %w", err)
	}
	out := make(map[string]Overlay, len(blocks))
	for name, blob := range blocks {
		canon := canonicalEnv(name)
		if canon == "" {
			return nil, fmt.Errorf("flynn-app.json: unknown environment %q", name)
		}
		if _, dup := out[canon]; dup {
			return nil, fmt.Errorf("flynn-app.json: environment %q is already set", canon)
		}
		ov, err := parseOverlay(blob)
		if err != nil {
			return nil, fmt.Errorf("flynn-app.json: environments.%s: %w", canon, err)
		}
		out[canon] = ov
	}
	return out, nil
}

func parseOverlay(raw json.RawMessage) (Overlay, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return Overlay{}, err
	}
	var ov Overlay
	ov.present = map[string]struct{}{}
	for k := range keys {
		if _, ok := overlayKeys[k]; !ok {
			return Overlay{}, fmt.Errorf("unknown field %q", k)
		}
		ov.present[k] = struct{}{}
	}
	var body Manifest
	if err := json.Unmarshal(raw, &body); err != nil {
		return Overlay{}, err
	}
	ov.Env = body.Env
	ov.Formation = body.Formation
	ov.Addons = body.Addons
	ov.Routes = body.Routes
	ov.Scripts = body.Scripts
	ov.Stack = body.Stack
	ov.Buildpacks = body.Buildpacks
	ov.Image = body.Image
	ov.GitHub = body.GitHub
	ov.Strategy = body.Strategy
	ov.DeployTimeout = body.DeployTimeout
	ov.Protected = body.Protected
	return ov, nil
}

// canonicalEnv maps Heroku's review name to preview. Other names must be one
// of development, staging, production, or preview.
func canonicalEnv(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "review" {
		return "preview"
	}
	if _, ok := envNames[n]; ok {
		return n
	}
	return ""
}

func (m *Manifest) validate() error {
	if m.Name != "" && len(m.Name) > 30 {
		return fmt.Errorf("flynn-app.json: name must be at most 30 characters")
	}
	stack := m.EffectiveStack()
	switch stack {
	case stackHeroku24, stackContainer:
	default:
		return fmt.Errorf("flynn-app.json: unknown stack %q", m.Stack)
	}
	if strings.TrimSpace(m.Image) != "" && stack != stackContainer {
		return fmt.Errorf("flynn-app.json: image is only valid when stack is container")
	}
	if m.Strategy != "" {
		if _, ok := strategies[m.Strategy]; !ok {
			return fmt.Errorf("flynn-app.json: invalid strategy %q", m.Strategy)
		}
	}
	if m.DeployTimeout != nil && *m.DeployTimeout <= 0 {
		return fmt.Errorf("flynn-app.json: deploy_timeout must be a positive number of seconds")
	}
	for k, v := range m.Env {
		if err := v.validate(k); err != nil {
			return err
		}
	}
	for typ, proc := range m.Formation {
		if err := proc.validate(typ); err != nil {
			return err
		}
	}
	for i, a := range m.Addons {
		if strings.TrimSpace(a.Type) == "" {
			return fmt.Errorf("flynn-app.json: addons[%d] needs a type", i)
		}
	}
	for i, bp := range m.Buildpacks {
		if strings.TrimSpace(bp.URL) == "" {
			return fmt.Errorf("flynn-app.json: buildpacks[%d].url is required", i)
		}
	}
	for i, r := range m.Routes {
		if err := r.validate(i); err != nil {
			return err
		}
	}
	if m.GitHub != nil {
		if strings.TrimSpace(m.GitHub.Repository) == "" {
			m.GitHub.Repository = githubSlug(m.Repository)
		}
		if strings.TrimSpace(m.GitHub.Repository) == "" {
			return fmt.Errorf("flynn-app.json: github.repository is required")
		}
	}
	for name, ov := range m.Environments {
		tmp := m.cloneShell()
		tmp.applyOverlay(ov)
		tmp.Environments = nil
		if err := tmp.validate(); err != nil {
			return fmt.Errorf("flynn-app.json: environments.%s: %w", name, err)
		}
	}
	return nil
}

func (m Manifest) cloneShell() Manifest {
	out := m
	out.Environments = nil
	return out
}

func (e EnvVar) validate(name string) error {
	if e.Generator != "" && e.Generator != "secret" {
		return fmt.Errorf("flynn-app.json: env %s: unknown generator %q", name, e.Generator)
	}
	return nil
}

func (e EnvVar) required() bool {
	if e.Required == nil {
		return true
	}
	return *e.Required
}

func (f Formation) validate(typ string) error {
	if !strings.EqualFold(typ, "release") && f.Quantity == nil {
		return fmt.Errorf("flynn-app.json: formation.%s.quantity is required", typ)
	}
	if f.Quantity != nil && *f.Quantity < 0 {
		return fmt.Errorf("flynn-app.json: formation.%s.quantity cannot be negative", typ)
	}
	for i, v := range f.Volumes {
		if strings.TrimSpace(v.Path) == "" {
			return fmt.Errorf("flynn-app.json: formation.%s.volumes[%d].path is required", typ, i)
		}
	}
	return nil
}

func (r Route) validate(i int) error {
	switch r.Type {
	case "http", "tcp":
	default:
		return fmt.Errorf("flynn-app.json: routes[%d].type must be http or tcp", i)
	}
	if strings.TrimSpace(r.Path) != "" {
		return fmt.Errorf("flynn-app.json: routes[%d]: path-based HTTP routes are not supported", i)
	}
	if r.Type == "http" && strings.TrimSpace(r.Domain) == "" {
		return fmt.Errorf("flynn-app.json: routes[%d].domain is required for http", i)
	}
	if r.TLSMode != "" && r.TLSMode != "passthrough" && r.TLSMode != "terminate" {
		return fmt.Errorf("flynn-app.json: routes[%d].tls_mode must be passthrough or terminate", i)
	}
	if r.Type == "http" && r.TLSMode != "" {
		return fmt.Errorf("flynn-app.json: routes[%d].tls_mode is only valid for tcp", i)
	}
	if r.Type == "tcp" && (r.Sticky || r.DisableKeepAlives) {
		return fmt.Errorf("flynn-app.json: routes[%d]: sticky and disable_keep_alives are HTTP only", i)
	}
	return nil
}

// EffectiveStack is heroku-24 when stack is omitted.
func (m Manifest) EffectiveStack() string {
	s := strings.TrimSpace(m.Stack)
	if s == "" {
		return stackHeroku24
	}
	return s
}

// ScalesFormation is true when a non-release process type sets quantity.
// That scale replaces the receiver's implicit web=1.
func (m Manifest) ScalesFormation() bool {
	for typ, proc := range m.Formation {
		if strings.EqualFold(typ, "release") {
			continue
		}
		if proc.Quantity != nil {
			return true
		}
	}
	return false
}

// HasLimits reports a formation entry with raw limit:set values.
func (m Manifest) HasLimits() bool {
	for _, proc := range m.Formation {
		if proc.Limits != nil && proc.Limits.any() {
			return true
		}
	}
	return false
}

func (l *Limits) any() bool {
	if l == nil {
		return false
	}
	return l.CPU != "" || l.Memory != "" || l.MaxFD != "" || l.TempDisk != ""
}

// HasTags reports host placement tags on a process type.
func (m Manifest) HasTags() bool {
	for _, proc := range m.Formation {
		if len(proc.Tags) > 0 {
			return true
		}
	}
	return false
}

// WantsHTTPS reports a route that asks for a managed certificate.
func (m Manifest) WantsHTTPS() bool {
	for _, r := range m.Routes {
		if r.HTTPS {
			return true
		}
	}
	return false
}

// WithEnvironment returns a copy with that environment's keys replaced.
// Keys the overlay omits stay at the root. An unknown or empty environment
// returns the root manifest.
func (m Manifest) WithEnvironment(name string) Manifest {
	canon := canonicalEnv(name)
	if canon == "" {
		return m
	}
	ov, ok := m.Environments[canon]
	if !ok {
		return m
	}
	out := m
	out.applyOverlay(ov)
	return out
}

func (m *Manifest) applyOverlay(ov Overlay) {
	if _, ok := ov.present["env"]; ok {
		m.Env = ov.Env
	}
	if _, ok := ov.present["formation"]; ok {
		m.Formation = ov.Formation
	}
	if _, ok := ov.present["addons"]; ok {
		m.Addons = ov.Addons
	}
	if _, ok := ov.present["routes"]; ok {
		m.Routes = ov.Routes
	}
	if _, ok := ov.present["scripts"]; ok {
		m.Scripts = ov.Scripts
	}
	if _, ok := ov.present["stack"]; ok {
		m.Stack = ov.Stack
	}
	if _, ok := ov.present["buildpacks"]; ok {
		m.Buildpacks = ov.Buildpacks
	}
	if _, ok := ov.present["image"]; ok {
		m.Image = ov.Image
	}
	if _, ok := ov.present["github"]; ok {
		m.GitHub = ov.GitHub
	}
	if _, ok := ov.present["strategy"]; ok {
		m.Strategy = ov.Strategy
	}
	if _, ok := ov.present["deploy_timeout"]; ok {
		m.DeployTimeout = ov.DeployTimeout
	}
	if _, ok := ov.present["protected"]; ok {
		m.Protected = ov.Protected
	}
}

// ResolveEnv fills values. generator secret is 64 hex characters. Required
// keys with no value and no generator fail unless existing already has them.
// Placeholders are substituted in values. An existing secret is kept.
func ResolveEnv(env map[string]EnvVar, existing, ph map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range env {
		val := Substitute(v.Value, ph)
		if val == "" && v.Generator == "secret" {
			if prev := strings.TrimSpace(existing[k]); prev != "" {
				val = prev
			} else {
				val = random.Hex(32)
			}
		}
		if val == "" {
			if prev := strings.TrimSpace(existing[k]); prev != "" {
				val = prev
			}
		}
		if val == "" && v.required() {
			return nil, fmt.Errorf("flynn-app.json: env %s is required", k)
		}
		if val != "" {
			out[k] = val
		}
	}
	return out, nil
}

// Substitute replaces {{name}} in env values and script commands.
func Substitute(s string, ph map[string]string) string {
	if ph == nil || !strings.Contains(s, "{{") {
		return s
	}
	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, "{{")
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		rest = rest[i+2:]
		j := strings.Index(rest, "}}")
		if j < 0 {
			b.WriteString("{{")
			b.WriteString(rest)
			break
		}
		key := strings.TrimSpace(rest[:j])
		if v, ok := ph[key]; ok {
			b.WriteString(v)
		} else {
			b.WriteString("{{")
			b.WriteString(rest[:j])
			b.WriteString("}}")
		}
		rest = rest[j+2:]
	}
	return b.String()
}

// BuildpackPlan is how slugbuilder should see buildpacks. A .buildpacks file
// already in the repo wins. One URL and no file becomes BUILDPACK_URL. Several
// URLs are injected as a .buildpacks file (slugbuilder uses the first URL).
type BuildpackPlan struct {
	URL   string
	Lines []string
}

// PlanBuildpacks chooses BUILDPACK_URL or an injected .buildpacks file.
func PlanBuildpacks(m Manifest, repoHasFile bool) BuildpackPlan {
	if repoHasFile || len(m.Buildpacks) == 0 || m.EffectiveStack() != stackHeroku24 {
		return BuildpackPlan{}
	}
	if len(m.Buildpacks) == 1 {
		return BuildpackPlan{URL: strings.TrimSpace(m.Buildpacks[0].URL)}
	}
	lines := make([]string, 0, len(m.Buildpacks))
	for _, bp := range m.Buildpacks {
		if u := strings.TrimSpace(bp.URL); u != "" {
			lines = append(lines, u)
		}
	}
	return BuildpackPlan{Lines: lines}
}

func githubSlug(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.TrimSuffix(raw, ".git")
	if strings.HasPrefix(raw, "git@github.com:") {
		p := strings.TrimPrefix(raw, "git@github.com:")
		if strings.Count(p, "/") == 1 {
			return p
		}
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	if host != "github.com" && host != "www.github.com" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

func (e *EnvVar) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		e.Value = s
		req := false
		e.Required = &req
		return nil
	}
	var obj struct {
		Description string `json:"description"`
		Value       string `json:"value"`
		Required    *bool  `json:"required"`
		Generator   string `json:"generator"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	*e = EnvVar{
		Description: obj.Description,
		Value:       obj.Value,
		Required:    obj.Required,
		Generator:   obj.Generator,
	}
	return nil
}

func (a *Addon) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		a.Type = s
		return nil
	}
	var obj struct {
		Type    string         `json:"type"`
		As      string         `json:"as"`
		Options map[string]any `json:"options"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	*a = Addon{Type: obj.Type, As: obj.As, Options: obj.Options}
	return nil
}

func (s *Script) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var cmd string
		if err := json.Unmarshal(b, &cmd); err != nil {
			return err
		}
		s.Command = cmd
		return nil
	}
	var obj struct {
		Command string `json:"command"`
		Runtime string `json:"runtime"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	*s = Script{Command: obj.Command, Runtime: obj.Runtime}
	return nil
}
