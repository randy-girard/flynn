package plugin

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
	router "github.com/randy-girard/flynn/router/types"
)

// LookupPluginApp finds an installed plugin by app name or CLI command.
func LookupPluginApp(apps []*ct.App, name string) (*ct.App, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("plugin name is required")
	}
	var byCLI *ct.App
	for _, app := range apps {
		if app == nil || !app.Plugin() {
			continue
		}
		if app.Name == name {
			return app, nil
		}
		if c := CLIFromApp(app); c != nil && c.Command == name && byCLI == nil {
			byCLI = app
		}
	}
	if byCLI != nil {
		return byCLI, nil
	}
	return nil, fmt.Errorf("%s is not an installed plugin; see flynn-host plugin:list", name)
}

// PluginRouter manages HTTP/TCP routes on an installed plugin app. It is the
// host-side counterpart of `flynn -a <plugin> route` (same ACME attachment as
// `flynn route add http --auto-tls`).
type PluginRouter struct {
	Client RouteClient
	Stdout io.Writer
}

func NewPluginRouter(client RouteClient, stdout io.Writer) *PluginRouter {
	return &PluginRouter{Client: client, Stdout: stdout}
}

// HTTPRouteOptions is `flynn route add http` scoped to a plugin app.
type HTTPRouteOptions struct {
	Domain            string
	Service           string
	Port              int
	AutoTLS           bool
	TLSCert           string
	TLSKey            string
	Sticky            bool
	Leader            bool
	Path              string
	DrainBackends     bool
	DisableKeepAlives bool
}

// HTTPRouteUpdate is `flynn route update` for an HTTP plugin route.
type HTTPRouteUpdate struct {
	Service           string
	AutoTLS           bool
	NoAutoTLS         bool
	TLSCert           string
	TLSKey            string
	SetSticky         bool
	Sticky            bool
	SetLeader         bool
	Leader            bool
	SetKeepAlives     bool
	DisableKeepAlives bool
}

// TCPRouteOptions is `flynn route add tcp` scoped to a plugin app.
type TCPRouteOptions struct {
	Service       string
	Port          int
	Leader        bool
	DrainBackends bool
}

func (r *PluginRouter) List(appID string) ([]*router.Route, error) {
	if r == nil || r.Client == nil {
		return nil, fmt.Errorf("missing controller client")
	}
	return r.Client.AppRouteList(appID)
}

func (r *PluginRouter) AddHTTP(app *ct.App, opts HTTPRouteOptions) (*router.Route, error) {
	if app == nil {
		return nil, fmt.Errorf("plugin app is required")
	}
	if opts.AutoTLS && (opts.TLSCert != "" || opts.TLSKey != "") {
		return nil, fmt.Errorf("--auto-tls cannot be used with --tls-cert or --tls-key")
	}
	existing, err := r.List(app.ID)
	if err != nil {
		return nil, fmt.Errorf("list routes for %s: %w", app.Name, err)
	}
	domain := strings.TrimSpace(opts.Domain)
	path := opts.Path
	var prev *router.Route
	if domain == "" {
		httpRoutes := httpPluginRoutes(existing)
		switch len(httpRoutes) {
		case 0:
			return nil, fmt.Errorf("domain is required (%s has no HTTP route)", app.Name)
		case 1:
			prev = httpRoutes[0]
			domain = prev.Domain
			if path == "" {
				path = prev.Path
			}
		default:
			return nil, fmt.Errorf("domain is required (%s has %d HTTP routes; list them with flynn-host plugin %s route)", app.Name, len(httpRoutes), app.Name)
		}
	} else {
		u, err := url.Parse("http://" + domain)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("failed to parse %s as URL", domain)
		}
		domain = u.Host
		if path == "" {
			path = u.Path
		}
		prev = findHTTPPluginRoute(existing, domain)
	}
	service := strings.TrimSpace(opts.Service)
	if service == "" {
		if prev != nil && prev.Service != "" {
			service = prev.Service
		} else {
			service = DefaultRouteService(app, existing, "http")
		}
	}
	if prev != nil {
		if service != "" {
			prev.Service = service
		}
		if opts.Port != 0 {
			prev.Port = int32(opts.Port)
		}
		if opts.Sticky {
			prev.Sticky = true
		}
		if opts.Leader {
			prev.Leader = true
		}
		if opts.DisableKeepAlives {
			prev.DisableKeepAlives = true
		}
		if opts.TLSCert != "" || opts.TLSKey != "" {
			prev.Certificate = nil
			prev.LegacyTLSCert = opts.TLSCert
			prev.LegacyTLSKey = opts.TLSKey
			prev.ManagedCertificateDomain = nil
		}
		if opts.AutoTLS {
			if err := r.enableAutoTLS(prev); err != nil {
				return nil, err
			}
		}
		if err := r.Client.UpdateRoute(app.ID, prev.FormattedID(), prev); err != nil {
			return nil, fmt.Errorf("update route %s: %w", domain, err)
		}
		return prev, nil
	}
	route := &router.Route{
		Type:              "http",
		Domain:            domain,
		Service:           service,
		Port:              int32(opts.Port),
		Sticky:            opts.Sticky,
		Leader:            opts.Leader,
		Path:              path,
		DrainBackends:     opts.DrainBackends,
		DisableKeepAlives: opts.DisableKeepAlives,
		LegacyTLSCert:     opts.TLSCert,
		LegacyTLSKey:      opts.TLSKey,
	}
	if opts.AutoTLS {
		if err := r.enableAutoTLS(route); err != nil {
			return nil, err
		}
	}
	if err := r.Client.CreateRoute(app.ID, route); err != nil {
		return nil, fmt.Errorf("create route %s: %w", domain, err)
	}
	return route, nil
}

func (r *PluginRouter) AddTCP(app *ct.App, opts TCPRouteOptions) (*router.Route, error) {
	if app == nil {
		return nil, fmt.Errorf("plugin app is required")
	}
	existing, err := r.List(app.ID)
	if err != nil {
		return nil, fmt.Errorf("list routes for %s: %w", app.Name, err)
	}
	service := strings.TrimSpace(opts.Service)
	if service == "" {
		service = DefaultRouteService(app, existing, "tcp")
	}
	route := &router.Route{
		Type:          "tcp",
		Service:       service,
		Port:          int32(opts.Port),
		Leader:        opts.Leader,
		DrainBackends: opts.DrainBackends,
	}
	if err := r.Client.CreateRoute(app.ID, route); err != nil {
		return nil, fmt.Errorf("create tcp route: %w", err)
	}
	return route, nil
}

func (r *PluginRouter) UpdateHTTP(app *ct.App, id string, opts HTTPRouteUpdate) (*router.Route, error) {
	if app == nil {
		return nil, fmt.Errorf("plugin app is required")
	}
	if opts.AutoTLS && opts.NoAutoTLS {
		return nil, fmt.Errorf("--auto-tls and --no-auto-tls cannot be used together")
	}
	route, err := r.get(app.ID, id)
	if err != nil {
		return nil, err
	}
	if route.Type != "http" {
		return nil, fmt.Errorf("route %s is %s, not http", id, route.Type)
	}
	if opts.Service != "" {
		route.Service = opts.Service
	}
	route.Certificate = nil
	if opts.TLSCert != "" || opts.TLSKey != "" {
		route.LegacyTLSCert = opts.TLSCert
		route.LegacyTLSKey = opts.TLSKey
	}
	if opts.AutoTLS {
		if route.LegacyTLSCert != "" || route.LegacyTLSKey != "" {
			return nil, fmt.Errorf("--auto-tls cannot be used with --tls-cert or --tls-key")
		}
		if err := r.enableAutoTLS(route); err != nil {
			return nil, err
		}
	} else if opts.NoAutoTLS {
		route.ManagedCertificateDomain = nil
	}
	if opts.SetSticky {
		route.Sticky = opts.Sticky
	}
	if opts.SetLeader {
		route.Leader = opts.Leader
	}
	if opts.SetKeepAlives {
		route.DisableKeepAlives = opts.DisableKeepAlives
	}
	if err := r.Client.UpdateRoute(app.ID, route.FormattedID(), route); err != nil {
		return nil, fmt.Errorf("update route %s: %w", id, err)
	}
	return route, nil
}

// TCPRouteUpdate is `flynn route update` for a TCP plugin route.
type TCPRouteUpdate struct {
	Service   string
	SetLeader bool
	Leader    bool
}

func (r *PluginRouter) UpdateTCP(app *ct.App, id string, opts TCPRouteUpdate) (*router.Route, error) {
	if app == nil {
		return nil, fmt.Errorf("plugin app is required")
	}
	route, err := r.get(app.ID, id)
	if err != nil {
		return nil, err
	}
	if route.Type != "tcp" {
		return nil, fmt.Errorf("route %s is %s, not tcp", id, route.Type)
	}
	if opts.Service != "" {
		route.Service = opts.Service
	}
	if opts.SetLeader {
		route.Leader = opts.Leader
	}
	if err := r.Client.UpdateRoute(app.ID, route.FormattedID(), route); err != nil {
		return nil, fmt.Errorf("update route %s: %w", id, err)
	}
	return route, nil
}

func (r *PluginRouter) Remove(app *ct.App, id string) error {
	if app == nil {
		return fmt.Errorf("plugin app is required")
	}
	if r == nil || r.Client == nil {
		return fmt.Errorf("missing controller client")
	}
	if err := r.Client.DeleteRoute(app.ID, id); err != nil {
		return fmt.Errorf("remove route %s: %w", id, err)
	}
	return nil
}

func (r *PluginRouter) get(appID, id string) (*router.Route, error) {
	routes, err := r.List(appID)
	if err != nil {
		return nil, err
	}
	for _, rt := range routes {
		if rt == nil {
			continue
		}
		if rt.ID == id || rt.FormattedID() == id {
			return rt, nil
		}
	}
	return nil, fmt.Errorf("route %s not found", id)
}

func (r *PluginRouter) enableAutoTLS(route *router.Route) error {
	return (&Installer{RouteClient: r.Client, Stdout: r.Stdout}).attachAutoTLS(route, true)
}

// DefaultRouteService is the plugin service name for a new route. Plugins
// typically advertise their app name (dashboard), not APP-web.
func DefaultRouteService(app *ct.App, routes []*router.Route, typ string) string {
	var svc string
	for _, rt := range routes {
		if rt == nil || rt.Service == "" {
			continue
		}
		if typ != "" && rt.Type != "" && rt.Type != typ {
			continue
		}
		if svc == "" {
			svc = rt.Service
			continue
		}
		if svc != rt.Service {
			svc = ""
			break
		}
	}
	if svc != "" {
		return svc
	}
	if app != nil && app.Name != "" {
		return app.Name
	}
	return ""
}

func httpPluginRoutes(routes []*router.Route) []*router.Route {
	var out []*router.Route
	for _, rt := range routes {
		if rt != nil && rt.Type == "http" {
			out = append(out, rt)
		}
	}
	return out
}

func findHTTPPluginRoute(routes []*router.Route, domain string) *router.Route {
	for _, rt := range routes {
		if rt != nil && rt.Type == "http" && rt.Domain == domain {
			return rt
		}
	}
	return nil
}
