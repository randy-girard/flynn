package resourceexpose

import (
	"fmt"
	"net/url"
	"strings"

	router "github.com/randy-girard/flynn/router/types"
)

// Spec describes how to expose a Flynn datastore over a TCP(/TLS) route.
type Spec struct {
	Provider string
	// EnvKey is the app env var naming the appliance app (FLYNN_POSTGRES).
	EnvKey string
	// DefaultApp is used when EnvKey is unset (shared cluster appliances).
	DefaultApp string
	// DefaultService is the discoverd service when the appliance app name
	// is not also the service (mariadb vs mysql).
	DefaultService string
	Leader         bool
}

var specs = []Spec{
	{Provider: "postgres", EnvKey: "FLYNN_POSTGRES", DefaultApp: "postgres", DefaultService: "postgres", Leader: true},
	{Provider: "mysql", EnvKey: "FLYNN_MYSQL", DefaultApp: "mariadb", DefaultService: "mariadb", Leader: true},
	{Provider: "mariadb", EnvKey: "FLYNN_MYSQL", DefaultApp: "mariadb", DefaultService: "mariadb", Leader: true},
	{Provider: "mongodb", EnvKey: "FLYNN_MONGO", DefaultApp: "mongodb", DefaultService: "mongodb", Leader: true},
	{Provider: "redis", EnvKey: "FLYNN_REDIS", Leader: true},
	{Provider: "kafka", EnvKey: "FLYNN_KAFKA", DefaultApp: "kafka", DefaultService: "kafka", Leader: true},
	{Provider: "clickhouse", EnvKey: "FLYNN_CLICKHOUSE", DefaultApp: "clickhouse", DefaultService: "clickhouse", Leader: true},
}

// Lookup returns the expose spec for a provider name or alias.
func Lookup(provider string) (Spec, error) {
	name := strings.ToLower(strings.TrimSpace(provider))
	for _, s := range specs {
		if s.Provider == name {
			return s, nil
		}
	}
	return Spec{}, fmt.Errorf("unknown datastore provider %q (postgres, mysql, mongodb, redis, kafka, clickhouse)", provider)
}

// KnownProviders is the list of datastore names resource:expose accepts.
func KnownProviders() []string {
	out := make([]string, 0, len(specs))
	seen := map[string]struct{}{}
	for _, s := range specs {
		if s.Provider == "mariadb" {
			continue
		}
		if _, ok := seen[s.Provider]; ok {
			continue
		}
		seen[s.Provider] = struct{}{}
		out = append(out, s.Provider)
	}
	return out
}

// ResolveAppService returns the appliance app name and discoverd service.
func (s Spec) ResolveAppService(appEnv map[string]string) (app, service string) {
	if appEnv != nil {
		if v := strings.TrimSpace(appEnv[s.EnvKey]); v != "" {
			app = v
		}
	}
	if app == "" {
		app = s.DefaultApp
	}
	service = s.DefaultService
	if service == "" {
		service = app
	}
	return app, service
}

// DefaultHostname is the stable DNS name for an exported datastore, analogous
// to HTTP app routes on the cluster domain.
func DefaultHostname(service, clusterDomain string) string {
	service = strings.Trim(strings.ToLower(service), ".")
	clusterDomain = strings.Trim(strings.ToLower(clusterDomain), ".")
	if service == "" || clusterDomain == "" {
		return service
	}
	if strings.HasSuffix(service, "."+clusterDomain) {
		return service
	}
	return service + "." + clusterDomain
}

// ClusterDomain extracts DEFAULT_ROUTE_DOMAIN from a controller URL
// (https://controller.example.com → example.com).
func ClusterDomain(controllerURL, defaultRouteDomain string) string {
	if d := strings.Trim(strings.ToLower(defaultRouteDomain), "."); d != "" {
		return d
	}
	u, err := url.Parse(controllerURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	return strings.TrimPrefix(host, "controller.")
}

// FirewallExposeCommand is the host-side command that opens the TCP port.
func FirewallExposeCommand(port int) string {
	return fmt.Sprintf("sudo flynn-host firewall:expose %d", port)
}

// FirewallUnexposeCommand closes a previously exposed TCP port.
func FirewallUnexposeCommand(port int) string {
	return fmt.Sprintf("sudo flynn-host firewall:unexpose %d", port)
}

// FindTCPRoute returns the TCP route for service, preferring a matching domain.
func FindTCPRoute(routes []*router.Route, service, domain string) *router.Route {
	var fallback *router.Route
	for _, r := range routes {
		if r == nil || r.Type != "tcp" || r.Service != service {
			continue
		}
		if domain != "" && r.Domain == domain {
			return r
		}
		if fallback == nil {
			fallback = r
		}
	}
	if domain != "" {
		return nil
	}
	return fallback
}

// NewTCPRoute builds a datastore export route. Default TLS mode is passthrough
// so postgres/mysql protocol SSL still works; terminate wraps TLS at the router.
func NewTCPRoute(service, domain, tlsMode string, port int, leader, autoTLS bool) (*router.Route, error) {
	mode := router.NormalizeTLSMode(tlsMode)
	if tlsMode == "" {
		mode = router.TLSModePassthrough
	}
	if !router.ValidTLSMode(mode) {
		return nil, fmt.Errorf("invalid tls mode %q (off, passthrough, terminate)", tlsMode)
	}
	if autoTLS && mode != router.TLSModeTerminate {
		mode = router.TLSModeTerminate
	}
	route := &router.Route{
		Type:          "tcp",
		Service:       service,
		Port:          int32(port),
		Leader:        leader,
		DrainBackends: true,
		Domain:        domain,
		TLSMode:       mode,
	}
	if autoTLS && domain != "" {
		d := domain
		route.ManagedCertificateDomain = &d
	}
	return route, nil
}
