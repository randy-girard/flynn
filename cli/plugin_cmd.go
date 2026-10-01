package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/cheggaaa/pb"
	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cliutil"
	"github.com/randy-girard/flynn/pkg/plugin"
	"github.com/randy-girard/flynn/pkg/resname"
	"github.com/randy-girard/flynn/pkg/term"
)

// runPluginCommand handles flynn <name> when <name> is not compiled into the
// CLI. Syntax comes from the cluster catalog (plugin app meta). Cluster-job
// actions run in the plugin image. Flynn-delegated actions run a built-in
// laptop command against the plugin app (flynn -a <app> route …).
func runPluginCommand(name string, args []string) error {
	client, err := getClusterClient()
	if err != nil {
		return err
	}
	cat, err := plugin.LoadCatalog(client)
	if err != nil {
		return err
	}
	spec := cat.Lookup(name)
	if spec == nil {
		return fmt.Errorf("%s is not a flynn command. See 'flynn help'", name)
	}
	if !spec.Runnable() {
		return fmt.Errorf("%s is installed but does not define CLI actions; upgrade the plugin with flynn-host plugin:install %s", name, name)
	}

	args = pluginDefaultArgs(spec, args)
	if pluginWantsResourceList(spec, args) {
		return listPluginResources(client, spec)
	}

	if action, rest, ok := spec.MatchFlynnDelegate(args); ok {
		return runPluginFlynnCommand(client, spec, action, rest)
	}

	// pg:psql pg-harbor-kxmnpq selects that instance. The token is not part of
	// the docopt usage, so redis-cli PING stays a redis argument.
	resourceName, args := peelResourceName(name, args)

	// DocoptUsage lists colon form first (canonical) and space form as a
	// fallback. Flynn still passes argv as ["pg", "psql"] after expanding
	// pg:psql; both patterns must parse.
	argv := make([]string, 1, 1+len(args))
	argv[0] = name
	argv = append(argv, args...)
	parsed, err := docopt.Parse(spec.DocoptUsage(), argv, true, "", false)
	if err != nil {
		return err
	}
	plugin.FoldColonBools(spec, parsed.Bool)
	if resourceName != "" {
		if parsed.String == nil {
			parsed.String = map[string]string{}
		}
		parsed.String["<name>"] = resourceName
	}
	return executePluginCLI(client, spec, parsed, args)
}

// peelResourceName removes one full resource app name (prefix-word-xxxxxx)
// from the plugin argv. A token that is not that shape is left alone.
func peelResourceName(command string, args []string) (string, []string) {
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" || len(args) == 0 {
		return "", args
	}
	// Usage already has <resource>/<follower>. Peeling pg-word-xxxxxx out
	// leaves `pg wait` with no operand, so docopt prints usage and exits 1.
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "wait", "promote", "unfollow":
		return "", args
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(command) + `-[a-z]+-[a-z]{6,8}$`)
	for i, arg := range args {
		if arg == "--" {
			return "", args
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		token := strings.ToLower(arg)
		match := re.MatchString(token)
		if !match && command == "pg" && strings.HasPrefix(token, "postgresql-") && resname.IsolatedService(token) {
			match = true
		}
		if !match {
			continue
		}
		rest := make([]string, 0, len(args)-1)
		rest = append(rest, args[:i]...)
		rest = append(rest, args[i+1:]...)
		return token, rest
	}
	return "", args
}

func runPluginFlynnCommand(client controller.Client, spec *plugin.CLI, action *plugin.CLIAction, extra []string) error {
	if spec == nil || action == nil {
		return fmt.Errorf("missing plugin CLI action")
	}
	flynnCmd := strings.TrimSpace(action.Flynn)
	if flynnCmd == "" {
		return fmt.Errorf("%s: missing flynn command", spec.Command)
	}
	appName := strings.TrimSpace(spec.App)
	if appName == "" {
		return fmt.Errorf("%s plugin CLI is missing the plugin app name", spec.Command)
	}
	rest := extra
	name := strings.TrimSpace(action.Name)
	if name == "" {
		name = flynnCmd
	}
	parts := strings.Fields(name)
	if len(parts) > 0 && len(rest) >= len(parts) {
		match := true
		for i, p := range parts {
			if rest[i] != p {
				match = false
				break
			}
		}
		if match {
			rest = rest[len(parts):]
		}
	}
	fields := strings.Fields(flynnCmd)
	resolved, rest, _ := resolveCommand(fields[0], append(fields[1:], rest...))
	cmd, ok := commands[resolved]
	if !ok {
		return fmt.Errorf("%s: flynn %s is not a built-in CLI command", spec.Command, flynnCmd)
	}
	argv := make([]string, 1, 1+len(rest))
	argv[0] = resolved
	argv = append(argv, rest...)
	parsed, err := docopt.Parse(cmd.usage, argv, true, "", cmd.optsFirst)
	if err != nil {
		return err
	}
	prev := flagApp
	flagApp = appName
	defer func() { flagApp = prev }()
	switch f := cmd.f.(type) {
	case func(*docopt.Args, controller.Client) error:
		return f(parsed, client)
	case func(*docopt.Args) error:
		return f(parsed)
	case func() error:
		return f()
	case func():
		f()
		return nil
	default:
		return fmt.Errorf("unexpected command type %T", cmd.f)
	}
}

type appReleaseGetter interface {
	GetAppRelease(appID string) (*ct.Release, error)
}

type pluginJobClient interface {
	GetAppRelease(appID string) (*ct.Release, error)
	AppResourceList(appID string) ([]*ct.Resource, error)
}

func executePluginCLI(client controller.Client, spec *plugin.CLI, args *docopt.Args, extra []string) error {
	action := spec.MatchAction(args.Bool)
	if action == nil {
		return fmt.Errorf("%s: no matching plugin CLI action", spec.Command)
	}
	if strings.TrimSpace(action.Flynn) != "" {
		return runPluginFlynnCommand(client, spec, action, extra)
	}

	config, err := pluginJobConfig(client, spec, action, args)
	if err != nil {
		return err
	}
	if action.Passthrough {
		config.Args = append(config.Args, extra...)
	}
	config.ReleaseEnv = action.ReleaseEnv
	config.Data = action.Data
	cleanup, err := pluginJobIO(config, action, args)
	if err != nil {
		return err
	}
	defer cleanup()
	return runJob(client, *config)
}

func pluginDefaultArgs(spec *plugin.CLI, args []string) []string {
	if spec == nil || len(args) > 0 {
		return args
	}
	if spec.Action("list") != nil {
		return []string{"list"}
	}
	return args
}

func pluginWantsResourceList(spec *plugin.CLI, args []string) bool {
	if spec == nil || strings.TrimSpace(spec.ResourceEnv) == "" {
		return false
	}
	if spec.Action("list") != nil {
		return false
	}
	if len(args) == 0 {
		return true
	}
	return len(args) == 1 && args[0] == "list"
}

func listPluginResources(client controller.Client, spec *plugin.CLI) error {
	if spec == nil {
		return fmt.Errorf("missing plugin CLI")
	}
	resources, err := client.AppResourceList(mustApp())
	if err != nil {
		return err
	}
	w := tabWriter()
	defer w.Flush()
	listRec(w, "NAME", "PROVIDER", "ID", "ROLE")
	for _, r := range resources {
		if r == nil || !pluginResourceMatches(spec, r) {
			continue
		}
		providerName := r.ProviderID
		if p, err := client.GetProvider(r.ProviderID); err == nil && p != nil && p.Name != "" {
			providerName = p.Name
		}
		role := strings.TrimSpace(r.Env["POSTGRES_ROLE"])
		if role == "" {
			role = strings.TrimSpace(r.Env["MYSQL_ROLE"])
		}
		if role == "" {
			role = strings.TrimSpace(r.Env["REDIS_ROLE"])
		}
		if role == "" {
			role = strings.TrimSpace(r.Env["CLICKHOUSE_ROLE"])
		}
		if role == "" {
			role = "-"
		}
		listRec(w, resourceDisplayName(r), providerName, r.ID, role)
	}
	return nil
}

func pluginResourceMatches(spec *plugin.CLI, res *ct.Resource) bool {
	if spec == nil || res == nil {
		return false
	}
	key := strings.TrimSpace(spec.ResourceEnv)
	if key != "" && res.Env != nil && strings.TrimSpace(res.Env[key]) != "" {
		return true
	}
	cmd := strings.ToLower(strings.TrimSpace(spec.Command))
	switch cmd {
	case "pg":
		cmd = "postgres"
	}
	if p, ok := res.Env["FLYNN_POSTGRES"]; ok && cmd == "postgres" && strings.TrimSpace(p) != "" {
		return true
	}
	return false
}

func pluginJobConfig(client pluginJobClient, spec *plugin.CLI, action *plugin.CLIAction, args *docopt.Args) (*runConfig, error) {
	appName := strings.TrimSpace(spec.App)
	if action == nil || !action.Cluster || appName == "" {
		appName = mustApp()
	}
	appRelease, err := client.GetAppRelease(appName)
	if err != nil {
		if action != nil && action.Cluster {
			return nil, fmt.Errorf("error getting app release: %s", err)
		}
		if !isReleaseMissing(err) {
			return nil, fmt.Errorf("error getting app release: %s", err)
		}
		appRelease = &ct.Release{}
	}

	var resources []*ct.Resource
	if action == nil || !action.Cluster {
		if list, lerr := client.AppResourceList(mustApp()); lerr == nil {
			resources = list
		}
	}

	in, resourceRelease, err := pluginInterp(client, spec, appRelease, resources, resourceNameArg(args))
	if err != nil {
		return nil, err
	}
	in.AppName = appName
	if resourceRelease == nil || resourceRelease.ID == "" {
		return nil, fmt.Errorf("error getting %s release", spec.Command)
	}

	jobArgs, err := plugin.InterpolateAll(action.Args, in)
	if err != nil {
		return nil, err
	}
	jobArgs = compactPluginArgs(jobArgs)
	if action.Append != "" {
		jobArgs = append(jobArgs, cliutil.List(args, action.Append)...)
	}

	env := make(map[string]string, len(action.Env)+6)
	for k, v := range action.Env {
		s, err := plugin.Interpolate(v, in)
		if err != nil {
			return nil, err
		}
		env[k] = s
	}
	copyAppPGEnv(env, in.App)

	return &runConfig{
		App:        appName,
		Release:    resourceRelease.ID,
		Env:        env,
		Args:       jobArgs,
		DisableLog: true,
		Exit:       true,
		// Plugin CLIs talk to plugin APIs (scheduler.discoverd, etc.).
		// User-partition jobs only resolve leader.<datastore>.discoverd.
		Partition: ct.PartitionTypeSystem,
	}, nil
}

func compactPluginArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != "" {
			out = append(out, a)
		}
	}
	return out
}

func copyAppPGEnv(env, app map[string]string) {
	if env == nil || app == nil {
		return
	}
	for _, k := range []string{"PGHOST", "PGUSER", "PGPASSWORD", "PGDATABASE", "PGSSLMODE", "PGPORT"} {
		if env[k] == "" && app[k] != "" {
			env[k] = app[k]
		}
	}
}

func resourceNameArg(args *docopt.Args) string {
	if args == nil || args.String == nil {
		return ""
	}
	for _, k := range []string{"<name>", "<resource>", "<follower>"} {
		if v := strings.TrimSpace(args.String[k]); v != "" {
			return v
		}
	}
	return ""
}

func pluginInterp(client appReleaseGetter, spec *plugin.CLI, appRelease *ct.Release, resources []*ct.Resource, resourceName string) (plugin.Interp, *ct.Release, error) {
	in := plugin.Interp{App: map[string]string{}}
	if appRelease != nil && appRelease.Env != nil {
		in.App = cloneEnv(appRelease.Env)
	}

	var selected *ct.Resource
	switch {
	case spec.ResourceEnv != "":
		name, res, err := resolvePluginInstance(spec, in.App, resources, resourceName)
		if err != nil {
			return in, nil, err
		}
		in.Resource = name
		selected = res
	case spec.App != "":
		in.Resource = spec.App
	default:
		return in, nil, fmt.Errorf("%s plugin CLI does not declare a resource", spec.Command)
	}

	instanceRelease, err := client.GetAppRelease(in.Resource)
	if err != nil {
		if spec.ResourceEnv == "" || !isReleaseMissing(err) {
			return in, nil, fmt.Errorf("error getting %s release: %s", spec.Command, err)
		}
		instanceRelease = nil
	}
	resRelease := instanceRelease
	if resRelease != nil {
		in.ResourceEnv = resRelease.Env
	}

	src := connectionEnvForInstance(in.App, in.Resource, selected, instanceRelease)
	if selected == nil && strings.TrimSpace(resourceName) != "" && instanceRelease == nil && len(src) == 0 {
		return in, nil, fmt.Errorf("no %s resource named %s", spec.Command, in.Resource)
	}
	if selected != nil && selected.Env != nil {
		applyNamedResource(in.App, in.Resource, selected.Env)
	} else if strings.TrimSpace(resourceName) != "" && len(src) > 0 {
		applyNamedResource(in.App, in.Resource, src)
	}

	if (resRelease == nil || resRelease.ID == "") && spec.App != "" && spec.App != in.Resource {
		pluginRel, perr := client.GetAppRelease(spec.App)
		if perr == nil && pluginRel != nil && pluginRel.ID != "" {
			resRelease = pluginRel
		}
	}
	if resRelease == nil || resRelease.ID == "" {
		if err != nil {
			return in, nil, fmt.Errorf("error getting %s release: %s", spec.Command, err)
		}
		return in, nil, fmt.Errorf("error getting %s release", spec.Command)
	}
	return in, resRelease, nil
}

func resolvePluginInstance(spec *plugin.CLI, env map[string]string, resources []*ct.Resource, resourceName string) (string, *ct.Resource, error) {
	matched := pluginMatchingResources(spec, resources)
	names := pluginInstanceNames(spec, env, matched)
	want := strings.TrimSpace(resourceName)
	if want != "" {
		want = resname.Canonical(spec.Command, want)
		if res := lookupPluginResource(matched, want, resourceName); res != nil {
			if name := resourceDisplayName(res); name != "" {
				return name, res, nil
			}
			return want, res, nil
		}
		for _, n := range names {
			if strings.EqualFold(n, want) || strings.EqualFold(n, strings.TrimSpace(resourceName)) {
				return n, lookupPluginResource(matched, n, ""), nil
			}
		}
		return want, nil, nil
	}
	if len(matched) == 1 {
		if name := resourceDisplayName(matched[0]); name != "" {
			return name, matched[0], nil
		}
	}
	if len(matched) > 1 {
		return "", nil, pluginMultipleInstances(spec, pluginInstanceNames(spec, nil, matched))
	}
	if len(names) == 1 {
		return names[0], lookupPluginResource(matched, names[0], ""), nil
	}
	if len(names) > 1 {
		return "", nil, pluginMultipleInstances(spec, names)
	}
	return "", nil, pluginResourceMissingErr(spec)
}

func pluginMatchingResources(spec *plugin.CLI, list []*ct.Resource) []*ct.Resource {
	var out []*ct.Resource
	for _, r := range list {
		if pluginResourceMatches(spec, r) {
			out = append(out, r)
		}
	}
	return out
}

func lookupPluginResource(list []*ct.Resource, names ...string) *ct.Resource {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		for _, r := range list {
			if resourceMatchesRef(r, name) {
				return r
			}
		}
	}
	return nil
}

func pluginInstanceNames(spec *plugin.CLI, env map[string]string, resources []*ct.Resource) []string {
	seen := map[string]struct{}{}
	var names []string
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n == "" {
			return
		}
		if spec != nil && spec.ResourceEnv != "" {
			n = resname.Canonical(spec.Command, n)
		}
		if _, ok := seen[n]; ok {
			return
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}
	for _, r := range resources {
		add(resourceDisplayName(r))
	}
	if spec != nil && spec.ResourceEnv != "" && env != nil {
		add(env[spec.ResourceEnv])
	}
	for _, n := range instanceNamesFromEnv(spec, env) {
		add(n)
	}
	sort.Strings(names)
	return names
}

func instanceNamesFromEnv(spec *plugin.CLI, env map[string]string) []string {
	if env == nil {
		return nil
	}
	cmd := ""
	if spec != nil {
		cmd = strings.ToLower(strings.TrimSpace(spec.Command))
	}
	re := instanceDiscoverdRe(cmd)
	var names []string
	for _, v := range env {
		for _, m := range re.FindAllStringSubmatch(v, -1) {
			if len(m) > 1 {
				names = append(names, m[1])
			}
		}
	}
	return names
}

func instanceDiscoverdRe(command string) *regexp.Regexp {
	if command == "pg" {
		return regexp.MustCompile(`(?:leader\.)?(postgresql-[a-z0-9]+(?:-[a-z0-9]+)*-[0-9]{5,8}|pg-[a-z]+-[a-z]{6,8})\.discoverd`)
	}
	if command == "" {
		return regexp.MustCompile(`(?:leader\.)?([a-z]+-[a-z]+-[a-z]{6,8}|postgresql-[a-z0-9]+(?:-[a-z0-9]+)*-[0-9]{5,8})\.discoverd`)
	}
	return regexp.MustCompile(`(?:leader\.)?(` + regexp.QuoteMeta(command) + `-[a-z]+-[a-z]{6,8})\.discoverd`)
}

func pluginResourceMissingErr(spec *plugin.CLI) error {
	msg := ""
	if spec != nil {
		msg = spec.ResourceMissing
		if msg == "" {
			msg = fmt.Sprintf("No %s resource found. Provision one with `flynn resource:add %s`", spec.Command, spec.Command)
		}
	}
	return fmt.Errorf("%s", msg)
}

func pluginMultipleInstances(spec *plugin.CLI, names []string) error {
	cmd := "resource"
	if spec != nil && spec.Command != "" {
		cmd = spec.Command
	}
	var b strings.Builder
	fmt.Fprintf(&b, "multiple %s resources; specify the name:\n", cmd)
	for _, n := range names {
		fmt.Fprintf(&b, "  %s\n", n)
	}
	return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}

func connectionEnvForInstance(app map[string]string, name string, res *ct.Resource, release *ct.Release) map[string]string {
	if res != nil && res.Env != nil {
		return res.Env
	}
	if release != nil && release.Env != nil {
		return release.Env
	}
	return envForNamedInstance(app, name)
}

func envForNamedInstance(env map[string]string, name string) map[string]string {
	out := map[string]string{}
	if name == "" || env == nil {
		return out
	}
	prefix := resname.EnvPrefix(name)
	pick := func(v string) bool {
		return v != "" && strings.Contains(v, name)
	}
	keys := []string{}
	for k := range env {
		if strings.HasPrefix(k, "FLYNN_POSTGRESQL_") && strings.HasSuffix(k, "_URL") {
			keys = append(keys, k)
		}
	}
	keys = append(keys, prefix+"_DATABASE_URL", prefix+"_POSTGRES_URL", "POSTGRES_URL", "DATABASE_URL")
	for _, k := range keys {
		if pick(env[k]) {
			out["POSTGRES_URL"] = env[k]
			out["DATABASE_URL"] = env[k]
			break
		}
	}
	if out["POSTGRES_URL"] == "" {
		for _, v := range env {
			if strings.Contains(v, "://") && strings.Contains(v, name) {
				out["POSTGRES_URL"] = v
				out["DATABASE_URL"] = v
				break
			}
		}
	}
	if prefix != "" {
		for _, k := range []string{"PGHOST", "PGUSER", "PGPASSWORD", "PGDATABASE", "PGPORT", "PGSSLMODE", "POSTGRES_PASSWORD"} {
			if v := env[prefix+"_"+k]; v != "" {
				out[k] = v
			}
		}
	}
	return out
}

func cloneEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

func isReleaseMissing(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, controller.ErrNotFound) || errors.Is(err, ct.ErrNotFound) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "resource not found") || strings.Contains(s, "no release for")
}

// applyNamedResource points console placeholders at the instance named on the
// command line. Host keys from the app's default attachment are dropped so
// manifests fall back to leader.<name>.discoverd, then the instance release
// env (password, URL) is copied in. MYSQL_PWD on a live mysql job is the
// superuser; MYSQL_APP_PASSWORD is the resource:add user console needs.
func applyNamedResource(app map[string]string, resource string, env map[string]string) {
	if app == nil {
		return
	}
	for _, key := range []string{
		"REDIS_HOST", "POSTGRES_URL", "DATABASE_URL", "MYSQL_HOST", "MONGO_HOST",
		"CLICKHOUSE_HOST", "CLICKHOUSE_URL", "CLICKHOUSE_HTTP_URL",
		"CLICKHOUSE_USER", "CLICKHOUSE_PASSWORD", "CLICKHOUSE_TRUSTED_CERT",
		"KAFKA_HOST", "KAFKA_URL", "KAFKA_BOOTSTRAP_SERVERS",
		"KAFKA_BROKER_URLS", "KAFKA_TRUSTED_CERT", "KAFKA_CLIENT_CERT",
		"KAFKA_CLIENT_CERT_KEY", "KAFKA_SASL_USERNAME", "KAFKA_SASL_PASSWORD",
		"KAFKA_SASL_MECHANISM", "KAFKA_TOPIC_PREFIX",
	} {
		delete(app, key)
	}
	for key, val := range env {
		if val != "" {
			app[key] = val
		}
	}
	if resource != "" {
		named := envForNamedInstance(app, resource)
		if app["DATABASE_URL"] == "" && named["DATABASE_URL"] != "" {
			app["DATABASE_URL"] = named["DATABASE_URL"]
		}
		if app["POSTGRES_URL"] == "" && named["POSTGRES_URL"] != "" {
			app["POSTGRES_URL"] = named["POSTGRES_URL"]
		}
	}
	if app["POSTGRES_URL"] == "" && strings.Contains(strings.ToLower(app["DATABASE_URL"]), "postgres") {
		app["POSTGRES_URL"] = app["DATABASE_URL"]
	}
	if app["DATABASE_URL"] == "" && app["POSTGRES_URL"] != "" {
		app["DATABASE_URL"] = app["POSTGRES_URL"]
	}
	// Isolated mysql/mongodb jobs keep *_PWD as the superuser for replication.
	// Console interpolates the app user + *_PWD, so prefer the app password.
	if pwd := app["MYSQL_APP_PASSWORD"]; pwd != "" {
		app["MYSQL_PWD"] = pwd
	}
	if pwd := app["MONGO_APP_PASSWORD"]; pwd != "" {
		app["MONGO_PWD"] = pwd
	}
	if su := app["KAFKA_SUPERUSER_PASSWORD"]; su != "" {
		app["KAFKA_SASL_PASSWORD"] = su
		app["KAFKA_SASL_USERNAME"] = "flynn-admin"
		app["KAFKA_SASL_MECHANISM"] = "SCRAM-SHA-256"
	}
	if resource != "" {
		host := "leader." + resource + ".discoverd"
		for _, key := range []string{"REDIS_HOST", "MYSQL_HOST", "MONGO_HOST", "CLICKHOUSE_HOST", "KAFKA_HOST"} {
			if app[key] == "" {
				app[key] = host
			}
		}
		if app["KAFKA_BOOTSTRAP_SERVERS"] == "" {
			app["KAFKA_BOOTSTRAP_SERVERS"] = host + ":9092"
		}
	}
}

func pluginJobIO(config *runConfig, action *plugin.CLIAction, args *docopt.Args) (func(), error) {
	var closers []io.Closer
	var bars []*pb.ProgressBar
	cleanup := func() {
		for _, b := range bars {
			b.Finish()
		}
		for _, c := range closers {
			c.Close()
		}
	}

	quiet := false
	if action.Quiet != "" {
		quiet = args.Bool[action.Quiet]
	}
	showProgress := action.Progress && !quiet && term.IsTerminal(os.Stderr.Fd())

	if action.StdoutFile != "" {
		config.Stdout = os.Stdout
		if filename := args.String[action.StdoutFile]; filename != "" {
			f, err := os.Create(filename)
			if err != nil {
				cleanup()
				return nil, err
			}
			closers = append(closers, f)
			config.Stdout = f
		}
		if showProgress {
			bar := pb.New(0)
			bar.SetUnits(pb.U_BYTES)
			bar.ShowBar = false
			bar.ShowSpeed = true
			bar.Output = os.Stderr
			bar.Start()
			bars = append(bars, bar)
			config.Stdout = io.MultiWriter(config.Stdout, bar)
		}
	}

	if action.StdinFile != "" {
		config.Stdin = os.Stdin
		var size int64
		if filename := args.String[action.StdinFile]; filename != "" {
			f, err := os.Open(filename)
			if err != nil {
				cleanup()
				return nil, err
			}
			closers = append(closers, f)
			stat, err := f.Stat()
			if err != nil {
				cleanup()
				return nil, err
			}
			size = stat.Size()
			config.Stdin = f
		}
		if showProgress {
			bar := pb.New(0)
			bar.SetUnits(pb.U_BYTES)
			if size > 0 {
				bar.Total = size
			} else {
				bar.ShowBar = false
			}
			bar.ShowSpeed = true
			bar.Output = os.Stderr
			bar.Start()
			bars = append(bars, bar)
			config.Stdin = bar.NewProxyReader(config.Stdin)
		}
	}
	return cleanup, nil
}
