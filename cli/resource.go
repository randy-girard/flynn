package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/dbruntime"
	"github.com/randy-girard/flynn/pkg/pgappliance"
	"github.com/randy-girard/flynn/pkg/resname"
	"github.com/randy-girard/flynn/pkg/resourceexpose"
	router "github.com/randy-girard/flynn/router/types"
)

func init() {
	register("resource", runResourceList, `
usage: flynn resource

List resources for the app.

NAME is the isolated instance (pg-harbor-kxmnpq). Use it with pg:psql,
redis-cli, --follow, --join, resource:attach, and resource:remove.
`)
	register("resource:add", runResourceAdd, `
usage: flynn resource:add <provider> [--as <name>] [--follow <resource>] [--join <resource>] [--runtime <name>] [--replication <mode>] [--cpu <milli>] [--memory <bytes>] [--disk <bytes>]

Provision a new resource for the app using <provider>.

For postgres, mysql, redis, mongodb, kafka, and clickhouse, the new instance
is sized from a database runtime (flynn-host db-runtime). Those are not app
process runtimes. Omitting --runtime uses small. --cpu, --memory, and --disk
are rejected unless a cluster admin has allowed custom sizes.

For postgres, mysql, redis, kafka, mongodb, and clickhouse, the installed plugin
receives --as, --follow, --join, --runtime, and --replication. Postgres, mysql,
and redis --follow creates a replica resource of that instance. Kafka and
mongodb --join starts another Flynn job on that existing cluster (any member
name works; --follow is accepted as an alias). ClickHouse --follow still copies
onto a separate resource. The platform postgres appliance at
postgres-api.discoverd is not used. --as ANALYTICS sets only ANALYTICS_URL.
The default name DATABASE sets only DATABASE_URL.

Options:
	--as=<name>              attachment env name (default DATABASE)
	--follow=<resource>      replica NAME or ID from flynn resource (postgres/mysql/redis/clickhouse); alias of --join on kafka/mongodb
	--join=<resource>        extra kafka or mongodb cluster node (NAME or ID from flynn resource)
	--runtime=<name>         database runtime name (default small)
	--replication=<mode>     postgres followers ignore this and always stream; pg:upgrade uses logical
	--cpu=<milli>            raw milliCPU (only when custom sizes are allowed)
	--memory=<bytes>         raw memory (only when custom sizes are allowed)
	--disk=<bytes>           raw disk (only when custom sizes are allowed)
`)
	register("resource:attach", runResourceAttach, `
usage: flynn resource:attach <provider> <resource> [--as <name>]

Attach an existing resource to this app.

<resource> is the NAME or ID from flynn resource. For postgres, --as sets one
env var (<NAME>_URL). The default name is the resource's existing *_URL key.
The same resource can attach to several apps with different names.

Options:
	--as=<name>  attachment env name
`)
	register("resource:detach", runResourceDetach, `
usage: flynn resource:detach <provider> <resource>

Detach a resource from this app and remove the attachment env var.

<resource> is the NAME or ID from flynn resource.
`)
	register("resource:remove", runResourceRemove, `
usage: flynn resource:remove [<provider>] [<resource>]

Remove a resource. <resource> is the NAME or ID from flynn resource
(pg-orchid-xkhthp). flynn resource:remove pg-orchid-xkhthp is enough when
that name is unique on the app. With only <provider>, removes the unique
resource for that provider. A leader cannot be removed while followers are
still linked; unfollow or remove those replicas first.
`)
	register("resource:expose", runResourceExpose, `
usage: flynn resource:expose <provider> [--domain <host>] [-p <port>] [--tls-mode <mode>] [--auto-tls] [-c <tls-cert> -k <tls-key>]

Export a datastore (postgres, mysql, mongodb, redis, kafka, clickhouse)
on a TCP route with a stable hostname, then open the host port with
flynn-host firewall:expose.

Options:
	--domain=<host>        hostname (default: SERVICE.CLUSTERDOMAIN)
	-p, --port=<port>      TCP port (assigned from 3000-3500 if omitted)
	--tls-mode=<mode>      off, passthrough (default), or terminate
	--auto-tls             Let's Encrypt on a terminate route (requires ACME)
	-c, --tls-cert=<tls-cert>  PEM cert for tls_mode=terminate, - for stdin
	-k, --tls-key=<tls-key>    PEM key for tls_mode=terminate, - for stdin

passthrough is the default so postgres/mysql native SSL still works. Use
terminate for TLS-first protocols (Redis TLS, Kafka, MongoDB, ClickHouse)
when the backend is plaintext. flynn-host also opens TCP route ports on
its 15s firewall sync; pin the port on every node:

    sudo flynn-host firewall:expose PORT
`)
	register("resource:unexpose", runResourceUnexpose, `
usage: flynn resource:unexpose <provider> [--domain <host>]

Remove the TCP export route for <provider> and print the matching
flynn-host firewall:unexpose command.
`)
}

func runResourceList(args *docopt.Args, client controller.Client) error {

	resources, err := client.AppResourceList(mustApp())
	if err != nil {
		return err
	}

	w := tabWriter()
	defer w.Flush()

	var provider *ct.Provider

	listRec(w, "NAME", "PROVIDER", "ID")
	for _, j := range resources {
		provider, err = client.GetProvider(j.ProviderID)
		if err != nil {
			return err
		}
		listRec(w, resourceDisplayName(j), provider.Name, j.ID)
	}

	return err
}

func runResourceAdd(args *docopt.Args, client controller.Client) error {
	provider := args.String["<provider>"]
	if err := rejectPlatformPostgresAdd(provider, client); err != nil {
		return err
	}

	req := &ct.ResourceReq{ProviderID: provider, Apps: []string{mustApp()}}
	cat, err := databaseRuntimeCatalog(client)
	if err != nil {
		return err
	}
	follow, err := resolvePeerRef(client, mustApp(), provider, args.String["--follow"])
	if err != nil {
		return err
	}
	join, err := resolvePeerRef(client, mustApp(), provider, args.String["--join"])
	if err != nil {
		return err
	}
	cfg, err := databaseProvisionConfig(provider, args.String["--as"], follow, join, args.String["--runtime"], args.String["--replication"], args.String["--cpu"], args.String["--memory"], args.String["--disk"], cat)
	if err != nil {
		return err
	}
	req.Config = cfg
	res, err := client.ProvisionResource(req)
	if err != nil {
		return err
	}

	env := appliedAttachmentEnv(client, res.Env, args.String["--as"])

	if _, err := setEnv(client, "", env); err != nil {
		return err
	}

	log.Println(createdResourceMessage(res, args.String["--as"]))

	return nil
}

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
// resource app name (pg-harbor-kxmnpq), not controller UUIDs. --as is shown
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
		return fmt.Sprintf("Created resource %s (as %s) and a new release.", name, as)
	}
	return fmt.Sprintf("Created resource %s and a new release.", name)
}

// resourceDisplayName is the isolated instance operators copy from flynn
// resource (pg-harbor-kxmnpq). It is what pg:psql and --follow/--join take.
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
// plugins expect for --follow/--join. Empty ref is unchanged.
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

// rejectPlatformPostgresAdd stops `flynn resource:add postgres` from creating
// a role on the built-in appliance. When the postgres plugin is installed it
// registers provider postgres at postgres-plugin.discoverd, and that URL is allowed.
func rejectPlatformPostgresAdd(provider string, client controller.Client) error {
	if provider != "postgres" {
		return nil
	}
	p, err := client.GetProvider(provider)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return pgappliance.ErrTenantProvision
		}
		return err
	}
	if p == nil || pgappliance.IsPlatformApplianceURL(p.URL) {
		return pgappliance.ErrTenantProvision
	}
	return nil
}

type databaseProvisionBody struct {
	As          string `json:"as,omitempty"`
	Follow      string `json:"follow,omitempty"`
	Join        string `json:"join,omitempty"`
	Runtime     string `json:"runtime,omitempty"`
	Replication string `json:"replication,omitempty"`
	CPU         int64  `json:"cpu,omitempty"`
	Memory      int64  `json:"memory,omitempty"`
	Disk        int64  `json:"disk,omitempty"`
}

func isClusterJoinProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "kafka", "mongodb":
		return true
	default:
		return false
	}
}

// provisionJoinFollow maps CLI --follow/--join onto the provider JSON.
// Kafka and mongodb send join (and follow as a compatibility alias). Replica
// engines send follow only; --join is rejected there.
func provisionJoinFollow(provider, follow, join string) (followJSON, joinJSON string, err error) {
	follow = strings.TrimSpace(follow)
	join = strings.TrimSpace(join)
	if follow != "" && join != "" && follow != join {
		return "", "", fmt.Errorf("--follow and --join cannot both be set")
	}
	target := join
	if target == "" {
		target = follow
	}
	if target == "" {
		return "", "", nil
	}
	if isClusterJoinProvider(provider) {
		return target, target, nil
	}
	if join != "" && follow == "" {
		return "", "", fmt.Errorf("%s uses --follow for a replica resource; --join adds a kafka or mongodb cluster node", provider)
	}
	return target, "", nil
}

// databaseProvisionConfig sizes postgres, mysql, redis, mongodb, kafka, and
// clickhouse from the database-runtime catalog. Other providers are unchanged.
// Omitting runtime uses small. Raw cpu, memory, or disk is rejected unless
// the catalog allows custom sizes. The size is fixed on this request;
// later edits to the runtime definition do not resize this instance.
func databaseProvisionConfig(provider, as, follow, join, runtime, replication, cpuRaw, memRaw, diskRaw string, cat dbruntime.Catalog) (*json.RawMessage, error) {
	if _, ok := dbruntime.ProviderEngine(provider); !ok {
		if cpuRaw != "" || memRaw != "" || diskRaw != "" {
			return nil, fmt.Errorf("cpu, memory, and disk apply to database providers (postgres, mysql, redis, mongodb, kafka, clickhouse)")
		}
		return nil, nil
	}
	var custom dbruntime.Size
	if cpuRaw != "" {
		n, err := dbruntime.ParseCPU(cpuRaw)
		if err != nil {
			return nil, err
		}
		custom.CPU = n
	}
	if memRaw != "" {
		n, err := dbruntime.ParseBytes(memRaw)
		if err != nil {
			return nil, err
		}
		custom.Memory = n
	}
	if diskRaw != "" {
		n, err := dbruntime.ParseBytes(diskRaw)
		if err != nil {
			return nil, err
		}
		custom.Disk = n
	}
	sz, name, err := dbruntime.ResolveProvision(provider, runtime, custom, cat, cat.AllowCustomSizes)
	if err != nil {
		return nil, err
	}
	followJSON, joinJSON, err := provisionJoinFollow(provider, follow, join)
	if err != nil {
		return nil, err
	}
	if followJSON != "" && strings.EqualFold(strings.TrimSpace(provider), "postgres") {
		mode := strings.ToLower(strings.TrimSpace(replication))
		if mode != "" && mode != "streaming" {
			return nil, fmt.Errorf("postgres followers use streaming replication; run flynn pg:upgrade for a major-version swap")
		}
		replication = "streaming"
	}
	body := databaseProvisionBody{
		As:          as,
		Follow:      followJSON,
		Join:        joinJSON,
		Runtime:     name,
		Replication: replication,
		CPU:         sz.CPU,
		Memory:      sz.Memory,
		Disk:        sz.Disk,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	msg := json.RawMessage(raw)
	return &msg, nil
}

func appliedAttachmentEnv(client controller.Client, incoming map[string]string, as string) map[string]*string {
	existing := map[string]string{}
	if release, err := client.GetAppRelease(mustApp()); err == nil && release != nil && release.Env != nil {
		existing = release.Env
	}
	merged := resname.MergeAttachment(existing, incoming, as)
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
	name := key[:len(key)-len("_URL")]
	if strings.TrimSpace(as) != "" {
		name = strings.ToUpper(strings.TrimSpace(as))
	}
	return map[string]string{name + "_URL": val}, nil
}

func runResourceAttach(args *docopt.Args, client controller.Client) error {
	provider := args.String["<provider>"]
	resRef, err := lookupProviderResource(client, mustApp(), provider, args.String["<resource>"])
	if err != nil {
		return err
	}
	res, err := client.AddResourceApp(provider, resRef.ID, mustApp())
	if err != nil {
		return err
	}
	env := appliedAttachmentEnv(client, res.Env, args.String["--as"])
	releaseID, err := setEnv(client, "", env)
	if err != nil {
		return err
	}
	log.Printf("Attached resource %s and release %s.", resourceDisplayName(res), releaseID)
	return nil
}

func runResourceDetach(args *docopt.Args, client controller.Client) error {
	provider := args.String["<provider>"]
	resRef, err := lookupProviderResource(client, mustApp(), provider, args.String["<resource>"])
	if err != nil {
		return err
	}
	res, err := client.DeleteResourceApp(provider, resRef.ID, mustApp())
	if err != nil {
		return err
	}
	release, err := client.GetAppRelease(mustApp())
	if err != nil {
		return err
	}
	env := resname.UnsetAttachment(release.Env, res.Env)
	releaseID, err := setEnv(client, "", env)
	if err != nil {
		return err
	}
	log.Printf("Detached resource %s and release %s.", resourceDisplayName(res), releaseID)
	return nil
}

func runResourceRemove(args *docopt.Args, client controller.Client) error {
	providerArg := strings.TrimSpace(args.String["<provider>"])
	resourceArg := strings.TrimSpace(args.String["<resource>"])
	resRef, providerName, err := resolveRemoveTarget(client, mustApp(), providerArg, resourceArg)
	if err != nil {
		return err
	}
	if names := resourceFollowerNames(resRef, appResourcesOrNil(client, mustApp())); len(names) > 0 {
		return fmt.Errorf("cannot remove %s while followers are still linked (%s); unfollow or remove those resources first", resourceDisplayName(resRef), strings.Join(names, ", "))
	}

	res, err := client.DeleteResource(providerName, resRef.ID)
	if err != nil {
		return err
	}

	release, err := client.GetAppRelease(mustApp())
	if err != nil {
		return err
	}
	env := resname.UnsetAttachment(release.Env, res.Env)
	releaseID, err := setEnv(client, "", env)
	if err != nil {
		return err
	}

	log.Printf("Deleted resource %s, created release %s.", resourceDisplayName(res), releaseID)

	return nil
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
// resource NAME/ID with no provider (flynn resource:remove pg-orchid-xkhthp).
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

func resourceFollowerNames(leader *ct.Resource, list []*ct.Resource) []string {
	if leader == nil {
		return nil
	}
	name := resourceDisplayName(leader)
	var out []string
	seen := map[string]bool{}
	for _, r := range list {
		if r == nil || r.ID == leader.ID || !resourceFollowsLeader(r, leader, name) {
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

func resourceFollowsLeader(res, leader *ct.Resource, leaderName string) bool {
	if res == nil || res.Env == nil || leaderName == "" {
		return false
	}
	env := res.Env
	for _, k := range []string{"POSTGRES_LEADER", "MYSQL_LEADER", "REDIS_LEADER", "CLICKHOUSE_LEADER", "KAFKA_LEADER"} {
		if strings.EqualFold(strings.TrimSpace(env[k]), leaderName) {
			return true
		}
	}
	if strings.EqualFold(strings.TrimSpace(env["POSTGRES_ROLE"]), "follower") && strings.EqualFold(strings.TrimSpace(env["POSTGRES_LEADER"]), leaderName) {
		return true
	}
	for k, v := range env {
		if !strings.Contains(k, "PRIMARY") {
			continue
		}
		if strings.Contains(v, leaderName) {
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

func runResourceExpose(args *docopt.Args, client controller.Client) error {
	spec, err := resourceexpose.Lookup(args.String["<provider>"])
	if err != nil {
		return err
	}
	appRelease, err := client.GetAppRelease(mustApp())
	if err != nil {
		return err
	}
	appliance, service := spec.ResolveAppService(appRelease.Env)
	if appliance == "" {
		return fmt.Errorf("no %s resource on this app; provision with flynn resource:add %s", spec.Provider, spec.Provider)
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

	existing, err := client.AppRouteList(mustApp())
	if err != nil {
		return err
	}
	if prev := resourceexpose.FindTCPRoute(existing, service, domain); prev != nil {
		fmt.Printf("%s already exposed as %s on port %d (%s)\n", spec.Provider, prev.Domain, prev.Port, prev.FormattedID())
		fmt.Printf("On each host: %s\n", resourceexpose.FirewallExposeCommand(int(prev.Port)))
		return nil
	}

	if err := client.CreateRoute(mustApp(), route); err != nil {
		return err
	}
	fmt.Printf("%s listening on port %d\n", route.FormattedID(), route.Port)
	if route.Domain != "" {
		fmt.Printf("hostname %s tls_mode=%s\n", route.Domain, displayTLSMode(route.TLSMode))
	}
	fmt.Printf("On each host run: %s\n", resourceexpose.FirewallExposeCommand(int(route.Port)))
	fmt.Printf("flynn-host also opens TCP route ports on its firewall sync.\n")
	return nil
}

func runResourceUnexpose(args *docopt.Args, client controller.Client) error {
	spec, err := resourceexpose.Lookup(args.String["<provider>"])
	if err != nil {
		return err
	}
	appRelease, err := client.GetAppRelease(mustApp())
	if err != nil {
		return err
	}
	_, service := spec.ResolveAppService(appRelease.Env)
	if service == "" {
		return fmt.Errorf("no %s resource on this app", spec.Provider)
	}
	routes, err := client.AppRouteList(mustApp())
	if err != nil {
		return err
	}
	route := resourceexpose.FindTCPRoute(routes, service, strings.TrimSpace(args.String["--domain"]))
	if route == nil {
		return fmt.Errorf("no TCP export route for %s", spec.Provider)
	}
	if err := client.DeleteRoute(mustApp(), route.FormattedID()); err != nil {
		return err
	}
	fmt.Printf("Route %s removed.\n", route.FormattedID())
	fmt.Printf("On each host run: %s\n", resourceexpose.FirewallUnexposeCommand(int(route.Port)))
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
