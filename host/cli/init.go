package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/bootstrap/discovery"
	"github.com/randy-girard/flynn/host/config"
	"github.com/randy-girard/flynn/pkg/cliutil"
	"github.com/randy-girard/flynn/pkg/hostfw"
)

func init() {
	Register("init", runInit, `
usage: flynn-host init [--init-discovery] [--discovery=TOKEN] [--peer-ips=IPLIST] [--external-ip=IP] [--tag <tag>]... [--role=<role>] [--max-processes=N] [--join-token=TOKEN] [--file=NAME]

options:
  --init-discovery    create a discovery token (requires DISCOVERY_SERVER and CONTROLLER_KEY)
  --discovery=TOKEN   join cluster with discovery token
  --peer-ips=IPLIST   join cluster using host IPs (must be already bootstrapped)
  --external-ip=IP    external IP address of host, defaults to the first IPv4 address of eth0
  --tag=<tag>         placement tag (KEY or KEY=VAL) written into host.json for the daemon
  --role=<role>       core or app, written as flynn-role next to --tag (omit to keep an existing flynn-role)
  --max-processes=N   tenant job cap (omit on a new host.json = from runtime sizes; 0 = unlimited)
  --join-token=TOKEN  one-time token a remote flynn-host bootstrap must send to set this
                      host's auth key (or set FLYNN_HOST_JOIN_TOKEN); use the same value
                      on every host, at least 16 characters
  --file=NAME         file to write to [default: /etc/flynn/host.json]

A host without FLYNN_HOST_AUTH_KEY lets only this machine set its key, unless
it has a join token. Multi-node bootstrap sets every host's key from one
coordinator, so give every host the same --join-token, or export the cluster
FLYNN_HOST_AUTH_KEY before init.
  `)
}

// minJoinTokenLen keeps a join token out of reach of the per-IP limiter
// (100 guesses a minute).
const minJoinTokenLen = 16

func runInit(args *docopt.Args) error {
	c := config.New()

	if tok := args.String["--join-token"]; tok != "" {
		os.Setenv("FLYNN_HOST_JOIN_TOKEN", tok)
	}
	if tok := os.Getenv("FLYNN_HOST_JOIN_TOKEN"); tok != "" && len(tok) < minJoinTokenLen {
		return fmt.Errorf("join token must be at least %d characters (try: openssl rand -hex 16)", minJoinTokenLen)
	}

	discoveryToken := args.String["--discovery"]
	if args.Bool["--init-discovery"] {
		var err error
		discoveryToken, err = discovery.NewToken()
		if err != nil {
			return err
		}
		fmt.Println(discoveryToken)
	}
	if discoveryToken != "" {
		c.Args = append(c.Args, "--discovery", discoveryToken)
	}
	if ip := args.String["--external-ip"]; ip != "" {
		c.Args = append(c.Args, "--external-ip", ip)
	}
	if ips := args.String["--peer-ips"]; ips != "" {
		c.Args = append(c.Args, "--peer-ips", ips)
		// Open those IPs on this host immediately so daemon join can reach them
		// even when they are not in the installer RFC1918 CIDRs.
		if err := hostfw.ApplySeedPeers(strings.Split(ips, ",")); err != nil {
			fmt.Fprintf(os.Stderr, "warn: firewall peer seed: %v\n", err)
		}
	}
	file := args.String["--file"]
	var existing map[string]string
	if prev, err := config.Open(file); err == nil {
		existing, _ = tagsFromDaemonArgs(prev.Args)
		if existing == nil {
			existing = map[string]string{}
		}
	}
	tagsArg, err := formatDaemonTagsArg(cliutil.List(args, "--tag"), args.String["--max-processes"], args.String["--role"], existing)
	if err != nil {
		return err
	}
	if tagsArg != "" {
		c.Args = append(c.Args, "--tags", tagsArg)
	}

	if err := c.WriteTo(file); err != nil {
		return err
	}
	// Persist pre-shared cluster secrets if the operator already exported
	// them (FLYNN_HOST_AUTH_KEY, DISCOVERD_AUTH_KEY, FLYNN_HOST_JOIN_TOKEN, …). Do not generate a
	// unique key here: that would reject configure-host-auth on other
	// nodes, and a join without DISCOVERD_AUTH_KEY is unauthorized.
	return config.PersistEnvAuthKey(file)
}
