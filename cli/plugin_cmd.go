package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
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

	if action, rest, ok := spec.MatchFlynnDelegate(args); ok {
		return runPluginFlynnCommand(client, spec, action, rest)
	}

	// pg:psql pg-harbor-kxmnpq selects that instance. The token is not part of
	// the docopt usage, so redis-cli PING stays a redis argument.
	resourceName, args := peelResourceName(name, args)

	argv := make([]string, 1, 1+len(args))
	argv[0] = name
	argv = append(argv, args...)
	parsed, err := docopt.Parse(spec.Doc, argv, true, "", false)
	if err != nil {
		return err
	}
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
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(command) + `-[a-z]+-[a-z]{6,8}$`)
	for i, arg := range args {
		if arg == "--" {
			return "", args
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		token := strings.ToLower(arg)
		if !re.MatchString(token) {
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

func pluginJobConfig(client appReleaseGetter, spec *plugin.CLI, action *plugin.CLIAction, args *docopt.Args) (*runConfig, error) {
	appName := strings.TrimSpace(spec.App)
	if action == nil || !action.Cluster || appName == "" {
		appName = mustApp()
	}
	appRelease, err := client.GetAppRelease(appName)
	if err != nil {
		return nil, fmt.Errorf("error getting app release: %s", err)
	}

	in, resourceRelease, err := pluginInterp(client, spec, appRelease, resourceNameArg(args))
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
	return strings.TrimSpace(args.String["<name>"])
}

func pluginInterp(client appReleaseGetter, spec *plugin.CLI, appRelease *ct.Release, resourceName string) (plugin.Interp, *ct.Release, error) {
	in := plugin.Interp{App: map[string]string{}}
	if appRelease != nil && appRelease.Env != nil {
		in.App = appRelease.Env
	}

	switch {
	case spec.ResourceEnv != "" && strings.TrimSpace(resourceName) != "":
		// pg:psql pg-harbor-kxmnpq. pipeline:create <name> also has <name>
		// in docopt; that is the pipeline name, not a datastore instance.
		in.Resource = resname.Canonical(spec.Command, resourceName)
	case spec.ResourceEnv != "":
		in.Resource = in.App[spec.ResourceEnv]
		if in.Resource == "" {
			msg := spec.ResourceMissing
			if msg == "" {
				msg = fmt.Sprintf("No %s resource found. Provision one with `flynn resource:add %s`", spec.Command, spec.Command)
			}
			return in, nil, fmt.Errorf("%s", msg)
		}
	case spec.App != "":
		in.Resource = spec.App
	default:
		return in, nil, fmt.Errorf("%s plugin CLI does not declare a resource", spec.Command)
	}

	resRelease, err := client.GetAppRelease(in.Resource)
	if err != nil {
		return in, nil, fmt.Errorf("error getting %s release: %s", spec.Command, err)
	}
	if resRelease != nil {
		in.ResourceEnv = resRelease.Env
	}
	if strings.TrimSpace(resourceName) != "" && resRelease != nil {
		copied := make(map[string]string, len(in.App)+len(resRelease.Env))
		for key, val := range in.App {
			copied[key] = val
		}
		in.App = copied
		applyNamedResource(in.App, in.Resource, resRelease.Env)
	}
	return in, resRelease, nil
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
