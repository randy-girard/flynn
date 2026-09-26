package main

import (
	"net/http"
	"sync"

	"github.com/randy-girard/flynn/controller/access"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/schema"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/instanceport"
	"github.com/randy-girard/flynn/pkg/pgappliance"
	"github.com/randy-girard/flynn/pkg/resource"
	"golang.org/x/net/context"
)

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
	if len(rr.Apps) > 0 {
		app, err := c.appRepo.Get(rr.Apps[0])
		if err != nil {
			respondWithError(w, err)
			return
		}
		target = app.(*ct.App)
		if !c.requireAppManage(ctx, w, target) {
			return
		}
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
	} else if tok := authz.TokenFromContext(ctx); tok != nil && !tok.ClusterKey && !tok.HasClusterAdmin() {
		httphelper.Forbidden(w, "provisioning requires an app you can manage")
		return
	}

	var config []byte
	if rr.Config != nil {
		config = *rr.Config
	} else {
		config = []byte(`{}`)
	}
	// The built-in appliance is the platform database. Bootstrap still creates
	// the controller and blobstore databases (system apps, platform marker).
	// A tenant app never reaches the appliance, so no tenant role is created.
	if pgappliance.IsPlatformApplianceURL(p.URL) {
		body, err := pgappliance.SystemProvisionBody(target != nil && target.System())
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
	env, err := c.stampInstancePort(p, data.ID, data.Env)
	if err != nil {
		respondWithError(w, err)
		return
	}

	res := &ct.Resource{
		ProviderID:   p.ID,
		ExternalID:   data.ID,
		Env:          env,
		Apps:         rr.Apps,
		OwnerAccount: owner,
	}

	if err := schema.Validate(res); err != nil {
		respondWithError(w, err)
		return
	}

	if err := c.resourceRepo.Add(res); err != nil {
		// TODO: attempt to "rollback" provisioning
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, res)
}

func (c *controllerAPI) GetProviderResources(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	p, err := c.getProvider(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}

	res, err := c.resourceRepo.ProviderList(p.ID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, c.filterResources(ctx, res))
}

func (c *controllerAPI) GetResources(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	res, err := c.resourceRepo.List()
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, res)
}

func (c *controllerAPI) GetResource(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)

	_, err := c.getProvider(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}

	res, err := c.resourceRepo.Get(params.ByName("resources_id"))
	if err != nil {
		respondWithError(w, err)
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

	if err := c.resourceRepo.Add(&resource); err != nil {
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
	res, err := c.resourceRepo.Get(id)
	if err != nil {
		logger.Error("getting resource error", "err", err)
		respondWithError(w, err)
		return
	}

	if res.OwnerAccount != "" {
		if !c.canAdminAccount(ctx, w, res.OwnerAccount) {
			return
		}
	}
	logger.Info("deprovisioning", "url", p.URL, "external.id", res.ExternalID)
	if err := resource.Deprovision(p.URL, res.ExternalID); err != nil {
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

	_, err := c.getProvider(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}

	resource, err := c.resourceRepo.Get(params.ByName("resources_id"))
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
	if resource.OwnerAccount != "" && app.OwnerAccount != resource.OwnerAccount {
		httphelper.Forbidden(w, "a resource can only be attached to an app with the same owner_account")
		return
	}
	if !c.requireAppManage(ctx, w, app) {
		return
	}
	if resource.OwnerAccount == "" && app.OwnerAccount != "" && c.tenancy != nil {
		if err := c.tenancy.SetResourceOwner(resource.ID, app.OwnerAccount); err != nil {
			respondWithError(w, err)
			return
		}
	}
	resource, err = c.resourceRepo.AddApp(params.ByName("resources_id"), params.ByName("app_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}

	httphelper.JSON(w, 200, resource)
}

func (c *controllerAPI) DeleteResourceApp(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)

	_, err := c.getProvider(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}

	existing, err := c.resourceRepo.Get(params.ByName("resources_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	if existing.OwnerAccount != "" && !c.canAdminAccount(ctx, w, existing.OwnerAccount) {
		return
	}
	resource, err := c.resourceRepo.RemoveApp(params.ByName("resources_id"), params.ByName("app_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}

	httphelper.JSON(w, 200, resource)
}

func (c *controllerAPI) filterResources(ctx context.Context, res []*ct.Resource) []*ct.Resource {
	tok := authz.TokenFromContext(ctx)
	if tok == nil || tok.ClusterKey || tok.HasClusterAdmin() {
		return res
	}
	var out []*ct.Resource
	for _, item := range res {
		if item.OwnerAccount == "" {
			continue
		}
		if c.canAdminOwner(ctx, item.OwnerAccount) {
			out = append(out, item)
		}
	}
	if out == nil {
		out = []*ct.Resource{}
	}
	return out
}

func (c *controllerAPI) canAdminOwner(ctx context.Context, account string) bool {
	res := c.accessForAccount(ctx, account)
	return access.Has(res.Permissions, access.PermAppAdmin) || res.ImplicitOwner || res.OrgManager
}

var instancePortAssign sync.Mutex

func (c *controllerAPI) stampInstancePort(p *ct.Provider, externalID string, env map[string]string) (map[string]string, error) {
	if p == nil || c.resourceRepo == nil || !instanceport.IsDatastore(p.Name) || pgappliance.IsPlatformApplianceURL(p.URL) {
		return env, nil
	}
	instancePortAssign.Lock()
	defer instancePortAssign.Unlock()
	list, err := c.resourceRepo.List()
	if err != nil {
		return nil, err
	}
	used := map[int]string{}
	for _, r := range list {
		if r == nil || r.Env == nil {
			continue
		}
		port, ok := instanceport.ParsePort(r.Env[instanceport.EnvPort])
		if !ok {
			continue
		}
		id := r.ExternalID
		if id == "" {
			id = r.ID
		}
		used[port] = id
	}
	assigned, err := instanceport.AssignEnv(externalID, env, used)
	if err != nil {
		return nil, ct.ValidationError{Field: "env", Message: err.Error()}
	}
	return assigned, nil
}

func (c *controllerAPI) GetAppResources(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	res, err := c.resourceRepo.AppList(c.getApp(ctx).ID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, res)
}
