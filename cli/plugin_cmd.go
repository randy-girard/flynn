package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cheggaaa/pb"
	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cliutil"
	"github.com/randy-girard/flynn/pkg/plugin"
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

	argv := make([]string, 1, 1+len(args))
	argv[0] = name
	argv = append(argv, args...)
	parsed, err := docopt.Parse(spec.Doc, argv, true, "", false)
	if err != nil {
		return err
	}
	return executePluginCLI(client, spec, parsed, args)
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
	appName := mustApp()
	appRelease, err := client.GetAppRelease(appName)
	if err != nil {
		return nil, fmt.Errorf("error getting app release: %s", err)
	}

	in, resourceRelease, err := pluginInterp(client, spec, appRelease)
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
	if action.Append != "" {
		jobArgs = append(jobArgs, cliutil.List(args, action.Append)...)
	}

	env := make(map[string]string, len(action.Env))
	for k, v := range action.Env {
		s, err := plugin.Interpolate(v, in)
		if err != nil {
			return nil, err
		}
		env[k] = s
	}

	return &runConfig{
		App:        appName,
		Release:    resourceRelease.ID,
		Env:        env,
		Args:       jobArgs,
		DisableLog: true,
		Exit:       true,
	}, nil
}

func pluginInterp(client appReleaseGetter, spec *plugin.CLI, appRelease *ct.Release) (plugin.Interp, *ct.Release, error) {
	in := plugin.Interp{App: map[string]string{}}
	if appRelease != nil && appRelease.Env != nil {
		in.App = appRelease.Env
	}

	switch {
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
	return in, resRelease, nil
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
