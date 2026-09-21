package main

import (
	"net/http"
	"strings"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

// Flynn discoverd names for user process types are:
//
//   - gitreceive / buildpack: appName+"-"+type for type "web" and types
//     ending in "-web" (so myapp-web, myapp-admin-web)
//   - docker / container deploys: always appName+"-web", even when the
//     process type is "app"
//
// System apps often register the app name itself (controller, dashboard,
// postgres) or a related name (postgres-api, controller-scheduler,
// status-web). Cluster admins still create those routes; app-scoped
// tokens may not.

type routeServiceDenied string

func (e routeServiceDenied) Error() string { return string(e) }

const (
	errRouteServiceNotOwned routeServiceDenied = "route service must belong to this app"
	errRouteServiceSystem   routeServiceDenied = "cannot create a route to a system app service"
)

// routeServiceMatchesApp reports whether service is owned by appName under
// SEC-007: it matches ^<appName>- (the default <app>-<type> discoverd name)
// or is declared on the current release's process types (ProcessType.Service
// or a port's host.Service.Name).
func routeServiceMatchesApp(appName, service string, release *ct.Release) bool {
	if service == "" || appName == "" {
		return false
	}
	if strings.HasPrefix(service, appName+"-") {
		return true
	}
	return releaseDeclaresService(release, service)
}

func releaseDeclaresService(release *ct.Release, service string) bool {
	if release == nil {
		return false
	}
	for _, proc := range release.Processes {
		if proc.Service == service {
			return true
		}
		for _, p := range proc.Ports {
			if p.Service != nil && p.Service.Name == service {
				return true
			}
		}
	}
	return false
}

// serviceOwnerNameCandidates lists possible owning app names for a discoverd
// service, longest first. Flynn services are "<appName>" or
// "<appName>-<processType>", and app names themselves may contain hyphens.
func serviceOwnerNameCandidates(service string) []string {
	if service == "" {
		return nil
	}
	out := make([]string, 0, strings.Count(service, "-")+1)
	out = append(out, service)
	for i := len(service) - 1; i > 0; i-- {
		if service[i] == '-' {
			out = append(out, service[:i])
		}
	}
	return out
}

func platformAppOwnsService(service string) bool {
	for _, name := range serviceOwnerNameCandidates(service) {
		if authz.IsPlatformAppName(name) {
			return true
		}
	}
	return false
}

func routeServiceCallerIsAdmin(tok *authorizer.Token) bool {
	return tok != nil && tok.HasClusterAdmin()
}

func (c *controllerAPI) currentAppRelease(app *ct.App) (*ct.Release, error) {
	if app == nil || app.ReleaseID == "" {
		return nil, nil
	}
	release, err := c.appRepo.GetRelease(app.ID)
	if err == data.ErrNotFound {
		return nil, nil
	}
	return release, err
}

// lookupServiceOwnerApp returns the app that most specifically owns service
// (longest matching app name), or nil if none exists.
func (c *controllerAPI) lookupServiceOwnerApp(service string) (*ct.App, error) {
	for _, cand := range serviceOwnerNameCandidates(service) {
		obj, err := c.appRepo.Get(cand)
		if err == data.ErrNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		return obj.(*ct.App), nil
	}
	return nil, nil
}

func (c *controllerAPI) routeServiceOwnershipError(ctx context.Context, app *ct.App, service string) error {
	if routeServiceCallerIsAdmin(authz.TokenFromContext(ctx)) {
		return nil
	}
	release, err := c.currentAppRelease(app)
	if err != nil {
		return err
	}
	if !routeServiceMatchesApp(app.Name, service, release) {
		return errRouteServiceNotOwned
	}
	owner, err := c.lookupServiceOwnerApp(service)
	if err != nil {
		return err
	}
	if owner != nil && owner.ID != app.ID {
		if owner.System() {
			return errRouteServiceSystem
		}
		return errRouteServiceNotOwned
	}
	if owner == nil && platformAppOwnsService(service) && !authz.IsPlatformAppName(app.Name) {
		return errRouteServiceSystem
	}
	return nil
}

// enforceRouteServiceOwnership rejects non-admin callers whose route.Service
// is not this app's discoverd name. Returns false if the handler should stop.
func (c *controllerAPI) enforceRouteServiceOwnership(ctx context.Context, w http.ResponseWriter, app *ct.App, service string) bool {
	err := c.routeServiceOwnershipError(ctx, app, service)
	if err == nil {
		return true
	}
	switch err {
	case errRouteServiceNotOwned, errRouteServiceSystem:
		httphelper.Forbidden(w, err.Error())
		return false
	default:
		respondWithError(w, err)
		return false
	}
}
