package cli

import (
	"github.com/flynn/flynn/host/fixer"
)

func init() {
	Register("fix", (&fixer.ClusterFixer{}).Run, `
usage: flynn-host fix [options]

Attempts to fix a broken cluster by starting missing jobs and cleaning orphaned
image data on each host's local disk.

When discoverd is down, fix probes this host's flynn-host HTTP API on :1113
(127.0.0.1 and local interface IPs). Pass --peer-ips when that is not enough
(multi-host, or the daemon bound a different address).

Options:
    -n, --min-hosts=<n>  minimum expected number of hosts (required)
	--peer-ips=<iplist>  host IPs if discoverd is down and local :1113 is not enough
`)
}
