package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/clihelp"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/plugin"
	"github.com/randy-girard/flynn/pkg/term"
)

// lookupHostPluginCLI finds a plugin CLI spec for flynn-host. Tests replace it.
var lookupHostPluginCLI = lookupHostPluginCLIDefault

func lookupHostPluginCLIDefault(name string) (*plugin.CLI, error) {
	return installedHostPluginCLI(name), nil
}

func installedHostPluginCLI(name string) *plugin.CLI {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if i := strings.IndexByte(name, ':'); i > 0 {
		name = name[:i]
	}
	for _, p := range plugin.ReadInstalled("") {
		if p.CLI == nil || strings.TrimSpace(p.CLI.Command) == "" {
			continue
		}
		if p.CLI.Command == name || p.MatchesName(name) {
			cli := *p.CLI
			if cli.App == "" {
				cli.App = p.Name
			}
			return &cli
		}
	}
	return nil
}

func splitHostPluginCommand(name string, args []string) (string, []string) {
	if i := strings.IndexByte(name, ':'); i > 0 && i < len(name)-1 {
		suffix := name[i+1:]
		name = name[:i]
		tokens := strings.Split(suffix, ":")
		args = append(tokens, args...)
	}
	return name, args
}

func runHostPluginCommand(name string, args []string) error {
	pluginName, extra := splitHostPluginCommand(name, args)
	spec, err := lookupHostPluginCLI(pluginName)
	if err != nil {
		return err
	}
	if spec == nil || !spec.Runnable() {
		return ErrInvalidCommand
	}
	if !spec.HasClusterActions() {
		if spec.HasFlynnVisibleActions() {
			return fmt.Errorf("%s is a flynn command; run `%s`", pluginName, spec.FlynnRedirect(nil))
		}
		return ErrInvalidCommand
	}

	argv := make([]string, 1, 1+len(extra))
	argv[0] = pluginName
	argv = append(argv, extra...)
	parsed, err := docopt.Parse(spec.DocoptUsage(), argv, true, "", false)
	if err != nil {
		return err
	}
	plugin.FoldColonBools(spec, parsed.Bool)
	action := spec.MatchAction(parsed.Bool)
	if action == nil {
		return fmt.Errorf("%s: no matching plugin CLI action", spec.Command)
	}
	if action.EffectiveScope() != plugin.CLIScopeCluster {
		return fmt.Errorf("%s is a flynn command; run `%s`", plugin.ColonName(spec.Command, action.Name), spec.FlynnRedirect(action))
	}
	client, err := ClusterController()
	if err != nil {
		return err
	}
	return executeHostPluginAction(client, spec, action, extra)
}

func executeHostPluginAction(client controller.Client, spec *plugin.CLI, action *plugin.CLIAction, extra []string) error {
	if spec == nil || action == nil {
		return fmt.Errorf("missing plugin CLI action")
	}
	appName := strings.TrimSpace(spec.App)
	if appName == "" {
		return fmt.Errorf("%s plugin CLI is missing the plugin app name", spec.Command)
	}
	release, err := client.GetAppRelease(appName)
	if err != nil {
		return fmt.Errorf("error getting app release: %s", err)
	}
	if release == nil || release.ID == "" {
		return fmt.Errorf("error getting %s release", spec.Command)
	}
	in := plugin.Interp{AppName: appName, Resource: appName}
	if release.Env != nil {
		in.App = release.Env
		in.ResourceEnv = release.Env
	}
	jobArgs, err := plugin.InterpolateAll(action.Args, in)
	if err != nil {
		return err
	}
	jobArgs = compactHostPluginArgs(jobArgs)
	if action.Passthrough {
		jobArgs = append(jobArgs, extra...)
	}
	req := &ct.NewJob{
		Args:                 jobArgs,
		ReleaseID:            release.ID,
		ReleaseEnv:           action.ReleaseEnv,
		DisableLog:           true,
		Data:                 action.Data,
		Partition:            ct.PartitionTypeSystem,
		DeprecatedEntrypoint: nil,
	}
	if len(jobArgs) > 0 {
		req.DeprecatedEntrypoint = []string{jobArgs[0]}
	}
	if len(jobArgs) > 1 {
		req.DeprecatedCmd = jobArgs[1:]
	}
	return runHostPluginJob(client, appName, req, os.Stdin, os.Stdout, os.Stderr)
}

func compactHostPluginArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != "" {
			out = append(out, a)
		}
	}
	return out
}

func runHostPluginJob(client controller.Client, app string, req *ct.NewJob, stdin io.Reader, stdout, stderr io.Writer) error {
	if req == nil {
		return fmt.Errorf("missing job")
	}
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	if inFile, ok := stdin.(*os.File); ok && stdout == os.Stdout && term.IsTerminal(inFile.Fd()) && term.IsTerminal(os.Stdout.Fd()) {
		if err := preparePlatformJobTTY(req, inFile); err != nil {
			return err
		}
	}
	rwc, err := client.RunJobAttached(app, req)
	if err != nil {
		return err
	}
	defer rwc.Close()
	attach := cluster.NewAttachClient(rwc)
	var termState *term.State
	if req.TTY {
		inFile, ok := stdin.(*os.File)
		if !ok {
			return fmt.Errorf("plugin TTY job requires a local terminal")
		}
		termState, err = term.MakeRaw(inFile.Fd())
		if err != nil {
			return err
		}
		defer term.RestoreTerminal(inFile.Fd(), termState)
		go func() {
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, syscall.SIGWINCH)
			for range ch {
				ws, err := term.GetWinsize(inFile.Fd())
				if err != nil {
					return
				}
				_ = attach.ResizeTTY(ws.Height, ws.Width)
				_ = attach.Signal(int(syscall.SIGWINCH))
			}
		}()
	}
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		sig := <-ch
		_ = attach.Signal(int(sig.(syscall.Signal)))
		time.Sleep(10 * time.Second)
		_ = attach.Signal(int(syscall.SIGKILL))
	}()
	go func() {
		_, _ = io.Copy(attach, stdin)
		attach.CloseWrite()
	}()
	exitStatus, err := attach.Receive(stdout, stderr)
	if err != nil {
		return err
	}
	if exitStatus != 0 {
		return fmt.Errorf("remote job exited with status %d", exitStatus)
	}
	return nil
}

func formatHostPluginHelp(name string) string {
	pluginName, extra := splitHostPluginCommand(name, nil)
	spec := installedHostPluginCLI(pluginName)
	if spec == nil || !spec.HasClusterActions() {
		return ""
	}
	if len(extra) > 0 {
		actionName := strings.Join(extra, " ")
		if a := spec.ActionByName(actionName); a != nil {
			help := spec.ActionHelp(plugin.ColonName(spec.Command, a.Name))
			help = strings.ReplaceAll(help, "flynn ", "flynn-host ")
			if strings.TrimSpace(help) != "" {
				return strings.TrimRight(help, "\n") + "\n"
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "usage: flynn-host %s\n", spec.Command)
	fmt.Fprintf(&b, "       flynn-host %s <command> [<args>...]\n", spec.Command)
	if spec.Usage != "" {
		b.WriteByte('\n')
		b.WriteString(spec.Usage)
		b.WriteByte('\n')
	}
	var items []clihelp.Item
	for _, a := range spec.Actions {
		if a.EffectiveScope() != plugin.CLIScopeCluster || strings.TrimSpace(a.Name) == "" {
			continue
		}
		full := plugin.ColonName(spec.Command, a.Name)
		desc := clihelp.ShortDescription(spec.ActionHelp(full))
		if desc == "" {
			desc = a.Name
		}
		items = append(items, clihelp.Item{Name: full, Desc: desc})
	}
	if len(items) > 0 {
		b.WriteString("\nCommands:\n")
		b.WriteString(clihelp.FormatItems(items))
	}
	return b.String()
}
