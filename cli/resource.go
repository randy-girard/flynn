package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/dbruntime"
	"github.com/randy-girard/flynn/pkg/resname"
	"github.com/randy-girard/flynn/pkg/resourceexpose"
	router "github.com/randy-girard/flynn/router/types"
)

func databaseRuntimeCatalog(client controller.Client) (dbruntime.Catalog, error) {
	file, fileErr := dbruntime.Load(dbruntime.Path())
	if client != nil {
		got, err := client.ListDBRuntimes()
		// Prefer the controller catalog. Fall back to a host file only when
		// the cluster copy is empty (for example right after a controller
		// deploy, before flynn-host seeds postgres from the cache).
		if err == nil && got != nil && len(got.Runtimes) > 0 {
			return *got, nil
		}
	}
	if fileErr != nil {
		return dbruntime.Catalog{}, fileErr
	}
	return file, nil
}

// createdResourceMessage is the resource:add success line. Operators grep the
// resource app name (postgresql-concave-48291), not controller UUIDs. --as is shown
// when it was set and differs from that name.
func createdResourceMessage(res *ct.Resource, as string) string {
	as = strings.ToUpper(strings.TrimSpace(as))
	name := ""
	if res != nil {
		name, _ = resname.Identity(res.Env)
		if name == "" && as != "" {
			name = as
		}
		if name == "" {
			name = strings.TrimSpace(res.ExternalID)
		}
		if name == "" {
			name = res.ID
		}
	}
	if name == "" {
		name = as
	}
	if name == "" {
		name = "unknown"
	}
	if as != "" && !strings.EqualFold(as, name) {
		return fmt.Sprintf("Created add-on %s (as %s) and a new release.", name, as)
	}
	return fmt.Sprintf("Created add-on %s and a new release.", name)
}

// resourceStartingMessage is the second addons:create line. Provision returns
// after the job is scheduled. waitCmd is the plugin's wait command (pg:wait).
func resourceStartingMessage(res *ct.Resource, waitCmd string) string {
	name := resourceDisplayName(res)
	if name == "" {
		name = "the instance"
	}
	wait := "flynn addons"
	if cmd := strings.TrimSpace(waitCmd); cmd != "" {
		wait = "flynn " + cmd + " " + name
	}
	return fmt.Sprintf("The instance is starting; check later with %s.", wait)
}

// resourceDisplayName is the isolated instance operators copy from flynn
// resource (postgresql-concave-48291). It is what pg:psql and --follow take.
func resourceDisplayName(res *ct.Resource) string {
	if res == nil {
		return ""
	}
	if name, _ := resname.Identity(res.Env); name != "" {
		return name
	}
	if n := strings.TrimSpace(res.ExternalID); n != "" {
		return n
	}
	return strings.TrimSpace(res.ID)
}

func resourceMatchesRef(res *ct.Resource, ref string) bool {
	ref = strings.TrimSpace(ref)
	if res == nil || ref == "" {
		return false
	}
	if strings.EqualFold(res.ID, ref) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(res.ExternalID), ref) {
		return true
	}
	return strings.EqualFold(resourceDisplayName(res), ref)
}

// resolvePeerRef turns a flynn resource NAME or ID into the isolated app name
// plugins expect for --follow. Empty ref is unchanged.
func resolvePeerRef(client controller.Client, app, provider, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	res, err := lookupProviderResource(client, app, provider, ref)
	if err != nil {
		return "", err
	}
	if name := resourceDisplayName(res); name != "" {
		return name, nil
	}
	return ref, nil
}

func lookupProviderResource(client controller.Client, app, provider, ref string) (*ct.Resource, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("resource is required")
	}
	p, err := client.GetProvider(provider)
	if err != nil {
		return nil, err
	}
	match := func(list []*ct.Resource) []*ct.Resource {
		var out []*ct.Resource
		for _, r := range list {
			if r == nil || r.ProviderID != p.ID {
				continue
			}
			if resourceMatchesRef(r, ref) {
				out = append(out, r)
			}
		}
		return out
	}
	if strings.TrimSpace(app) != "" {
		list, err := client.AppResourceList(app)
		if err != nil {
			return nil, err
		}
		if got := match(list); len(got) == 1 {
			return got[0], nil
		} else if len(got) > 1 {
			return nil, fmt.Errorf("multiple resources match %q; use the ID from flynn resource", ref)
		}
	}
	list, err := client.ResourceList(p.ID)
	if err != nil {
		return nil, err
	}
	got := match(list)
	if len(got) == 1 {
		return got[0], nil
	}
	if len(got) > 1 {
		return nil, fmt.Errorf("multiple resources match %q; use the ID from flynn resource", ref)
	}
	return nil, fmt.Errorf("resource %q not found for %s; see flynn resource", ref, provider)
}

func alreadyAttachedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint") || strings.Contains(msg, "already attached")
}

func appliedAttachmentEnv(client controller.Client, incoming map[string]string, as string, newProvision bool) map[string]*string {
	existing := map[string]string{}
	if release, err := client.GetAppRelease(mustApp()); err == nil && release != nil && release.Env != nil {
		existing = release.Env
	}
	merged := resname.MergeAttachment(existing, incoming, as, newProvision)
	env := make(map[string]*string, len(merged))
	for k, v := range merged {
		s := v
		env[k] = &s
	}
	return env
}

func singleAttachmentEnv(resourceEnv map[string]string, as string) (map[string]string, error) {
	var key, val string
	n := 0
	for k, v := range resourceEnv {
		if strings.HasSuffix(k, "_URL") && v != "" {
			n++
			key, val = k, v
		}
	}
	if n != 1 {
		return nil, fmt.Errorf("postgres attachment expects exactly one *_URL, found %d", n)
	}
	if strings.TrimSpace(as) != "" {
		return map[string]string{resname.PostgresAttachmentURLKey(as, nil): val}, nil
	}
	return map[string]string{key: val}, nil
}

func resolvedAppID(client controller.Client) (string, error) {
	app, err := client.GetApp(mustApp())
	if err != nil {
		return "", err
	}
	if id := strings.TrimSpace(app.ID); id != "" {
		return id, nil
	}
	return mustApp(), nil
}

func appResourcesOrNil(client controller.Client, app string) []*ct.Resource {
	if client == nil || strings.TrimSpace(app) == "" {
		return nil
	}
	list, err := client.AppResourceList(app)
	if err != nil {
		return nil
	}
	return list
}

// resolveRemoveTarget accepts provider+NAME/ID, a unique provider, or a unique
// resource NAME/ID with no provider (flynn resource:remove postgresql-concave-48291).
func resolveRemoveTarget(client controller.Client, app, provider, resource string) (*ct.Resource, string, error) {
	provider = strings.TrimSpace(provider)
	resource = strings.TrimSpace(resource)
	if provider == "" && resource == "" {
		return nil, "", fmt.Errorf("resource NAME or ID is required; see flynn resource")
	}
	if resource != "" {
		if provider == "" {
			res, err := lookupAppResource(client, app, resource)
			if err != nil {
				return nil, "", err
			}
			p, err := client.GetProvider(res.ProviderID)
			if err != nil {
				return nil, "", err
			}
			return res, p.Name, nil
		}
		res, err := lookupProviderResource(client, app, provider, resource)
		if err != nil {
			return nil, "", err
		}
		return res, provider, nil
	}
	if p, err := client.GetProvider(provider); err == nil && p != nil {
		id, err := resolveResource(provider, client)
		if err != nil {
			return nil, "", err
		}
		res, err := lookupProviderResource(client, app, provider, id)
		if err != nil {
			return nil, "", err
		}
		return res, provider, nil
	}
	res, err := lookupAppResource(client, app, provider)
	if err != nil {
		return nil, "", err
	}
	p, err := client.GetProvider(res.ProviderID)
	if err != nil {
		return nil, "", err
	}
	return res, p.Name, nil
}

// normalizeResourceArgs treats a single positional as the resource NAME or ID.
// docopt assigns that token to <provider> when <resource> is empty.
func normalizeResourceArgs(provider, resource string) (string, string, error) {
	provider = strings.TrimSpace(provider)
	resource = strings.TrimSpace(resource)
	if provider != "" && resource == "" {
		resource = provider
		provider = ""
	}
	if resource == "" {
		return "", "", fmt.Errorf("resource NAME or ID is required; see flynn resource")
	}
	return provider, resource, nil
}

// resolveNamedResource finds a resource by provider plus NAME/ID, or by a
// unique NAME/ID alone (flynn resource:attach mysql-valley-77924).
func resolveNamedResource(client controller.Client, app, provider, resource string) (*ct.Resource, string, error) {
	provider, resource, err := normalizeResourceArgs(provider, resource)
	if err != nil {
		return nil, "", err
	}
	if provider != "" {
		res, err := lookupProviderResource(client, app, provider, resource)
		if err != nil {
			return nil, "", err
		}
		return res, provider, nil
	}
	res, err := lookupResourceByRef(client, resource)
	if err != nil {
		return nil, "", err
	}
	p, err := client.GetProvider(res.ProviderID)
	if err != nil {
		return nil, "", err
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = p.ID
	}
	return res, name, nil
}

func lookupResourceByRef(client controller.Client, ref string) (*ct.Resource, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("resource is required")
	}
	list, err := client.ResourceListAll()
	if err != nil {
		return nil, err
	}
	var got []*ct.Resource
	for _, r := range list {
		if resourceMatchesRef(r, ref) {
			got = append(got, r)
		}
	}
	if len(got) == 1 {
		return got[0], nil
	}
	if len(got) > 1 {
		return nil, fmt.Errorf("multiple resources match %q; pass the provider and the ID from flynn resource", ref)
	}
	return nil, fmt.Errorf("resource %q not found; see flynn resource", ref)
}

func lookupAppResource(client controller.Client, app, ref string) (*ct.Resource, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("resource is required")
	}
	list, err := client.AppResourceList(app)
	if err != nil {
		return nil, err
	}
	var got []*ct.Resource
	for _, r := range list {
		if resourceMatchesRef(r, ref) {
			got = append(got, r)
		}
	}
	if len(got) == 1 {
		return got[0], nil
	}
	if len(got) > 1 {
		return nil, fmt.Errorf("multiple resources match %q; use the ID from flynn resource", ref)
	}
	return nil, fmt.Errorf("resource %q not found; see flynn resource", ref)
}

func resourceFollowerNames(leader *ct.Resource, list []*ct.Resource, keys []string) []string {
	if leader == nil || len(keys) == 0 {
		return nil
	}
	name := resourceDisplayName(leader)
	var out []string
	seen := map[string]bool{}
	for _, r := range list {
		if r == nil || r.ID == leader.ID || !resourceFollowsLeader(r, name, keys) {
			continue
		}
		n := resourceDisplayName(r)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

func resourceFollowsLeader(res *ct.Resource, leaderName string, keys []string) bool {
	if res == nil || res.Env == nil || leaderName == "" {
		return false
	}
	env := res.Env
	for _, k := range keys {
		if strings.EqualFold(strings.TrimSpace(env[k]), leaderName) {
			return true
		}
	}
	return false
}

func resolveResource(provider string, client controller.Client) (string, error) {
	resources, err := client.AppResourceList(mustApp())
	if err != nil {
		return "", err
	}
	var matched []*ct.Resource
	for _, r := range resources {
		p, err := client.GetProvider(r.ProviderID)
		if err != nil {
			return "", err
		}
		if r.ProviderID == provider || p.Name == provider {
			matched = append(matched, r)
		}
	}
	if len(matched) != 1 {
		return "", fmt.Errorf("App has more than one resource for %s, specify NAME or ID from flynn resource", provider)
	}
	return matched[0].ID, nil
}

func lookupAttachedResource(client controller.Client, app, ref string) (*ct.Resource, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("resource is required; use the NAME or ID from flynn resource")
	}
	list, err := client.AppResourceList(app)
	if err != nil {
		return nil, err
	}
	var matched []*ct.Resource
	for _, r := range list {
		if resourceMatchesRef(r, ref) {
			matched = append(matched, r)
		}
	}
	if len(matched) == 1 {
		return matched[0], nil
	}
	if len(matched) > 1 {
		return nil, fmt.Errorf("multiple resources match %q; use the ID from flynn resource", ref)
	}
	return nil, fmt.Errorf("resource %q not found on this app; use the NAME or ID from flynn resource, not the provider", ref)
}

func runResourceExpose(args *docopt.Args, client controller.Client) error {
	res, err := lookupAttachedResource(client, mustApp(), args.String["<resource>"])
	if err != nil {
		return err
	}
	spec, service, err := resourceexpose.ServiceForResource(res.Env)
	if err != nil {
		return err
	}
	if service == "" {
		return fmt.Errorf("resource %s has no discoverd service", resourceDisplayName(res))
	}

	domain := strings.TrimSpace(args.String["--domain"])
	if domain == "" {
		clusterDomain := clusterRouteDomain(client)
		domain = resourceexpose.DefaultHostname(service, clusterDomain)
	}

	port := 0
	if args.String["--port"] != "" {
		port, err = strconv.Atoi(args.String["--port"])
		if err != nil {
			return err
		}
	}

	autoTLS := args.Bool["--auto-tls"]
	tlsCert, tlsKey, err := parseTLSCert(args)
	if err != nil {
		return err
	}
	if autoTLS && (tlsCert != "" || tlsKey != "") {
		return errors.New("--auto-tls cannot be used with --tls-cert or --tls-key")
	}

	tlsMode := args.String["--tls-mode"]
	route, err := resourceexpose.NewTCPRoute(service, domain, tlsMode, port, spec.Leader, autoTLS)
	if err != nil {
		return err
	}
	if tlsCert != "" || tlsKey != "" {
		route.LegacyTLSCert = tlsCert
		route.LegacyTLSKey = tlsKey
		if route.TLSMode == "" || route.TLSMode == router.TLSModePassthrough {
			route.TLSMode = router.TLSModeTerminate
		}
	}

	if autoTLS {
		acmeConfig, err := client.GetACMEConfig()
		if err != nil {
			return fmt.Errorf("error checking ACME configuration: %s", err)
		}
		if !acmeConfig.Enabled {
			return fmt.Errorf("ACME/Let's Encrypt is not enabled for this cluster.\nRun 'flynn-host acme:configure --email=<email> --agree-tos' and 'flynn-host acme:enable' first.")
		}
	}

	appName, _ := spec.ResolveAppService(res.Env)
	existing, err := client.AppRouteList(mustApp())
	if err != nil {
		return err
	}
	if prev := resourceexpose.FindTCPRoute(existing, service, domain); prev != nil {
		fmt.Println(exposedResourceURL(client, res, appName, prev))
		return nil
	}

	if err := client.CreateRoute(mustApp(), route); err != nil {
		return err
	}
	fmt.Println(exposedResourceURL(client, res, appName, route))
	return nil
}

func exposedResourceURL(client controller.Client, res *ct.Resource, appName string, route *router.Route) string {
	if route == nil {
		return ""
	}
	host := exposeNodeIP(client, appName)
	if host == "" {
		host = strings.TrimSpace(route.Domain)
	}
	env := map[string]string{}
	if res != nil && res.Env != nil {
		env = res.Env
	}
	return resourceexpose.ExposedURL(env, host, int(route.Port))
}

type hostAPI interface {
	Get(path string, out interface{}) error
}

func exposeNodeIP(client controller.Client, appName string) string {
	appName = strings.TrimSpace(appName)
	getter, ok := client.(hostAPI)
	if !ok || client == nil || appName == "" {
		return ""
	}
	jobs, err := client.JobList(appName)
	if err != nil {
		return ""
	}
	hostID := ""
	for _, job := range jobs {
		if job == nil || job.HostID == "" || job.State != ct.JobStateUp {
			continue
		}
		hostID = job.HostID
		break
	}
	if hostID == "" {
		return ""
	}
	var hosts []struct {
		ID   string `json:"id"`
		Addr string `json:"addr"`
	}
	if err := getter.Get("/hosts", &hosts); err != nil {
		return ""
	}
	for _, h := range hosts {
		if h.ID == hostID {
			return resourceexpose.HostAddrIP(h.Addr)
		}
	}
	return ""
}

func runResourceUnexpose(args *docopt.Args, client controller.Client) error {
	res, err := lookupAttachedResource(client, mustApp(), args.String["<resource>"])
	if err != nil {
		return err
	}
	_, service, err := resourceexpose.ServiceForResource(res.Env)
	if err != nil {
		return err
	}
	routes, err := client.AppRouteList(mustApp())
	if err != nil {
		return err
	}
	route := resourceexpose.FindTCPRoute(routes, service, strings.TrimSpace(args.String["--domain"]))
	if route == nil {
		return fmt.Errorf("no TCP export route for %s", resourceDisplayName(res))
	}
	if err := client.DeleteRoute(mustApp(), route.FormattedID()); err != nil {
		return err
	}
	fmt.Printf("Route %s removed.\n", route.FormattedID())
	fmt.Println(resourceexpose.FirewallFollowsRoute(int(route.Port), false))
	return nil
}

func clusterRouteDomain(client controller.Client) string {
	var explicit string
	if rel, err := client.GetAppRelease("controller"); err == nil && rel != nil {
		explicit = rel.Env["DEFAULT_ROUTE_DOMAIN"]
	}
	var controllerURL string
	if cluster, err := getCluster(); err == nil && cluster != nil {
		controllerURL = cluster.ControllerURL
	}
	return resourceexpose.ClusterDomain(controllerURL, explicit)
}

func displayTLSMode(mode string) string {
	if mode == "" {
		return "off"
	}
	return mode
}
