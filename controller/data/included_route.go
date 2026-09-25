package data

import (
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
	router "github.com/randy-girard/flynn/router/types"
)

// IncludedHTTPRouteDomain is the cluster hostname created with a user app.
func IncludedHTTPRouteDomain(appName, defaultDomain string) string {
	appName = strings.TrimSpace(appName)
	defaultDomain = strings.TrimSpace(defaultDomain)
	if appName == "" || defaultDomain == "" {
		return ""
	}
	return appName + "." + defaultDomain
}

// HTTPRoutePath normalizes an empty HTTP path to "/".
func HTTPRoutePath(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

// IsIncludedHTTPRoute reports whether route is the undeletable cluster hostname
// for app ({app}.{DEFAULT_ROUTE_DOMAIN} at path /).
func IsIncludedHTTPRoute(route *router.Route, app *ct.App, defaultDomain string) bool {
	if route == nil || app == nil || app.System() {
		return false
	}
	want := IncludedHTTPRouteDomain(app.Name, defaultDomain)
	if want == "" {
		return false
	}
	typ := route.Type
	if typ == "" {
		typ = "http"
	}
	if typ != "http" {
		return false
	}
	if HTTPRoutePath(route.Path) != "/" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(route.Domain), want)
}

// MarkIncludedRoutes sets Route.Included on each matching route.
func MarkIncludedRoutes(app *ct.App, defaultDomain string, routes ...*router.Route) {
	for _, route := range routes {
		if route == nil {
			continue
		}
		route.Included = IsIncludedHTTPRoute(route, app, defaultDomain)
	}
}

// DefaultDomain is DEFAULT_ROUTE_DOMAIN for this controller.
func (r *AppRepo) DefaultDomain() string {
	return r.defaultDomain
}

func (r *AppRepo) newIncludedRoute(app *ct.App) *router.Route {
	return (&router.HTTPRoute{
		ParentRef:     ct.RouteParentRefPrefix + app.ID,
		Domain:        IncludedHTTPRouteDomain(app.Name, r.defaultDomain),
		Service:       app.Name + "-web",
		DrainBackends: true,
	}).ToRoute()
}

// EnsureIncludedRoute creates the cluster hostname if it is missing, then marks
// included routes. System apps and an empty DEFAULT_ROUTE_DOMAIN are skipped.
func (r *AppRepo) EnsureIncludedRoute(app *ct.App, routes []*router.Route) ([]*router.Route, error) {
	if app == nil || app.System() || r.defaultDomain == "" {
		MarkIncludedRoutes(app, r.defaultDomain, routes...)
		return routes, nil
	}
	for _, route := range routes {
		if IsIncludedHTTPRoute(route, app, r.defaultDomain) {
			MarkIncludedRoutes(app, r.defaultDomain, routes...)
			return routes, nil
		}
	}
	route := r.newIncludedRoute(app)
	if err := r.routes.Add(route); err != nil {
		if err == ErrRouteConflict {
			MarkIncludedRoutes(app, r.defaultDomain, routes...)
			return routes, nil
		}
		return routes, err
	}
	routes = append(routes, route)
	MarkIncludedRoutes(app, r.defaultDomain, routes...)
	return routes, nil
}
