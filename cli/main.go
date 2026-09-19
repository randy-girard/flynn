package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/docker/go-units"
	"github.com/flynn/go-docopt"
	cfg "github.com/randy-girard/flynn/cli/config"
	controller "github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/pkg/cliutil"
	"github.com/randy-girard/flynn/pkg/shutdown"
	"github.com/randy-girard/flynn/pkg/version"
)

var (
	flagCluster = os.Getenv("FLYNN_CLUSTER")
	flagApp     string
)

// cliUsage is the root flynn help text. Command is optional so `flynn`,
// `flynn -h`, and `flynn --help` reach pluginAwareUsage instead of docopt
// printing this string and exiting (which omitted installed plugin CLIs).
var cliUsage = `
usage: flynn [-h] [-a <app>] [-c <cluster>] [<command>] [<args>...]

Options:
	-a <app>
	-c <cluster>
	-h, --help

Commands:
	alert                list app metric alerts
	alert:add            add an app metric alert
	alert:disable        disable an app metric alert
	alert:enable         enable an app metric alert
	alert:remove         delete an app metric alert
	apps                 list apps
	apps:create          create an app
	apps:destroy         delete an app
	apps:export          export app data
	apps:import          create app from exported data
	apps:info            show app information
	cluster              list CLI cluster configs
	cluster:add          add a cluster to ~/.flynnrc
	cluster:default      get or set the default cluster
	cluster:refresh      refresh TLS pin and URLs in ~/.flynnrc
	cluster:remove       remove a cluster from ~/.flynnrc
	deploy               list deployments
	deploy:batch-size    get or set in-batches size
	deploy:timeout       get or set deploy timeout
	docker:push          push a Docker image
	env                  list env variables
	env:get              get an env variable
	env:set              set env variables
	env:unset            unset env variables
	git:remote           add a git remote for the app
	github               show connected GitHub repo
	github:connect       connect a GitHub repo
	github:deploy        deploy from GitHub
	github:disconnect    disconnect GitHub
	github:set           GitHub auto-deploy settings
	help                 show usage for a specific command
	limit                list resource limits
	limit:profile        apply a runtime environment to a process type
	limit:profiles       list cluster runtime environments
	limit:set            set resource limits
	log                  get app log
	log-sink             list app log sinks
	log-sink:add         add an app log sink
	log-sink:remove      remove an app log sink
	login                authenticate with the dashboard (OAuth)
	meta                 list app metadata
	meta:set             set app metadata
	meta:unset           unset app metadata
	metrics              print the latest app metrics snapshot
	pg:dump              dump a postgres database
	pg:psql              postgres console
	pg:restore           restore a postgres dump
	plugin:list          list plugins installed on this cluster (--known for official plugins)
	provider             list resource providers
	provider:add         add a resource provider
	ps                   list jobs
	ps:kill              kill jobs
	ps:run               run a job
	ps:scale             change formation
	release              list app releases
	release:add          add a release
	release:destroy      delete a release
	release:rollback     rollback to a previous release
	release:show         show a release
	release:update       update a release
	resource             list app resources
	resource:add         provision a resource
	resource:expose      export a datastore on a TLS TCP route
	resource:remove      remove a resource
	resource:unexpose    remove a datastore TCP export route
	route                list routes
	route:add            add a route
	route:remove         remove a route
	route:update         update a route
	run                  run a job (shorthand for ps:run)
	scale                change formation (shorthand for ps:scale)
	stack                show git-push stack
	stack:set            set git-push stack
	update               update the Flynn CLI from GitHub Releases
	version              show flynn version
	volume               list volumes
	volume:decommission  decommission a volume
	volume:show          show a volume

See 'flynn help <command>' for more information on a specific command.
`[1:]

func main() {
	defer shutdown.Exit()

	log.SetFlags(0)

	updater.notifyIfUpdateAvailable()

	if leadingVersionFlag(os.Args[1:]) {
		fmt.Println(version.String())
		return
	}

	// help=false: docopt must not print cliUsage on -h/--help. Installed
	// plugin commands (redis, …) are merged from the cluster catalog.
	args, _ := docopt.Parse(cliUsage, nil, false, version.String(), true)
	if err := applyGlobalFlags(args); err != nil {
		shutdown.Fatal(err)
	}

	cmd, cmdArgs := positionalArgs(args)
	help := helpFlag(args)

	if cmd == "" || (cmd == "help" && len(cmdArgs) == 0) {
		fmt.Println(pluginAwareUsage(cliUsage))
		return
	}

	if cmd == "help" && cmdArgs[0] == "--json" {
		cmds := make(map[string]string)
		for name, c := range commands {
			cmds[name] = c.usage
		}
		if cat, err := clusterPluginCatalog(); err == nil {
			for _, p := range cat.Commands {
				if p.Doc != "" {
					cmds[p.Command] = p.Doc
				} else if _, ok := cmds[p.Command]; !ok && p.Usage != "" {
					cmds[p.Command] = p.Usage
				}
			}
		}
		out, err := json.MarshalIndent(cmds, "", "\t")
		if err != nil {
			shutdown.Fatal(err)
		}
		fmt.Println(string(out))
		return
	}

	if cmd == "help" {
		cmd = cmdArgs[0]
		cmdArgs = []string{"--help"}
	} else if help {
		cmdArgs = []string{"--help"}
	}

	if err := runCommand(cmd, cmdArgs); err != nil {
		log.Println(err)
		if needsFlynnLoginHint(err) {
			log.Println("Reauthentication required. Run `flynn login` to get new credentials.")
		}
		shutdown.ExitWithCode(1)
		return
	}
}

func applyGlobalFlags(args *docopt.Args) error {
	if args == nil {
		return nil
	}
	if args.String["-c"] != "" {
		flagCluster = args.String["-c"]
	}
	flagApp = args.String["-a"]
	if flagApp == "" {
		return nil
	}
	if err := readConfig(); err != nil {
		return err
	}
	if ra, err := appFromGitRemote(flagApp); err == nil {
		clusterConf = ra.Cluster
		flagApp = ra.Name
	}
	return nil
}

func positionalArgs(args *docopt.Args) (string, []string) {
	if args == nil {
		return "", nil
	}
	cmd := args.String["<command>"]
	return cmd, cliutil.List(args, "<args>")
}

func helpFlag(args *docopt.Args) bool {
	return args != nil && (args.Bool["--help"] || args.Bool["-h"])
}

func leadingVersionFlag(argv []string) bool {
	for _, a := range argv {
		if a == "--version" {
			return true
		}
		if a == "-h" || a == "--help" {
			continue
		}
		return false
	}
	return false
}

// needsFlynnLoginHint reports whether err likely means dashboard OAuth tokens are
// missing, revoked, or expired — user should run `flynn login` again.
func needsFlynnLoginHint(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "invalid_grant") {
		return true
	}
	if strings.Contains(msg, "invalid_token") {
		return true
	}
	if strings.Contains(msg, "token_expired") {
		return true
	}
	if strings.Contains(msg, "expired token") {
		return true
	}
	if strings.Contains(msg, "id_token expired") {
		return true
	}
	if strings.Contains(msg, "refresh token") && strings.Contains(msg, "invalid") {
		return true
	}
	if strings.Contains(msg, "unauthorized_client") {
		return true
	}
	if strings.Contains(msg, "access_denied") {
		return true
	}
	if strings.Contains(msg, "not_before_claim") {
		return true
	}
	return false
}

type command struct {
	usage     string
	f         interface{}
	optsFirst bool
}

var commands = make(map[string]*command)

func register(cmd string, f interface{}, usage string) *command {
	switch f.(type) {
	case func(*docopt.Args, controller.Client) error, func(*docopt.Args) error, func() error, func():
	default:
		panic(fmt.Sprintf("invalid command function %s '%T'", cmd, f))
	}
	c := &command{usage: strings.TrimLeftFunc(usage, unicode.IsSpace), f: f}
	commands[cmd] = c
	return c
}

func runCommand(name string, args []string) (err error) {
	resolved, rest, from := resolveCommand(name, args)
	if from != "" {
		printCommandRename(from, resolved)
		name, args = resolved, rest
	}

	argv := make([]string, 1, 1+len(args))
	argv[0] = name
	argv = append(argv, args...)

	cmd, ok := commands[name]
	if !ok {
		if base, suffix, ok := splitColonCommand(name); ok {
			if to, from := pluginColonRename(name); from != "" {
				printCommandRename(from, to)
			}
			pluginArgs := append(expandColonSuffix(base, suffix), args...)
			return runPluginCommand(base, pluginArgs)
		}
		if to, from := pluginSpaceAlias(name, args); from != "" {
			printCommandRename(from, to)
		}
		return runPluginCommand(name, args)
	}
	if err := requirePluginCommand(name); err != nil {
		return err
	}
	parsedArgs, err := docopt.Parse(cmd.usage, argv, true, "", cmd.optsFirst)
	if err != nil {
		return err
	}

	switch f := cmd.f.(type) {
	case func(*docopt.Args, controller.Client) error:
		// create client and run command
		client, err := getClusterClient()
		if err != nil {
			shutdown.Fatal(err)
		}

		return f(parsedArgs, client)
	case func(*docopt.Args) error:
		return f(parsedArgs)
	case func() error:
		return f()
	case func():
		f()
		return nil
	}

	return fmt.Errorf("unexpected command type %T", cmd.f)
}

var config *cfg.Config
var clusterConf *cfg.Cluster

func configPath() string {
	return cfg.DefaultPath()
}

func readConfig() (err error) {
	if config != nil {
		return nil
	}
	config, err = cfg.ReadFile(configPath())
	if os.IsNotExist(err) {
		err = nil
	}
	if config.Upgrade() {
		if err := config.SaveTo(configPath()); err != nil {
			return fmt.Errorf("Error saving upgraded config: %s", err)
		}
	}
	return
}

func getClusterClient() (controller.Client, error) {
	cluster, err := getCluster()
	if err != nil {
		return nil, err
	}
	return cluster.Client()
}

var ErrNoClusters = errors.New("no clusters configured")

func getCluster() (*cfg.Cluster, error) {
	app() // try to look up and cache app/cluster from git remotes
	if clusterConf != nil {
		return clusterConf, nil
	}
	if err := readConfig(); err != nil {
		return nil, err
	}
	if len(config.Clusters) == 0 {
		return nil, ErrNoClusters
	}
	name := flagCluster
	// Get the default cluster
	if name == "" {
		name = config.Default
	}
	// Default cluster not set, pick the first one
	if name == "" {
		clusterConf = config.Clusters[0]
		return clusterConf, nil
	}
	for _, s := range config.Clusters {
		if s.Name == name {
			clusterConf = s
			return s, nil
		}
	}
	return nil, fmt.Errorf("unknown cluster %q", name)
}

func app() (string, error) {
	if flagApp != "" {
		return flagApp, nil
	}
	if app := os.Getenv("FLYNN_APP"); app != "" {
		flagApp = app
		return app, nil
	}
	if err := readConfig(); err != nil {
		return "", err
	}

	ra, err := appFromGitRemote(remoteFromGitConfig())
	if err != nil {
		return "", err
	}
	if ra == nil {
		return "", errors.New("no app found, run from a repo with a flynn remote or specify one with -a")
	}
	clusterConf = ra.Cluster
	flagApp = ra.Name
	return ra.Name, nil
}

func mustApp() string {
	name, err := app()
	if err != nil {
		log.Println(err)
		shutdown.ExitWithCode(1)
	}
	return name
}

func tabWriter() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
}

func humanTime(ts *time.Time) string {
	if ts == nil || ts.IsZero() {
		return ""
	}
	return units.HumanDuration(time.Now().UTC().Sub(*ts)) + " ago"
}

func listRec(w io.Writer, a ...interface{}) {
	for i, x := range a {
		fmt.Fprint(w, x)
		if i+1 < len(a) {
			w.Write([]byte{'\t'})
		} else {
			w.Write([]byte{'\n'})
		}
	}
}

func compatCheck(client controller.Client, minVersion string) (bool, error) {
	status, err := client.Status()
	if err != nil {
		return false, err
	}
	v := version.Parse(status.Version)
	return v.Dev || !v.Before(version.Parse(minVersion)), nil
}
