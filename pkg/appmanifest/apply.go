package appmanifest

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/dbruntime"
	"github.com/randy-girard/flynn/pkg/plugin"
	"github.com/randy-girard/flynn/pkg/resname"
	router "github.com/randy-girard/flynn/router/types"
)

// Hooks let the CLI and the receiver supply pipeline and GitHub without
// this package talking to discoverd itself.
type Hooks struct {
	// Pipeline returns the app's environment. A missing pipeline is an empty
	// context and a nil error.
	Pipeline func(appID string) (PipelineContext, error)
	// GitHubInstalled reports whether flynn-plugin-github is on the cluster.
	GitHubInstalled func() bool
	// GitHubConnected reports an existing repo connection.
	GitHubConnected func(appID string) (bool, error)
	// ConnectGitHub runs github:connect for the app.
	ConnectGitHub func(appID string, spec GitHub) error
}

// PipelineContext is one pipeline membership.
type PipelineContext struct {
	Environment  string
	Placeholders map[string]string
}

// Applier runs the manifest against a controller.
type Applier struct {
	b            backend
	Catalog      *plugin.Catalog
	Runtimes     dbruntime.Catalog
	Admin        bool
	HTTPS        bool
	Log          io.Writer
	Hooks        Hooks
	manifest     *Manifest
	placeholders map[string]string
}

// PrepareOptions controls one apply.
type PrepareOptions struct {
	// Environment overrides the pipeline lookup. Empty uses Hooks.Pipeline.
	Environment string
	// Placeholders are merged over the pipeline placeholders.
	Placeholders map[string]string
	// ExistingEnv is the release env plus any push -e flags. Required keys
	// may already be satisfied here.
	ExistingEnv map[string]string
	// HonorImage is set for apps:create --manifest. A git deploy ignores image.
	HonorImage bool
	// RepoHasBuildpacks is true when the archive already has .buildpacks.
	RepoHasBuildpacks bool
}

// PrepareResult is the pre-build half. Env is the release env the build
// should copy, including add-on URLs and generated secrets.
type PrepareResult struct {
	Env              map[string]string
	HasFormation     bool
	InjectBuildpacks []string
	Image            string
	Stack            string
}

// FinishInput is the post-release half. DefaultScaled means the caller
// already scaled the implicit web=1 (or app=1) and Finish must not do it
// again. When the manifest has a formation, the caller leaves DefaultScaled
// false and Finish scales those quantities instead.
type FinishInput struct {
	Release       *ct.Release
	Processes     map[string]ct.ProcessType
	DefaultScaled bool
}

// PostdeployError is a non-zero postdeploy. The app is left in place, the
// error is recorded, and a later push does not run the command again.
type PostdeployError struct{ Err error }

func (e *PostdeployError) Error() string {
	if e == nil || e.Err == nil {
		return "postdeploy failed"
	}
	return e.Err.Error()
}

func (e *PostdeployError) Unwrap() error { return e.Err }

type addonRecord struct {
	Type string `json:"type"`
	As   string `json:"as"`
	ID   string `json:"id"`
}

type routeRecord struct {
	Domain string `json:"domain"`
	ID     string `json:"id"`
}

// Prepare parses the file, records pending, and applies strategy, env,
// add-ons, stack, and buildpacks. Formation, routes, postdeploy, and GitHub
// wait for Finish, after the release is current.
func (a *Applier) Prepare(appID string, raw []byte, opt PrepareOptions) (*PrepareResult, error) {
	if a.Log == nil {
		a.Log = io.Discard
	}
	app, err := a.b.GetApp(appID)
	if err != nil {
		return nil, err
	}
	pending := metaValue(app, MetaStatus) == StatusPending

	ctx, err := a.pipelineContext(app, opt)
	if err != nil {
		return nil, err
	}
	m, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	envName := opt.Environment
	if envName == "" {
		envName = ctx.Environment
	}
	applied := m.WithEnvironment(envName)
	a.placeholders = mergePlaceholders(ctx.Placeholders, opt.Placeholders)
	if a.placeholders == nil {
		a.placeholders = map[string]string{}
	}
	if a.placeholders["app"] == "" {
		a.placeholders["app"] = app.Name
	}
	applied.Scripts.Postdeploy.Command = Substitute(applied.Scripts.Postdeploy.Command, a.placeholders)
	applied.Scripts.PreviewDestroy.Command = Substitute(applied.Scripts.PreviewDestroy.Command, a.placeholders)

	if err := a.validateCluster(app, &applied, opt.ExistingEnv); err != nil {
		return nil, err
	}
	if !pending {
		if err := a.updateMeta(app, func(meta map[string]string) {
			meta[MetaStatus] = StatusPending
		}); err != nil {
			return nil, err
		}
	}
	fmt.Fprintln(a.Log, ApplyLine)
	if err := a.applyAppFields(app, &applied); err != nil {
		return nil, err
	}
	if err := a.provisionAddons(app, &applied); err != nil {
		return nil, err
	}
	env, err := a.writeEnv(app, &applied, opt)
	if err != nil {
		return nil, err
	}
	a.manifest = &applied
	plan := PlanBuildpacks(applied, opt.RepoHasBuildpacks)
	res := &PrepareResult{
		Env:          env,
		HasFormation: applied.ScalesFormation(),
		Stack:        applied.EffectiveStack(),
	}
	if plan.URL != "" {
		res.Env["BUILDPACK_URL"] = plan.URL
	}
	res.InjectBuildpacks = plan.Lines
	if opt.HonorImage && applied.EffectiveStack() == stackContainer {
		res.Image = strings.TrimSpace(applied.Image)
	}
	return res, nil
}

// Finish applies formation, routes, postdeploy, and GitHub, then marks the
// manifest applied. A postdeploy failure leaves pending and returns
// PostdeployError.
func (a *Applier) Finish(appID string, in FinishInput) error {
	if a.Log == nil {
		a.Log = io.Discard
	}
	if a.manifest == nil {
		return fmt.Errorf("flynn-app.json: finish called before prepare")
	}
	app, err := a.b.GetApp(appID)
	if err != nil {
		return err
	}
	m := a.manifest
	deferred := false
	if m.ScalesFormation() {
		ok, err := a.applyFormation(app, m, in)
		if err != nil {
			return err
		}
		if !ok {
			deferred = true
		}
	} else if !in.DefaultScaled {
		if err := a.scaleDefault(app, in); err != nil {
			return err
		}
	}
	if err := a.applyRoutes(app, m); err != nil {
		return err
	}
	ran, err := a.runPostdeploy(app, m, in.Release)
	if err != nil {
		return err
	}
	if !ran && strings.TrimSpace(m.Scripts.Postdeploy.Command) != "" && metaValue(app, MetaPostdeploy) == "" {
		deferred = true
	}
	if err := a.connectGitHub(app, m); err != nil {
		return err
	}
	if deferred {
		fmt.Fprintln(a.Log, "-----> flynn-app.json will finish on the first code deploy")
		return nil
	}
	return a.updateMeta(app, func(meta map[string]string) {
		meta[MetaStatus] = StatusApplied
	})
}

// ApplyCreate runs Prepare and Finish for apps:create --manifest. A failure
// before postdeploy deletes the new app. pushImage is used when stack is
// container and image is set.
func (a *Applier) ApplyCreate(app *ct.App, raw []byte, opt PrepareOptions, pushImage func(tag string) error) error {
	if app == nil {
		return fmt.Errorf("app is required")
	}
	opt.HonorImage = true
	res, err := a.Prepare(app.ID, raw, opt)
	if err != nil {
		return a.maybeRollback(app.ID, true, err)
	}
	if res.Image != "" {
		if pushImage == nil {
			err := fmt.Errorf("flynn-app.json: image %s needs a local docker client", res.Image)
			return a.maybeRollback(app.ID, true, err)
		}
		if err := pushImage(res.Image); err != nil {
			return a.maybeRollback(app.ID, true, err)
		}
	}
	head, err := a.b.Head(app.ID)
	if err != nil {
		return a.maybeRollback(app.ID, true, err)
	}
	procs := map[string]ct.ProcessType{}
	if head != nil {
		procs = head.Processes
	}
	if err := a.Finish(app.ID, FinishInput{Release: head, Processes: procs}); err != nil {
		return a.maybeRollback(app.ID, true, err)
	}
	return nil
}

func (a *Applier) maybeRollback(appID string, rollback bool, err error) error {
	if err == nil || !rollback {
		return err
	}
	var post *PostdeployError
	if errorsAs(err, &post) {
		return err
	}
	if delErr := a.b.DeleteApp(appID); delErr != nil {
		fmt.Fprintf(a.Log, "-----> WARN: could not delete app after flynn-app.json failed: %s\n", delErr)
	}
	return err
}

func (a *Applier) pipelineContext(app *ct.App, opt PrepareOptions) (PipelineContext, error) {
	if a.Hooks.Pipeline == nil {
		return PipelineContext{Placeholders: opt.Placeholders}, nil
	}
	ctx, err := a.Hooks.Pipeline(app.ID)
	if err != nil && metaValue(app, "flynn-pipeline") != "" {
		return PipelineContext{}, err
	}
	if err != nil {
		return PipelineContext{}, nil
	}
	return ctx, nil
}

func (a *Applier) validateCluster(app *ct.App, m *Manifest, existing map[string]string) error {
	if m.GitHub != nil {
		installed := a.Hooks.GitHubInstalled != nil && a.Hooks.GitHubInstalled()
		if !installed {
			return fmt.Errorf("flynn-app.json: github requires the github plugin")
		}
	}
	if m.WantsHTTPS() && !a.HTTPS {
		return fmt.Errorf("flynn-app.json: https requires Let's Encrypt (flynn letsencrypt)")
	}
	if m.HasLimits() && !a.allowCustomLimits() {
		return fmt.Errorf("flynn-app.json: custom limits are disabled on this cluster")
	}
	if m.HasTags() && !a.Admin && !a.allowUserTags() {
		return fmt.Errorf("flynn-app.json: formation tags require a cluster admin or runtime:allow-user-scale-tags")
	}
	profiles, err := a.b.Profiles()
	if err != nil {
		return err
	}
	for typ, proc := range m.Formation {
		if strings.TrimSpace(proc.Runtime) == "" {
			continue
		}
		if findProfile(profiles, proc.Runtime) == nil {
			return fmt.Errorf("flynn-app.json: formation.%s: unknown runtime %q", typ, proc.Runtime)
		}
	}
	for _, addon := range m.Addons {
		if _, err := a.addonConfig(addon); err != nil {
			return err
		}
	}
	if _, err := ResolveEnv(m.Env, existing, a.placeholders); err != nil {
		return err
	}
	return nil
}

func (a *Applier) allowCustomLimits() bool {
	s, err := a.b.Settings()
	return err == nil && s != nil && s.AllowCustomLimits
}

func (a *Applier) allowUserTags() bool {
	s, err := a.b.Settings()
	return err == nil && s != nil && s.AllowUserScaleTags
}

func (a *Applier) applyAppFields(app *ct.App, m *Manifest) error {
	upd := &ct.App{ID: app.ID}
	changed := false
	if m.Strategy != "" {
		upd.Strategy = m.Strategy
		changed = true
	}
	if m.DeployTimeout != nil {
		upd.DeployTimeout = int32(*m.DeployTimeout)
		changed = true
	}
	if changed {
		if err := a.b.UpdateApp(upd); err != nil {
			return err
		}
	}
	return a.updateMeta(app, func(meta map[string]string) {
		if d := strings.TrimSpace(m.Description); d != "" {
			meta[MetaDescription] = d
		}
		if m.Protected != nil && *m.Protected {
			meta[MetaProtected] = "true"
		}
		if cmd := strings.TrimSpace(m.Scripts.PreviewDestroy.Command); cmd != "" {
			meta[MetaPreviewDestroy] = cmd
		}
	})
}

func (a *Applier) provisionAddons(app *ct.App, m *Manifest) error {
	done := decodeAddons(metaValue(app, MetaAddons))
	for _, addon := range m.Addons {
		if addonSeen(done, addon) {
			continue
		}
		spec, cfg, err := a.prepareAddon(addon)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Log, "-----> Provisioning addon %s\n", addon.Type)
		id := ""
		var env map[string]string
		if spec.Backend == plugin.BackendPlugin {
			if err := a.b.PutPluginAddon(app.ID, spec.Type); err != nil {
				return err
			}
			id = spec.Type
		} else {
			msg := json.RawMessage(cfg)
			res, err := a.b.Provision(&ct.ResourceReq{
				ProviderID: spec.Provider,
				Apps:       []string{app.ID},
				Config:     &msg,
			})
			if err != nil {
				return err
			}
			id = res.ID
			env = res.Env
			if err := a.mergeAddonEnv(app.ID, env, addon.As); err != nil {
				return err
			}
		}
		done = append(done, addonRecord{Type: addon.Type, As: addon.As, ID: id})
		raw, err := json.Marshal(done)
		if err != nil {
			return err
		}
		if err := a.updateMeta(app, func(meta map[string]string) {
			meta[MetaAddons] = string(raw)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (a *Applier) prepareAddon(addon Addon) (*plugin.AddonSpec, []byte, error) {
	if a.Catalog == nil {
		return nil, nil, fmt.Errorf("flynn-app.json: unknown addon type %q", addon.Type)
	}
	spec := a.Catalog.AddonByType(addon.Type)
	if spec == nil {
		return nil, nil, fmt.Errorf("flynn-app.json: unknown addon type %q", addon.Type)
	}
	if !spec.Supports(plugin.VerbCreate) {
		return nil, nil, fmt.Errorf("flynn-app.json: %s addons cannot be created", addon.Type)
	}
	if spec.Backend == plugin.BackendPlugin {
		if len(addon.Options) > 0 || addon.As != "" {
			return nil, nil, fmt.Errorf("flynn-app.json: %s addons do not take options", addon.Type)
		}
		return spec, nil, nil
	}
	in, err := createInput(addon)
	if err != nil {
		return nil, nil, err
	}
	raw, err := spec.ProvisionConfig(in, a.Runtimes)
	if err != nil {
		return nil, nil, fmt.Errorf("flynn-app.json: addon %s: %w", addon.Type, err)
	}
	return spec, raw, nil
}

func (a *Applier) addonConfig(addon Addon) ([]byte, error) {
	_, raw, err := a.prepareAddon(addon)
	return raw, err
}

func createInput(addon Addon) (plugin.CreateInput, error) {
	in := plugin.CreateInput{As: addon.As}
	for k, v := range addon.Options {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "runtime":
			in.Runtime = fmt.Sprint(v)
		case "follow":
			in.Follow = fmt.Sprint(v)
		case "replication":
			in.Replication = fmt.Sprint(v)
		case "cpu":
			in.CPU = fmt.Sprint(v)
		case "memory":
			in.Memory = fmt.Sprint(v)
		case "disk":
			in.Disk = fmt.Sprint(v)
		case "auto_failover":
			in.AutoFailover = truthy(v)
		default:
			return in, fmt.Errorf("flynn-app.json: addon %s: unknown option %q", addon.Type, k)
		}
	}
	return in, nil
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true") || t == "1"
	default:
		return fmt.Sprint(v) == "true"
	}
}

func (a *Applier) mergeAddonEnv(appID string, incoming map[string]string, as string) error {
	if len(incoming) == 0 {
		return nil
	}
	head, err := a.b.Head(appID)
	if err != nil {
		return err
	}
	existing := map[string]string{}
	if head != nil && head.Env != nil {
		existing = head.Env
	}
	merged := resname.MergeAttachment(existing, incoming, as, true)
	return a.putEnv(appID, merged, false)
}

func (a *Applier) writeEnv(app *ct.App, m *Manifest, opt PrepareOptions) (map[string]string, error) {
	resolved, err := ResolveEnv(m.Env, opt.ExistingEnv, a.placeholders)
	if err != nil {
		return nil, err
	}
	plan := PlanBuildpacks(*m, opt.RepoHasBuildpacks)
	switch m.EffectiveStack() {
	case stackContainer:
		resolved["FLYNN_STACK"] = stackContainer
	default:
		resolved["FLYNN_STACK"] = ""
	}
	if plan.URL != "" {
		resolved["BUILDPACK_URL"] = plan.URL
	} else if opt.RepoHasBuildpacks || len(plan.Lines) > 0 {
		resolved["BUILDPACK_URL"] = ""
	}
	if err := a.putEnv(app.ID, resolved, true); err != nil {
		return nil, err
	}
	head, err := a.b.Head(app.ID)
	if err != nil {
		return nil, err
	}
	if head == nil || head.Env == nil {
		return map[string]string{}, nil
	}
	out := map[string]string{}
	for k, v := range head.Env {
		out[k] = v
	}
	return out, nil
}

// putEnv merges keys onto the current release. An empty value deletes the key
// when deleteEmpty is set (stack and BUILDPACK_URL).
func (a *Applier) putEnv(appID string, kv map[string]string, deleteEmpty bool) error {
	if len(kv) == 0 {
		return nil
	}
	head, err := a.b.Head(appID)
	if err != nil {
		return err
	}
	rel := &ct.Release{}
	if head != nil {
		rel.Env = cloneStrings(head.Env)
		rel.Meta = cloneStrings(head.Meta)
		rel.Processes = head.Processes
		rel.ArtifactIDs = append([]string{}, head.ArtifactIDs...)
		rel.LegacyArtifactID = head.LegacyArtifactID
	}
	if rel.Env == nil {
		rel.Env = map[string]string{}
	}
	for k, v := range kv {
		if v == "" && deleteEmpty {
			delete(rel.Env, k)
			continue
		}
		if v == "" {
			continue
		}
		rel.Env[k] = v
	}
	return a.b.SaveRelease(appID, rel, len(rel.ArtifactIDs) > 0)
}

func (a *Applier) applyFormation(app *ct.App, m *Manifest, in FinishInput) (bool, error) {
	procs := in.Processes
	if len(procs) == 0 && in.Release != nil {
		procs = in.Release.Processes
	}
	if len(procs) == 0 {
		return false, nil
	}
	profiles, err := a.b.Profiles()
	if err != nil {
		return false, err
	}
	next := map[string]ct.ProcessType{}
	for k, v := range procs {
		next[k] = v
	}
	scale := map[string]int{}
	tags := map[string]map[string]string{}
	for typ, spec := range m.Formation {
		proc, ok := next[typ]
		if !ok {
			if strings.EqualFold(typ, "release") {
				continue
			}
			return false, fmt.Errorf("flynn-app.json: formation.%s is not in this release", typ)
		}
		if spec.Runtime != "" {
			profile := findProfile(profiles, spec.Runtime)
			if profile == nil {
				return false, fmt.Errorf("flynn-app.json: formation.%s: unknown runtime %q", typ, spec.Runtime)
			}
			proc = applyProfile(proc, profile)
		}
		if spec.Limits != nil && spec.Limits.any() {
			var err error
			proc, err = applyRawLimits(proc, spec.Limits)
			if err != nil {
				return false, err
			}
		}
		if len(spec.Volumes) > 0 {
			proc.Volumes = mergeVolumes(proc.Volumes, spec.Volumes)
		}
		next[typ] = proc
		if strings.EqualFold(typ, "release") || spec.Quantity == nil {
			continue
		}
		scale[typ] = *spec.Quantity
		if len(spec.Tags) > 0 {
			tags[typ] = spec.Tags
		}
	}
	rel := in.Release
	if rel == nil {
		rel = &ct.Release{}
	}
	updated := &ct.Release{
		Env:              cloneStrings(rel.Env),
		Meta:             cloneStrings(rel.Meta),
		Processes:        next,
		ArtifactIDs:      append([]string{}, rel.ArtifactIDs...),
		LegacyArtifactID: rel.LegacyArtifactID,
	}
	if err := a.b.SaveRelease(app.ID, updated, len(updated.ArtifactIDs) > 0); err != nil {
		return false, err
	}
	if len(scale) == 0 {
		return true, nil
	}
	return true, a.b.Scale(app.ID, updated.ID, ct.ScaleOptions{Processes: scale, Tags: tags})
}

func (a *Applier) scaleDefault(app *ct.App, in FinishInput) error {
	procs := in.Processes
	if len(procs) == 0 && in.Release != nil {
		procs = in.Release.Processes
	}
	name := ""
	if _, ok := procs["web"]; ok {
		name = "web"
	} else if _, ok := procs["app"]; ok {
		name = "app"
	}
	if name == "" || in.Release == nil || in.Release.ID == "" {
		return nil
	}
	return a.b.Scale(app.ID, in.Release.ID, ct.ScaleOptions{Processes: map[string]int{name: 1}})
}

func applyProfile(proc ct.ProcessType, profile *ct.RuntimeProfile) ct.ProcessType {
	if proc.Resources == nil {
		proc.Resources = resource.Defaults()
	}
	resource.ApplyNamedLimitsWithReserve(proc.Resources, profile.Memory, profile.CPU, profile.ReserveResources)
	proc.RuntimeProfile = profile.Name
	return proc
}

func applyRawLimits(proc ct.ProcessType, lim *Limits) (ct.ProcessType, error) {
	if proc.Resources == nil {
		proc.Resources = resource.Defaults()
	}
	set := func(typ resource.Type, raw string) error {
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		n, err := resource.ParseLimit(typ, raw)
		if err != nil {
			return err
		}
		proc.Resources[typ] = resource.Spec{Request: &n, Limit: &n}
		return nil
	}
	if err := set(resource.TypeCPU, lim.CPU); err != nil {
		return proc, err
	}
	if err := set(resource.TypeMemory, lim.Memory); err != nil {
		return proc, err
	}
	if err := set(resource.TypeMaxFD, lim.MaxFD); err != nil {
		return proc, err
	}
	if err := set(resource.TypeTempDisk, lim.TempDisk); err != nil {
		return proc, err
	}
	return proc, nil
}

func mergeVolumes(existing []ct.VolumeReq, add []Volume) []ct.VolumeReq {
	out := append([]ct.VolumeReq{}, existing...)
	for _, v := range add {
		found := false
		for i := range out {
			if out[i].Path == v.Path {
				out[i].DeleteOnStop = v.DeleteOnStop
				found = true
				break
			}
		}
		if !found {
			out = append(out, ct.VolumeReq{Path: v.Path, DeleteOnStop: v.DeleteOnStop})
		}
	}
	return out
}

func findProfile(list []*ct.RuntimeProfile, name string) *ct.RuntimeProfile {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, p := range list {
		if p != nil && strings.ToLower(p.Name) == want {
			return p
		}
	}
	return nil
}

func (a *Applier) applyRoutes(app *ct.App, m *Manifest) error {
	if len(m.Routes) == 0 {
		return nil
	}
	existing, err := a.b.Routes(app.ID)
	if err != nil {
		return err
	}
	have := map[string]struct{}{}
	for _, r := range existing {
		if r != nil && r.Domain != "" {
			have[strings.ToLower(r.Domain)] = struct{}{}
		}
	}
	recorded := decodeRoutes(metaValue(app, MetaRoutes))
	for _, rec := range recorded {
		if rec.Domain != "" {
			have[strings.ToLower(rec.Domain)] = struct{}{}
		}
	}
	for _, spec := range m.Routes {
		domain := strings.TrimSpace(spec.Domain)
		if domain != "" {
			if _, ok := have[strings.ToLower(domain)]; ok {
				continue
			}
		}
		route := &router.Route{
			Type:              spec.Type,
			Domain:            domain,
			Service:           spec.Service,
			Sticky:            spec.Sticky,
			Leader:            spec.Leader,
			TLSMode:           spec.TLSMode,
			DisableKeepAlives: spec.DisableKeepAlives,
			DrainBackends:     true,
			ParentRef:         "controller/apps/" + app.ID,
		}
		if spec.Service == "" {
			route.Service = app.Name + "-web"
		}
		if spec.Port != nil {
			route.Port = int32(*spec.Port)
		}
		if spec.DrainBackends != nil {
			route.DrainBackends = *spec.DrainBackends
		}
		if spec.HTTPS && domain != "" {
			host := domain
			route.ManagedCertificateDomain = &host
		}
		fmt.Fprintf(a.Log, "-----> Adding %s route %s\n", spec.Type, domain)
		if err := a.b.CreateRoute(app.ID, route); err != nil {
			return err
		}
		recorded = append(recorded, routeRecord{Domain: domain, ID: route.ID})
		if domain != "" {
			have[strings.ToLower(domain)] = struct{}{}
		}
		raw, err := json.Marshal(recorded)
		if err != nil {
			return err
		}
		if err := a.updateMeta(app, func(meta map[string]string) {
			meta[MetaRoutes] = string(raw)
		}); err != nil {
			return err
		}
	}
	return nil
}

// runPostdeploy returns false when the command was not run and is not yet
// recorded, so Finish can leave the apply pending.
func (a *Applier) runPostdeploy(app *ct.App, m *Manifest, rel *ct.Release) (bool, error) {
	cmd := strings.TrimSpace(m.Scripts.Postdeploy.Command)
	if cmd == "" {
		return true, nil
	}
	switch metaValue(app, MetaPostdeploy) {
	case PostdeployDone, PostdeployFailed:
		return true, nil
	}
	if rel == nil || rel.ID == "" || len(rel.ArtifactIDs) == 0 {
		return false, nil
	}
	fmt.Fprintf(a.Log, "-----> Running postdeploy\n       %s\n", cmd)
	var resources resource.Resources
	if rt := strings.TrimSpace(m.Scripts.Postdeploy.Runtime); rt != "" {
		profiles, err := a.b.Profiles()
		if err != nil {
			return false, err
		}
		profile := findProfile(profiles, rt)
		if profile == nil {
			return false, fmt.Errorf("flynn-app.json: scripts.postdeploy: unknown runtime %q", rt)
		}
		resources = resource.Defaults()
		resource.ApplyNamedLimitsWithReserve(resources, profile.Memory, profile.CPU, profile.ReserveResources)
	}
	err := a.b.RunCommand(app.ID, rel, cmd, resources)
	if err != nil {
		_ = a.updateMeta(app, func(meta map[string]string) {
			meta[MetaPostdeploy] = PostdeployFailed
		})
		return true, &PostdeployError{Err: err}
	}
	if err := a.updateMeta(app, func(meta map[string]string) {
		meta[MetaPostdeploy] = PostdeployDone
	}); err != nil {
		return true, err
	}
	return true, nil
}

func (a *Applier) connectGitHub(app *ct.App, m *Manifest) error {
	if m.GitHub == nil {
		return nil
	}
	if a.Hooks.ConnectGitHub == nil {
		return fmt.Errorf("flynn-app.json: github requires the github plugin")
	}
	fmt.Fprintf(a.Log, "-----> Connecting GitHub %s\n", m.GitHub.Repository)
	return a.Hooks.ConnectGitHub(app.ID, *m.GitHub)
}

func (a *Applier) updateMeta(app *ct.App, fn func(map[string]string)) error {
	fresh, err := a.b.GetApp(app.ID)
	if err != nil {
		return err
	}
	if fresh.Meta == nil {
		fresh.Meta = map[string]string{}
	}
	fn(fresh.Meta)
	if err := a.b.UpdateAppMeta(fresh); err != nil {
		return err
	}
	app.Meta = fresh.Meta
	return nil
}

func decodeAddons(raw string) []addonRecord {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []addonRecord
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func addonSeen(list []addonRecord, addon Addon) bool {
	for _, rec := range list {
		if strings.EqualFold(rec.Type, addon.Type) && rec.As == addon.As {
			return true
		}
	}
	return false
}

func decodeRoutes(raw string) []routeRecord {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []routeRecord
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func cloneStrings(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mergePlaceholders(a, b map[string]string) map[string]string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func errorsAs(err error, target **PostdeployError) bool {
	for err != nil {
		if e, ok := err.(*PostdeployError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
