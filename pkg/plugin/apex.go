package plugin

import (
	"fmt"
	"strings"

	ct "github.com/flynn/flynn/controller/types"
	router "github.com/flynn/flynn/router/types"
)

// ApexInfo is the app (if any) that currently serves the cluster root domain.
type ApexInfo struct {
	Domain string
	App    *ct.App
	Route  *router.Route
}

type apexLister interface {
	AppRouteList(appID string) ([]*router.Route, error)
}

type apexMutator interface {
	apexLister
	CreateRoute(appID string, route *router.Route) error
	DeleteRoute(appID, routeID string) error
	GetACMEConfig() (*ct.ACMEConfig, error)
	UpdateRoute(appID, routeID string, route *router.Route) error
}

func normalizeApexDomain(domain string) string {
	return strings.ToLower(strings.TrimSpace(domain))
}

func isApexRoute(rt *router.Route, domain string) bool {
	if rt == nil || rt.Type != "http" {
		return false
	}
	return strings.EqualFold(rt.Domain, domain)
}

// LookupApex finds which installed app owns the cluster apex HTTP route.
func LookupApex(lister apexLister, apps []*ct.App, domain string) (*ApexInfo, error) {
	domain = normalizeApexDomain(domain)
	if domain == "" {
		return nil, fmt.Errorf("cluster domain is required")
	}
	if lister == nil {
		return nil, fmt.Errorf("missing controller client")
	}
	info := &ApexInfo{Domain: domain}
	for _, app := range apps {
		if app == nil {
			continue
		}
		routes, err := lister.AppRouteList(app.ID)
		if err != nil {
			return nil, fmt.Errorf("list routes for %s: %w", app.Name, err)
		}
		for _, rt := range routes {
			if isApexRoute(rt, domain) {
				info.App = app
				info.Route = rt
				return info, nil
			}
		}
	}
	return info, nil
}

// AssignApex points the cluster apex domain at app, moving it off any previous owner.
func AssignApex(client apexMutator, apps []*ct.App, domain string, app *ct.App) (*router.Route, error) {
	if app == nil {
		return nil, fmt.Errorf("app is required")
	}
	domain = normalizeApexDomain(domain)
	if domain == "" {
		return nil, fmt.Errorf("cluster domain is required")
	}
	prev, err := LookupApex(client, apps, domain)
	if err != nil {
		return nil, err
	}
	if prev != nil && prev.Route != nil && prev.App != nil && prev.App.ID == app.ID {
		return prev.Route, nil
	}
	if prev != nil && prev.Route != nil && prev.App != nil {
		if err := client.DeleteRoute(prev.App.ID, prev.Route.FormattedID()); err != nil {
			return nil, fmt.Errorf("remove apex from %s: %w", prev.App.Name, err)
		}
	}
	existing, err := client.AppRouteList(app.ID)
	if err != nil {
		return nil, fmt.Errorf("list routes for %s: %w", app.Name, err)
	}
	route := &router.Route{
		Type:          "http",
		Domain:        domain,
		Service:       DefaultRouteService(app, existing, "http"),
		DrainBackends: true,
	}
	if acme, err := client.GetACMEConfig(); err == nil && acme != nil && acme.Enabled {
		d := domain
		route.ManagedCertificateDomain = &d
	}
	if err := client.CreateRoute(app.ID, route); err != nil {
		return nil, fmt.Errorf("create apex route %s: %w", domain, err)
	}
	return route, nil
}

// ClearApex removes the HTTP route for the cluster apex domain.
func ClearApex(client apexMutator, apps []*ct.App, domain string) (*ApexInfo, error) {
	info, err := LookupApex(client, apps, domain)
	if err != nil {
		return nil, err
	}
	if info == nil || info.Route == nil || info.App == nil {
		return info, nil
	}
	if err := client.DeleteRoute(info.App.ID, info.Route.FormattedID()); err != nil {
		return nil, fmt.Errorf("remove apex from %s: %w", info.App.Name, err)
	}
	return info, nil
}
