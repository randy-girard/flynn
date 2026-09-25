package main

import (
	"net/http"
	"strings"

	"github.com/randy-girard/flynn/controller/access"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	router "github.com/randy-girard/flynn/router/types"
	"golang.org/x/net/context"
)

func (c *controllerAPI) prepareNewApp(ctx context.Context, app *ct.App) error {
	if c == nil || c.tenancy == nil || app == nil {
		return nil
	}
	tok := authz.TokenFromContext(ctx)
	if tok != nil && tok.UserID != "" && app.CreatedBy == "" {
		app.CreatedBy = tok.UserID
	}
	if app.OwnerAccount == "" && tok != nil && !tok.ClusterKey && tok.UserID != "" {
		app.OwnerAccount = "user:" + tok.UserID
	}
	if app.OwnerAccount == "" {
		return nil
	}
	if err := c.rejectIfSuspended(app.OwnerAccount); err != nil {
		return err
	}
	usage, err := c.tenancy.Usage(app.OwnerAccount)
	if err != nil {
		return err
	}
	return c.quotaAllows(app.OwnerAccount, usage.Apps+1, 0, 0, 0, 0)
}

func (c *controllerAPI) filterVisibleApps(ctx context.Context, list interface{}) interface{} {
	apps, ok := list.([]*ct.App)
	if !ok || c == nil {
		return list
	}
	tok := authz.TokenFromContext(ctx)
	if tok == nil || tok.ClusterKey || tok.HasClusterAdmin() {
		return apps
	}
	var out []*ct.App
	for _, app := range apps {
		if !appVisible(tok, app) {
			continue
		}
		out = append(out, app)
	}
	if out == nil {
		out = []*ct.App{}
	}
	return out
}

func appVisible(tok *authorizer.Token, app *ct.App) bool {
	if tok == nil || app == nil {
		return false
	}
	named := false
	for _, g := range tok.AppGrants {
		if g.AppID == app.ID || g.AppID == app.Name {
			if authz.HasAppPermission(g.Permissions, "") {
				named = true
			}
		}
	}
	if app.OwnerAccount == "" {
		return named
	}
	return named
}

func (c *controllerAPI) rejectIfSuspended(account string) error {
	if c == nil || c.tenancy == nil || account == "" {
		return nil
	}
	suspended, err := c.tenancy.AccountSuspended(account)
	if err != nil {
		return err
	}
	if suspended {
		return ct.ValidationError{Message: "account is suspended"}
	}
	if id, ok := splitUserAccount(account); ok {
		u, err := c.tenancy.GetUser(id)
		if err == data.ErrNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		if u.Suspended || u.Disabled {
			return ct.ValidationError{Message: "account is suspended"}
		}
	}
	return nil
}

func splitUserAccount(account string) (string, bool) {
	const prefix = "user:"
	if len(account) > len(prefix) && account[:len(prefix)] == prefix {
		return account[len(prefix):], true
	}
	return "", false
}

func (c *controllerAPI) quotaAllows(account string, apps, procs, memoryMB, resources, collaborators int) error {
	if c == nil || c.tenancy == nil || account == "" {
		return nil
	}
	settings, err := c.tenancy.GetSettings()
	if err != nil {
		return err
	}
	stored, err := c.tenancy.GetQuota(account)
	if err != nil && err != data.ErrNotFound {
		return err
	}
	var explicit *tenancy.Limits
	if stored != nil {
		explicit = quotaLimits(stored)
	}
	limits, err := tenancy.EffectiveLimits(settings.Mode, explicit)
	if err != nil {
		return err
	}
	checks := []struct {
		name  string
		limit *int
		used  int
	}{
		{"max_apps", limits.MaxApps, apps},
		{"max_processes", limits.MaxProcesses, procs},
		{"max_memory_mb", limits.MaxMemoryMB, memoryMB},
		{"max_resources", limits.MaxResources, resources},
		{"max_collaborators", limits.MaxCollaborators, collaborators},
	}
	for _, check := range checks {
		if check.used == 0 {
			continue
		}
		if tenancy.Exceeds(check.limit, check.used) {
			return ct.ValidationError{Field: check.name, Message: "account quota exceeded"}
		}
	}
	return nil
}

func (c *controllerAPI) hostedTenantSafe(ctx context.Context, p *ct.Provider) error {
	if c == nil || c.tenancy == nil || p == nil || p.TenantSafe {
		return nil
	}
	settings, err := c.tenancy.GetSettings()
	if err != nil {
		return err
	}
	if settings.Mode != tenancy.ModeHosted {
		return nil
	}
	tok := authz.TokenFromContext(ctx)
	if tok == nil || tok.ClusterKey || tok.HasClusterAdmin() {
		return nil
	}
	return ct.ValidationError{Field: "provider", Message: "provider is not tenant_safe; hosted tenants cannot provision it"}
}

func (c *controllerAPI) requireAppManage(ctx context.Context, w http.ResponseWriter, app *ct.App) bool {
	res := c.accessFor(ctx, app)
	if access.Has(res.Permissions, access.PermAppWrite) || access.Has(res.Permissions, access.PermAppAdmin) {
		return true
	}
	tok := authz.TokenFromContext(ctx)
	if tok != nil && (tok.ClusterKey || tok.HasClusterAdmin()) {
		return true
	}
	// Keep explicit grant tokens working before membership rows exist.
	if tok != nil && (authz.HasAppPermission(grantPerms(tok, app), authz.PermAppWrite) || authz.HasAppPermission(grantPerms(tok, app), authz.PermAppAdmin)) {
		return true
	}
	httphelper.Forbidden(w, "admin or manage on the app is required")
	return false
}

func grantPerms(tok *authorizer.Token, app *ct.App) []string {
	if tok == nil || app == nil {
		return nil
	}
	var out []string
	for _, g := range tok.AppGrants {
		if g.AppID == app.ID || g.AppID == app.Name {
			out = append(out, g.Permissions...)
		}
	}
	return out
}

func (c *controllerAPI) checkHostname(ctx context.Context, app *ct.App, hostname string) error {
	if c == nil || c.tenancy == nil || app == nil || hostname == "" {
		return nil
	}
	settings, err := c.tenancy.GetSettings()
	if err != nil {
		return err
	}
	var verified []string
	if app.OwnerAccount != "" {
		verified, err = c.tenancy.VerifiedHostnames(app.OwnerAccount)
		if err != nil {
			return err
		}
	}
	domain := ""
	if c.appRepo != nil {
		domain = c.appRepo.DefaultDomain()
	}
	if err := tenancy.HostnameAllowed(settings.Mode, hostname, app.Name, domain, verified); err != nil {
		return ct.ValidationError{Field: "domain", Message: err.Error()}
	}
	return nil
}

func existingProcesses(c *controllerAPI, appID, releaseID string) map[string]int {
	if c == nil || c.formationRepo == nil {
		return nil
	}
	f, err := c.formationRepo.Get(appID, releaseID)
	if err != nil || f == nil {
		return nil
	}
	return f.Processes
}

func (c *controllerAPI) filterRoutes(ctx context.Context, routes []*router.Route) []*router.Route {
	tok := authz.TokenFromContext(ctx)
	if tok == nil || tok.ClusterKey || tok.HasClusterAdmin() || c.appRepo == nil {
		return routes
	}
	var out []*router.Route
	for _, route := range routes {
		appID := strings.TrimPrefix(route.ParentRef, ct.RouteParentRefPrefix)
		if appID == "" || appID == route.ParentRef {
			continue
		}
		raw, err := c.appRepo.Get(appID)
		if err != nil {
			continue
		}
		app := raw.(*ct.App)
		if appVisible(tok, app) {
			out = append(out, route)
		}
	}
	if out == nil {
		out = []*router.Route{}
	}
	return out
}

func (c *controllerAPI) enforceScale(ctx context.Context, app *ct.App, oldCounts, newCounts map[string]int) error {
	if c == nil || c.tenancy == nil || app == nil || app.OwnerAccount == "" {
		return nil
	}
	if tenancy.ScaleIncreases(oldCounts, newCounts) {
		if err := c.rejectIfSuspended(app.OwnerAccount); err != nil {
			return err
		}
	}
	usage, err := c.tenancy.Usage(app.OwnerAccount)
	if err != nil {
		return err
	}
	oldN, newN := 0, 0
	for _, n := range oldCounts {
		if n > 0 {
			oldN += n
		}
	}
	for _, n := range newCounts {
		if n > 0 {
			newN += n
		}
	}
	procs := usage.Processes - oldN + newN
	if procs < 0 {
		procs = 0
	}
	return c.quotaAllows(app.OwnerAccount, 0, procs, 0, 0, 0)
}
