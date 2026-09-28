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
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cliutil"
	"github.com/randy-girard/flynn/pkg/shutdown"
	"github.com/randy-girard/flynn/pkg/version"
)

var (
	flagCluster = os.Getenv("FLYNN_CLUSTER")
	flagApp     string
)

// cliUsage is the root flynn parse string. Command is optional so `flynn`,
// `flynn -h`, and `flynn --help` reach formatRootHelp instead of docopt
// printing this string and exiting (which omitted installed plugin CLIs).
// The Commands list is generated from registered parent commands.
var cliUsage = `
usage: flynn [-h] [-a <app>] [-c <cluster>] [<command>] [<args>...]

Options:
	-a <app>
	-c <cluster>
	-h, --help
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
		fmt.Print(formatRootHelp())
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
		topic := helpTopic(cmdArgs[0], cmdArgs[1:])
		if !knownHelpTopic(topic) && !knownHelpTopic(cmdArgs[0]) {
			log.Printf("%q is not a Flynn command. See 'flynn help'.", cmdArgs[0])
			shutdown.ExitWithCode(1)
			return
		}
		fmt.Print(formatHelp(topic))
		return
	}
	if help || wantsHelp(cmdArgs) {
		topic := helpTopic(cmd, cmdArgs)
		if !knownHelpTopic(topic) && !knownHelpTopic(cmd) {
			log.Printf("%q is not a Flynn command. See 'flynn help'.", cmd)
			shutdown.ExitWithCode(1)
			return
		}
		fmt.Print(formatHelp(topic))
		return
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
		if err := bindGitRemoteApp(ra); err != nil {
			return err
		}
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

// clusterNameOverride is -c / FLYNN_CLUSTER, else the flynnrc default.
// Git remotes may still supply the app name; they do not override this cluster.
func clusterNameOverride() string {
	if name := strings.TrimSpace(flagCluster); name != "" {
		return name
	}
	if config != nil {
		return strings.TrimSpace(config.Default)
	}
	return ""
}

func selectCluster(conf *cfg.Config, name string, gitRemote *cfg.Cluster) (*cfg.Cluster, error) {
	if conf == nil || len(conf.Clusters) == 0 {
		return nil, ErrNoClusters
	}
	if name != "" {
		for _, s := range conf.Clusters {
			if s.Name == name {
				return s, nil
			}
		}
		return nil, fmt.Errorf("unknown cluster %q", name)
	}
	if gitRemote != nil {
		return gitRemote, nil
	}
	return conf.Clusters[0], nil
}

func getCluster() (*cfg.Cluster, error) {
	if err := readConfig(); err != nil {
		return nil, err
	}
	name := clusterNameOverride()
	if name != "" {
		c, err := selectCluster(config, name, nil)
		if err != nil {
			return nil, err
		}
		clusterConf = c
		return clusterConf, nil
	}
	if clusterConf != nil {
		return clusterConf, nil
	}
	if _, err := app(); err == nil && clusterConf != nil {
		return clusterConf, nil
	}
	c, err := selectCluster(config, "", nil)
	if err != nil {
		return nil, err
	}
	clusterConf = c
	return clusterConf, nil
}

func gitRemoteClusterMismatch(ra *remoteApp, clusterName string) error {
	if ra == nil || ra.Cluster == nil || clusterName == "" {
		return nil
	}
	if ra.Cluster.Name == clusterName {
		return nil
	}
	return fmt.Errorf("git remote is app %q on cluster %q; current cluster is %q. Use flynn -c %s ps or flynn -a <app> for cluster %q", ra.Name, ra.Cluster.Name, clusterName, ra.Cluster.Name, clusterName)
}

func bindGitRemoteApp(ra *remoteApp) error {
	if ra == nil {
		return errors.New("no app found, run from a repo with a flynn remote or specify one with -a")
	}
	if err := gitRemoteClusterMismatch(ra, clusterNameOverride()); err != nil {
		return err
	}
	flagApp = ra.Name
	if clusterNameOverride() == "" && clusterConf == nil {
		clusterConf = ra.Cluster
	}
	return nil
}

func currentClusterName() string {
	if clusterConf != nil && clusterConf.Name != "" {
		return clusterConf.Name
	}
	return clusterNameOverride()
}

func errAppNotOnCluster(err error, app string) error {
	if err == nil || err != ct.ErrNotFound {
		return err
	}
	app = strings.TrimSpace(app)
	if app == "" {
		return err
	}
	if name := currentClusterName(); name != "" {
		return fmt.Errorf("app %q not found on cluster %q", app, name)
	}
	return fmt.Errorf("app %q not found on this cluster", app)
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
	if err := bindGitRemoteApp(ra); err != nil {
		return "", err
	}
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
