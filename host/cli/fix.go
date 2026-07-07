package cli

import (
	"github.com/flynn/flynn/host/fixer"
)

func init() {
	Register("fix", (&fixer.ClusterFixer{}).Run, `
usage: flynn-host fix [options]

Attempts to fix a broken cluster by starting missing jobs, cleaning orphaned
image data on each host, reconciling stale sirenia volume records in the
controller, rebuilding postgres/mariadb/mongodb when discoverd cluster
state or volume references are inconsistent, and ensuring each database's
web process is running so resource add/remove APIs are available.

Safe cleanup removes orphaned image tmp/mnt directories and unreferenced
layer-cache files only. Database and storage volume data on hosts is never
deleted; missing controller volume records for sirenia apps are decommissioned
so the scheduler can provision replacements.

Options:
    -n, --min-hosts=<n>  minimum expected number of hosts (required)
	--peer-ips=<iplist>  list of host IPs (required if discoverd is down)
`)
}
