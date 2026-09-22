package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/bootstrap/discovery"
	"github.com/randy-girard/flynn/host/config"
	"github.com/randy-girard/flynn/pkg/hostfw"
)

func init() {
	Register("init", runInit, `
usage: flynn-host init [options]

options:
  --init-discovery    create a discovery token (requires DISCOVERY_SERVER, e.g. https://discovery.example.com)
  --discovery=TOKEN   join cluster with discovery token
  --peer-ips=IPLIST   join cluster using host IPs (must be already bootstrapped)
  --external-ip=IP    external IP address of host, defaults to the first IPv4 address of eth0
  --file=NAME         file to write to [default: /etc/flynn/host.json]
  `)
}

func runInit(args *docopt.Args) error {
	c := config.New()

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
	if err := c.WriteTo(file); err != nil {
		return err
	}
	// Persist pre-shared cluster secrets if the operator already exported
	// them (FLYNN_HOST_AUTH_KEY, DISCOVERD_AUTH_KEY, …). Do not generate a
	// unique key here: that would reject configure-host-auth on other
	// nodes, and a join without DISCOVERD_AUTH_KEY is unauthorized.
	return config.PersistEnvAuthKey(file)
}
