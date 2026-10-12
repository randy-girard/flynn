package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

// enterprisePluginApp is the app flynn-host plugin:install enterprise creates.
const enterprisePluginApp = "enterprise-plugin"

func (c *controllerAPI) GetClusterPolicy(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	p, err := c.loadClusterPolicy()
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, p)
}

func (c *controllerAPI) PutClusterPolicy(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var body ct.ClusterPolicy
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	normalizeClusterPolicy(&body)
	if body.DefaultRuntime != "" {
		p, err := c.runtimeProfileRepo.GetByName(body.DefaultRuntime)
		if err == data.ErrNotFound {
			httphelper.ValidationError(w, "default_runtime", fmt.Sprintf("unknown runtime %q", body.DefaultRuntime))
			return
		} else if err != nil {
			respondWithError(w, err)
			return
		}
		body.DefaultRuntime = p.Name
	}
	if err := c.tenancy.SetClusterPolicy(&body); err != nil {
		respondWithError(w, err)
		return
	}
	_ = c.tenancy.Audit(c.actor(ctx), "policy.set", "", "", body)
	installed, err := c.enterprisePluginInstalled()
	if err != nil {
		respondWithError(w, err)
		return
	}
	body.Enforced = installed
	httphelper.JSON(w, 200, &body)
}

// loadClusterPolicy returns the stored policy with Enforced set. The policy
// is kept when the enterprise plugin is uninstalled so a reinstall restores
// it, but it is not enforced in the meantime.
func (c *controllerAPI) loadClusterPolicy() (*ct.ClusterPolicy, error) {
	p, err := c.tenancy.GetClusterPolicy()
	if err != nil {
		return nil, err
	}
	if p.Enforced, err = c.enterprisePluginInstalled(); err != nil {
		return nil, err
	}
	return p, nil
}

func (c *controllerAPI) enterprisePluginInstalled() (bool, error) {
	if c.appRepo == nil {
		return false, nil
	}
	app, err := c.appRepo.Get(enterprisePluginApp)
	if err == data.ErrNotFound {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return app.(*ct.App).Plugin(), nil
}

func normalizeClusterPolicy(p *ct.ClusterPolicy) {
	p.DefaultRuntime = strings.ToLower(strings.TrimSpace(p.DefaultRuntime))
	p.AllowedBuildpacks = normalizePolicyList(p.AllowedBuildpacks)
	p.ImageAllowlist = normalizePolicyList(p.ImageAllowlist)
	p.Enforced = false
	p.UpdatedAt = nil
}

func normalizePolicyList(in []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(in))
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// enforceClusterPolicy applies the stored policy to a release for a
// non-system app before it is persisted.
func (c *controllerAPI) enforceClusterPolicy(app *ct.App, release *ct.Release) error {
	if app == nil || app.System() || release == nil {
		return nil
	}
	pol, err := c.loadClusterPolicy()
	if err != nil {
		return err
	}
	if !pol.Enforced {
		return nil
	}
	var image *ct.Artifact
	if len(pol.ImageAllowlist) > 0 {
		id := release.LegacyArtifactID
		if len(release.ArtifactIDs) > 0 {
			id = release.ArtifactIDs[0]
		}
		if id != "" {
			a, err := c.artifactRepo.Get(id)
			if err != nil && err != data.ErrNotFound {
				return err
			}
			if err == nil {
				image = a.(*ct.Artifact)
			}
		}
	}
	return applyClusterPolicy(pol, release, image)
}

// applyClusterPolicy sets the default runtime on new process types and
// rejects custom limits or images outside the allowlist. image is the
// release's first (runtime) artifact.
func applyClusterPolicy(pol *ct.ClusterPolicy, release *ct.Release, image *ct.Artifact) error {
	if pol == nil {
		return nil
	}
	for name, proc := range release.Processes {
		if ct.IsInternalProcessType(name) || strings.TrimSpace(proc.RuntimeProfile) != "" {
			continue
		}
		if !resource.HasMemoryOrCPU(proc.Resources) {
			if pol.DefaultRuntime != "" {
				proc.RuntimeProfile = pol.DefaultRuntime
				release.Processes[name] = proc
			}
			continue
		}
		if pol.DisableCustomCPU {
			return ct.ValidationError{
				Field:   "processes." + name + ".resources",
				Message: fmt.Sprintf("custom CPU limits are disabled by cluster policy; set a named runtime with `flynn limit:runtime %s <runtime>`", name),
			}
		}
	}
	if len(pol.ImageAllowlist) == 0 || image == nil {
		return nil
	}
	names := releaseImageNames(release, image)
	for _, n := range names {
		for _, allowed := range pol.ImageAllowlist {
			if n == allowed {
				return nil
			}
		}
	}
	desc := image.URI
	if len(names) > 0 {
		desc = names[0]
	}
	return ct.ValidationError{
		Field:   "artifacts",
		Message: fmt.Sprintf("image %q is not in the cluster image allowlist", desc),
	}
}

// releaseImageNames lists the stack and image names an allowlist entry can
// match: the artifact's flynn.component (with and without a version
// suffix), its docker-receive.repository, and the release's build stack.
func releaseImageNames(release *ct.Release, image *ct.Artifact) []string {
	var names []string
	add := func(v string) {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			names = append(names, v)
		}
	}
	if image.Meta != nil {
		comp := image.Meta["flynn.component"]
		add(comp)
		if i := strings.LastIndex(comp, "-"); i > 0 && isDigits(comp[i+1:]) {
			add(comp[:i])
		}
		add(image.Meta["docker-receive.repository"])
	}
	if release.Meta["docker-receive"] != "true" {
		stack := release.Meta["slugrunner.stack"]
		if stack == "" && image.IsSlugrunner() {
			stack = "heroku-24"
		}
		add(stack)
	}
	return names
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
