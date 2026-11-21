package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/schema"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/instanceport"
	"github.com/randy-girard/flynn/pkg/pgappliance"
	"github.com/randy-girard/flynn/pkg/plugin"
	"github.com/randy-girard/flynn/pkg/resname"
	"github.com/randy-girard/flynn/pkg/resource"
	"golang.org/x/net/context"
)

// UpdateProvider is PUT /providers/:providers_id. It changes tenant_safe
// only; name and URL in the body are ignored. Authz limits it to the cluster
// key (plugin install and update).
func (c *controllerAPI) UpdateProvider(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var in struct {
		TenantSafe *bool `json:"tenant_safe"`
	}
	if err := httphelper.DecodeJSON(req, &in); err != nil {
		respondWithError(w, err)
		return
	}
	if in.TenantSafe == nil {
		respondWithError(w, ct.ValidationError{Field: "tenant_safe", Message: "must be set"})
		return
	}
	params, _ := ctxhelper.ParamsFromContext(ctx)
	p, err := c.providerRepo.SetTenantSafe(params.ByName("providers_id"), *in.TenantSafe)
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, p)
}

func (c *controllerAPI) ProvisionResource(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	p, err := c.getProvider(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}

	var rr ct.ResourceReq
	if err = httphelper.DecodeJSON(req, &rr); err != nil {
		respondWithError(w, err)
		return
	}
	if err := c.hostedTenantSafe(ctx, p); err != nil {
		respondWithError(w, err)
		return
	}
	var owner string
	var target *ct.App
	var apps []*ct.App
	if len(rr.Apps) > 0 {
		var ok bool
		if apps, ok = c.provisionApps(ctx, w, rr.Apps); !ok {
			return
		}
		target = apps[0]
		if err := c.rejectIfSuspended(target.OwnerAccount); err != nil {
			respondWithError(w, err)
			return
		}
		owner = target.OwnerAccount
		if owner != "" {
			n, err := c.tenancy.CountResources(owner)
			if err != nil {
				respondWithError(w, err)
				return
			}
			if err := c.quotaAllows(owner, 0, 0, 0, n+1, 0); err != nil {
				respondWithError(w, err)
				return
			}
		}
	} else {
		account, err := c.ownerForUnattachedResource(ctx, rr.OwnerAccount)
		if err != nil {
			respondWithError(w, err)
			return
		}
		// A cluster key has no user. Resources it creates with no app stay
		// unowned, which is how bootstrap and the controller test client
		// provision. Any other caller needs an account.
		if account == "" {
			tok := authz.TokenFromContext(ctx)
			if tok == nil || !tok.ClusterKey {
				httphelper.Forbidden(w, "provisioning without an app requires a user account")
				return
			}
		} else if !c.allowUnattachedOwner(ctx, account) {
			httphelper.Forbidden(w, "provisioning without an app requires an account you administer")
			return
		}
		if err := c.rejectIfSuspended(account); err != nil {
			respondWithError(w, err)
			return
		}
		owner = account
		if owner != "" && c.tenancy != nil {
			n, err := c.tenancy.CountResources(owner)
			if err != nil {
				respondWithError(w, err)
				return
			}
			if err := c.quotaAllows(owner, 0, 0, 0, n+1, 0); err != nil {
				respondWithError(w, err)
				return
			}
		}
	}

	var config []byte
	if rr.Config != nil {
		config = *rr.Config
	} else {
		config = []byte(`{}`)
	}
	addon := c.addonForProvider(p)
	if addon != nil && addon.PassApp {
		tok := authz.TokenFromContext(ctx)
		config = provisionConfigWithApp(config, target, tok != nil && tok.ClusterKey)
	}
	// The built-in appliance is the platform database. Bootstrap still creates
	// the controller and blobstore databases (system apps, platform marker).
	// A tenant app never reaches the appliance, so no tenant role is created.
	if pgappliance.IsPlatformApplianceURL(p.URL) {
		allowed := len(apps) > 0
		for _, app := range apps {
			allowed = allowed && pgappliance.AllowPlatformApp(app.System(), app.Plugin())
		}
		if !allowed {
			respondWithError(w, ct.ValidationError{Field: "provider", Message: pgappliance.ErrTenantProvision.Error()})
			return
		}
		body, err := pgappliance.SystemProvisionBody(true)
		if err != nil {
			respondWithError(w, ct.ValidationError{Field: "provider", Message: err.Error()})
			return
		}
		config = body
	}
	data, err := resource.Provision(p.URL, config)
	if err != nil {
		respondWithError(w, err)
		return
	}
	rollbackProvision := func() {
		ids := resource.DeprovisionIDs(data.ID, data.Env)
		if err := resource.DeprovisionAny(p.URL, ids...); err != nil {
			if l, ok := ctxhelper.LoggerFromContext(ctx); ok {
				l.Error("rollback provision failed", "err", err, "id", data.ID)
			}
		}
	}
	data.Env = resname.ResourceEnv(c.appReleaseEnv(target), data.Env, provisionAs(config))

	res := &ct.Resource{
		ProviderID:   p.ID,
		ExternalID:   data.ID,
		Env:          data.Env,
		Apps:         rr.Apps,
		OwnerAccount: owner,
	}
	if target != nil {
		res.OwnerApp = target.ID
	}

	if err := schema.Validate(res); err != nil {
		rollbackProvision()
		respondWithError(w, err)
		return
	}

	if err := c.addProvisionedResource(p, addon, res); err != nil {
		rollbackProvision()
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, res)
}

// provisionApps loads every app named in a provision request. The caller must
// manage each one, and each must have the owner_account of the first, the
// same rule AddResourceApp applies, so a resource is never attached to an app
// of another account. It writes the error and returns false on refusal.
func (c *controllerAPI) provisionApps(ctx context.Context, w http.ResponseWriter, ids []string) ([]*ct.App, bool) {
	apps := make([]*ct.App, 0, len(ids))
	for _, id := range ids {
		raw, err := c.appRepo.Get(id)
		if err != nil {
			respondWithError(w, err)
			return nil, false
		}
		app := raw.(*ct.App)
		if len(apps) > 0 && app.OwnerAccount != apps[0].OwnerAccount {
			httphelper.Forbidden(w, "every app in apps must have the same owner_account")
			return nil, false
		}
		if !c.requireAppManage(ctx, w, app) {
			return nil, false
		}
		apps = append(apps, app)
	}
	return apps, true
}

// defaultResourceOwner is the account for a resource created with no app.
// An explicit user:<id> or org:<id> wins, so an organization context is not
// replaced by the caller's personal account. Empty falls back to user:<id>.
// A cluster key with no user stays unowned.
func defaultResourceOwner(requested, userID string, clusterKey bool) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		if !validResourceOwner(requested) {
			return "", ct.ValidationError{Field: "owner_account", Message: "must be user:<id> or org:<id>"}
		}
		return requested, nil
	}
	userID = strings.TrimSpace(userID)
	if clusterKey || userID == "" {
		return "", nil
	}
	return "user:" + userID, nil
}

func validResourceOwner(account string) bool {
	prefix := ""
	switch {
	case strings.HasPrefix(account, "user:"):
		prefix = "user:"
	case strings.HasPrefix(account, "org:"):
		prefix = "org:"
	default:
		return false
	}
	id := strings.TrimSpace(strings.TrimPrefix(account, prefix))
	return id != "" && !strings.ContainsAny(id, " \t/")
}

func (c *controllerAPI) ownerForUnattachedResource(ctx context.Context, requested string) (string, error) {
	tok := authz.TokenFromContext(ctx)
	userID := ""
	clusterKey := false
	if tok != nil {
		userID = tok.UserID
		clusterKey = tok.ClusterKey
	}
	return defaultResourceOwner(requested, userID, clusterKey)
}

func (c *controllerAPI) allowUnattachedOwner(ctx context.Context, owner string) bool {
	tok := authz.TokenFromContext(ctx)
	if tok == nil || tok.ClusterKey || tok.HasClusterAdmin() {
		return true
	}
	if tok.UserID != "" && owner == "user:"+tok.UserID {
		return true
	}
	return c.canAdminOwner(ctx, owner)
}

func provisionAs(config []byte) string {
	var body struct {
		As string `json:"as"`
	}
	if json.Unmarshal(config, &body) != nil {
		return ""
	}
	return body.As
}

// provisionConfigWithApp stamps the tenant app name so database plugins attach
// the same way as `flynn resource:add` (name, role, FLYNN_POSTGRESQL_<COLOR>_URL, and DATABASE_URL when free).
func provisionConfigWithApp(config []byte, app *ct.App, clusterKey bool) []byte {
	var obj map[string]any
	if len(config) == 0 {
		obj = map[string]any{}
	} else if err := json.Unmarshal(config, &obj); err != nil || obj == nil {
		return config
	}
	if app != nil && strings.TrimSpace(app.Name) != "" {
		// The request's app is the only tenant. A caller-supplied "app" or
		// "tenant" would let a plugin act for another tenant (for example
		// follow its database), so both are always replaced.
		obj["app"] = app.Name
		obj["tenant"] = app.Name
	} else if clusterKey {
		return config
	} else {
		delete(obj, "app")
		delete(obj, "tenant")
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return config
	}
	return out
}

func (c *controllerAPI) appReleaseEnv(app *ct.App) map[string]string {
	if app == nil || app.ReleaseID == "" || c.releaseRepo == nil {
		return nil
	}
	rel, err := c.releaseRepo.Get(app.ReleaseID)
	if err != nil || rel == nil {
		return nil
	}
	release, ok := rel.(*ct.Release)
	if !ok || release == nil {
		return nil
	}
	return release.Env
}

func (c *controllerAPI) GetProviderResources(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	p, err := c.getProvider(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}

	res, err := c.listVisibleResources(ctx, p.ID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, res)
}

func (c *controllerAPI) GetResources(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	res, err := c.listVisibleResources(ctx, "")
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, res)
}

// providerResource loads the resource for a provider route. A resource that
// belongs to another provider is reported as not found, so the provider in the
// URL cannot be used to reach or act on a different provider's resource.
func (c *controllerAPI) providerResource(p *ct.Provider, id string) (*ct.Resource, error) {
	res, err := c.resourceRepo.Get(id)
	if err != nil {
		return nil, err
	}
	if res.ProviderID != p.ID {
		return nil, ErrNotFound
	}
	return res, nil
}

// requireResourceAdmin lets the cluster key and cluster admins act on any
// resource. Everyone else must administer the owner account, and a resource
// with no owner (a platform resource) is never theirs.
func (c *controllerAPI) requireResourceAdmin(ctx context.Context, w http.ResponseWriter, res *ct.Resource) bool {
	tok := authz.TokenFromContext(ctx)
	if tok != nil && (tok.ClusterKey || tok.HasClusterAdmin()) {
		return true
	}
	if res.OwnerAccount == "" {
		httphelper.Forbidden(w, "this resource has no owner account; only a cluster admin can act on it")
		return false
	}
	return c.canAdminAccount(ctx, w, res.OwnerAccount)
}

func (c *controllerAPI) GetResource(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)

	p, err := c.getProvider(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}

	res, err := c.providerResource(p, params.ByName("resources_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	if !c.requireResourceAdmin(ctx, w, res) {
		return
	}
	httphelper.JSON(w, 200, res)
}

func (c *controllerAPI) PutResource(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)

	p, err := c.getProvider(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}

	var resource ct.Resource
	if err = httphelper.DecodeJSON(req, &resource); err != nil {
		respondWithError(w, err)
		return
	}

	resource.ID = params.ByName("resources_id")
	resource.ProviderID = p.ID

	if err := schema.Validate(resource); err != nil {
		respondWithError(w, err)
		return
	}

	if existing, err := c.resourceRepo.Get(resource.ID); err == nil {
		if existing.ProviderID != p.ID {
			respondWithError(w, ErrNotFound)
			return
		}
		if err := c.resourceRepo.Update(&resource); err != nil {
			respondWithError(w, err)
			return
		}
	} else if err == ErrNotFound {
		if err := c.resourceRepo.Add(&resource); err != nil {
			respondWithError(w, err)
			return
		}
	} else {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, &resource)
}

func (c *controllerAPI) DeleteResource(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	id := params.ByName("resources_id")

	logger.Info("getting provider", "params", params)

	p, err := c.getProvider(ctx)
	if err != nil {
		logger.Error("getting provider error", "err", err)
		respondWithError(w, err)
		return
	}

	logger.Info("getting resource", "id", id)
	res, err := c.providerResource(p, id)
	if err != nil {
		logger.Error("getting resource error", "err", err)
		respondWithError(w, err)
		return
	}

	if !c.requireResourceAdmin(ctx, w, res) {
		return
	}
	if appID := strings.TrimSpace(req.URL.Query().Get("app_id")); appID != "" {
		appRaw, err := c.appRepo.Get(appID)
		if err != nil {
			respondWithError(w, err)
			return
		}
		app := appRaw.(*ct.App)
		if !res.OwnedByApp(app) {
			httphelper.ValidationError(w, "resource", "this app is attached to the resource but does not own it; detach it instead")
			return
		}
	}
	ids := resource.DeprovisionIDs(res.ExternalID, res.Env)
	logger.Info("deprovisioning", "url", p.URL, "external.id", res.ExternalID, "ids", ids)
	if err := resource.DeprovisionAny(p.URL, ids...); err != nil {
		logger.Error("error deprovisioning", "err", err)
		respondWithError(w, err)
		return
	}

	logger.Info("removing resource")
	if err := c.resourceRepo.Remove(res); err != nil {
		logger.Error("error removing resource", "err", err)
		respondWithError(w, err)
		return
	}
	logger.Info("completed resource removal")

	httphelper.JSON(w, 200, res)
}

func (c *controllerAPI) AddResourceApp(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)

	p, err := c.getProvider(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}

	resource, err := c.providerResource(p, params.ByName("resources_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	appRaw, err := c.appRepo.Get(params.ByName("app_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	app := appRaw.(*ct.App)
	if pgappliance.IsPlatformApplianceURL(p.URL) && !pgappliance.AllowPlatformApp(app.System(), app.Plugin()) {
		respondWithError(w, ct.ValidationError{Field: "provider", Message: pgappliance.ErrTenantProvision.Error()})
		return
	}
	if resource.OwnerAccount != "" && app.OwnerAccount != resource.OwnerAccount {
		httphelper.Forbidden(w, "a resource can only be attached to an app with the same owner_account")
		return
	}
	if !c.requireAppManage(ctx, w, app) {
		return
	}
	// Attaching re-owns an unowned resource, so a platform resource can only
	// be attached by a cluster admin.
	if resource.OwnerAccount == "" && !c.requireResourceAdmin(ctx, w, resource) {
		return
	}
	if resource.OwnerAccount == "" && app.OwnerAccount != "" && c.tenancy != nil {
		if err := c.tenancy.SetResourceOwner(resource.ID, app.OwnerAccount); err != nil {
			respondWithError(w, err)
			return
		}
	}
	if resource.OwnerApp == "" {
		if err := c.resourceRepo.SetOwnerApp(resource.ID, app.ID); err != nil {
			respondWithError(w, err)
			return
		}
	}
	resource, err = c.resourceRepo.AddApp(params.ByName("resources_id"), app.ID)
	if err != nil {
		respondWithError(w, err)
		return
	}

	httphelper.JSON(w, 200, resource)
}

func (c *controllerAPI) DeleteResourceApp(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)

	p, err := c.getProvider(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}

	existing, err := c.providerResource(p, params.ByName("resources_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	appRaw, err := c.appRepo.Get(params.ByName("app_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	app := appRaw.(*ct.App)
	if existing.OwnerAccount != "" && app.OwnerAccount != existing.OwnerAccount {
		httphelper.Forbidden(w, "a resource can only be detached from an app with the same owner_account")
		return
	}
	if !c.requireAppManage(ctx, w, app) {
		return
	}
	resource, err := c.resourceRepo.RemoveApp(params.ByName("resources_id"), app.ID)
	if err != nil {
		respondWithError(w, err)
		return
	}

	httphelper.JSON(w, 200, resource)
}

// listVisibleResources lists every resource (of one provider when
// providerID is set) for cluster admin and the cluster key. A user token gets
// the resources owned by an account it administers, filtered by owner in SQL:
// a fixed number of queries however many resources and accounts exist.
// Unowned (platform) resources stay admin only.
func (c *controllerAPI) listVisibleResources(ctx context.Context, providerID string) ([]*ct.Resource, error) {
	tok := authz.TokenFromContext(ctx)
	if tok == nil || tok.ClusterKey || tok.HasClusterAdmin() {
		if providerID == "" {
			return c.resourceRepo.List()
		}
		return c.resourceRepo.ProviderList(providerID)
	}
	owners, err := c.adminOwnerAccounts(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.resourceRepo.ListOwnedBy(providerID, owners)
	if err != nil {
		return nil, err
	}
	if res == nil {
		res = []*ct.Resource{}
	}
	return res, nil
}

func (c *controllerAPI) canAdminOwner(ctx context.Context, account string) bool {
	return adminsAccount(c.accessForAccount(ctx, account))
}

// addProvisionedResource inserts res. A provider whose plugin asks for an
// instance port gets the lowest free one, picked and inserted in one
// transaction under the allocation lock (CORE-39), so concurrent provisions
// on any number of controllers never share a port. Only the allocated
// ports are read, not every resource (CORE-34-F2).
func (c *controllerAPI) addProvisionedResource(p *ct.Provider, addon *plugin.AddonSpec, res *ct.Resource) error {
	if !providerStampsInstancePort(p, addon) {
		return c.resourceRepo.Add(res)
	}
	return c.resourceRepo.AddWithInstancePort(res, func(used map[int]string) (map[string]string, error) {
		assigned, err := instanceport.AssignEnv(res.ExternalID, res.Env, used)
		if err != nil {
			return nil, ct.ValidationError{Field: "env", Message: err.Error()}
		}
		return assigned, nil
	})
}

// providerStampsInstancePort is true when the installed plugin asks for a
// per-instance port. The platform appliance never gets one. Provider names
// are not special-cased here.
func providerStampsInstancePort(p *ct.Provider, addon *plugin.AddonSpec) bool {
	if p == nil || pgappliance.IsPlatformApplianceURL(p.URL) {
		return false
	}
	return addon != nil && addon.InstancePort
}

// addonForProvider is the add-on spec of the installed plugin that serves
// provider p, or nil. It reads plugin apps only, and a provision reads it
// once: PassApp (send the tenant app name) and InstancePort come from it.
func (c *controllerAPI) addonForProvider(p *ct.Provider) *plugin.AddonSpec {
	if p == nil || c == nil || c.appRepo == nil {
		return nil
	}
	apps, err := c.appRepo.ListPlugins()
	if err != nil {
		return nil
	}
	for _, app := range apps {
		spec := plugin.AddonFromApp(app)
		if spec == nil || !spec.MatchesType(p.Name) {
			continue
		}
		return spec
	}
	return nil
}

func (c *controllerAPI) GetAppResources(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	res, err := c.resourceRepo.AppList(c.getApp(ctx).ID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, res)
}
