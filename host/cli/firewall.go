package cli

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/pkg/hostfw"
)

func init() {
	Register("firewall", runFirewallStatus, `
usage: flynn-host firewall

Show flynn-host managed firewall rules (peer IPs and exposed TCP ports).
Public 22/80/443 and private cluster CIDRs are owned by the installer.
`)
	Register("firewall:sync", runFirewallSync, `
usage: flynn-host firewall:sync [--peer-ips <ips>] [--ports <ports>]

Reconcile UFW with known cluster peer IPs and exposed TCP ports.
--peer-ips and --ports seed extra allows stored for later syncs.

Options:
    --peer-ips=<ips>  comma-separated extra peer IPv4 addresses
    --ports=<ports>   comma-separated extra TCP ports to expose
`)
	Register("firewall:peer-add", runFirewallPeerAdd, `
usage: flynn-host firewall:peer-add <ip>

Allow all cluster traffic from a node IP. Use this on existing nodes before
joining a host whose address is not in the private CIDR ranges.
`)
	Register("firewall:peer-remove", runFirewallPeerRemove, `
usage: flynn-host firewall:peer-remove <ip>

Drop a previously allowed peer IP once that node has left the cluster.
`)
	Register("firewall:expose", runFirewallExpose, `
usage: flynn-host firewall:expose <port>

Open a TCP port on the host (for example a TCP route or an exported datastore).
22/80/443 stay installer-owned. flynn resource:expose prints this command after
it creates the TCP(/TLS) route. flynn-host also opens live TCP route ports on
its periodic firewall sync.
`)
	Register("firewall:unexpose", runFirewallUnexpose, `
usage: flynn-host firewall:unexpose <port>

Close a TCP port that flynn-host previously exposed.
`)
}

func runFirewallStatus(_ *docopt.Args) error {
	b := hostfw.UFWBackend{}
	have, err := b.List()
	if err != nil {
		return err
	}
	extra := hostfw.LoadExtra(hostfw.StatePath())
	fmt.Fprintf(os.Stderr, "state %s extra_peers=%s extra_ports=%v\n", hostfw.StatePath(), strings.Join(extra.Peers, ","), extra.Ports)
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	fmt.Fprintln(w, "KIND\tPORT\tFROM\tCOMMENT")
	managed := hostfw.Managed(have)
	sort.Slice(managed, func(i, j int) bool { return managed[i].Key() < managed[j].Key() })
	if len(managed) == 0 {
		fmt.Fprintln(w, "(none)\t\t\t")
		return w.Flush()
	}
	for _, r := range managed {
		from := r.From
		if from == "" {
			from = "Anywhere"
		}
		port := ""
		if r.Port > 0 {
			port = strconv.Itoa(r.Port)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Kind, port, from, r.Comment)
	}
	return w.Flush()
}

func runFirewallSync(args *docopt.Args) error {
	extra := hostfw.LoadExtra(hostfw.StatePath())
	if s := strings.TrimSpace(args.String["--peer-ips"]); s != "" {
		for _, ip := range strings.Split(s, ",") {
			parsed, err := hostfw.ParsePeerIP(ip)
			if err != nil {
				return err
			}
			extra = extra.WithPeer(parsed)
		}
	}
	if s := strings.TrimSpace(args.String["--ports"]); s != "" {
		for _, p := range strings.Split(s, ",") {
			port, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil {
				return fmt.Errorf("invalid port %q", p)
			}
			extra = extra.WithPort(port)
		}
	}
	if err := hostfw.SaveExtra(hostfw.StatePath(), extra); err != nil {
		return err
	}
	return hostfw.Reconcile(hostfw.UFWBackend{}, hostfw.MergeDesired("", extra, nil, nil))
}

func runFirewallPeerAdd(args *docopt.Args) error {
	ip, err := hostfw.ParsePeerIP(args.String["<ip>"])
	if err != nil {
		return err
	}
	return mutateFirewall(func(e hostfw.Extra) hostfw.Extra { return e.WithPeer(ip) })
}

func runFirewallPeerRemove(args *docopt.Args) error {
	ip, err := hostfw.ParsePeerIP(args.String["<ip>"])
	if err != nil {
		return err
	}
	return mutateFirewall(func(e hostfw.Extra) hostfw.Extra { return e.WithoutPeer(ip) })
}

func runFirewallExpose(args *docopt.Args) error {
	port, err := strconv.Atoi(args.String["<port>"])
	if err != nil {
		return fmt.Errorf("invalid port %q", args.String["<port>"])
	}
	return mutateFirewall(func(e hostfw.Extra) hostfw.Extra { return e.WithPort(port) })
}

func runFirewallUnexpose(args *docopt.Args) error {
	port, err := strconv.Atoi(args.String["<port>"])
	if err != nil {
		return fmt.Errorf("invalid port %q", args.String["<port>"])
	}
	return mutateFirewall(func(e hostfw.Extra) hostfw.Extra { return e.WithoutPort(port) })
}

func mutateFirewall(fn func(hostfw.Extra) hostfw.Extra) error {
	path := hostfw.StatePath()
	extra := fn(hostfw.LoadExtra(path))
	if err := hostfw.SaveExtra(path, extra); err != nil {
		return err
	}
	return hostfw.Reconcile(hostfw.UFWBackend{}, hostfw.MergeDesired("", extra, nil, nil))
}
