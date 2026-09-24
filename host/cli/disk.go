package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/pkg/cluster"
)

func init() {
	Register("disk:reclaim", runDiskReclaim, diskReclaimUsage)
}

const diskReclaimUsage = `
usage: flynn-host disk:reclaim [--host <id>]

Free unused Flynn disk on cluster hosts without deleting app data.

Garbage-collects volumes that no job or controller record uses (same keep
rules as volume:gc), deletes leftover per-job image dirs and unreferenced
layer-cache files, then waits for ZFS TRIM so a file-backed pool can punch
holes in the sparse vdev. Persistent Postgres/plugin volumes stay. TRIM is
skipped when the pool is missing or the vdev does not support it.

Examples:

    $ sudo flynn-host disk:reclaim
    $ sudo flynn-host disk:reclaim --host localhost

Options:
    --host=<id>  Reclaim one host (default: every host)
`

func runDiskReclaim(args *docopt.Args, client *cluster.Client) error {
	hosts, err := filterReclaimHosts(client, strings.TrimSpace(args.String["--host"]))
	if err != nil {
		return err
	}

	fmt.Println("Garbage-collecting unused volumes...")
	if err := garbageCollectUnusedVolumes(hosts, nil); err != nil {
		return err
	}

	failed := false
	for _, h := range hosts {
		fmt.Printf("Cleaning image cache and trimming ZFS on %s...\n", h.ID())
		if err := h.ReclaimDisk(); err != nil {
			failed = true
			fmt.Printf("could not reclaim disk on %s: %s\n", h.ID(), err)
			continue
		}
		stats, err := h.GetStats()
		if err != nil {
			fmt.Printf("%s: reclaim finished\n", h.ID())
			continue
		}
		fmt.Printf("%s: %s free on %s (%s used of %s)\n",
			h.ID(),
			formatBytesIEC(stats.DiskFreeBytes),
			stats.DiskPath,
			formatBytesIEC(stats.DiskUsedBytes),
			formatBytesIEC(stats.DiskTotalBytes),
		)
	}
	if failed {
		return errors.New("could not reclaim disk on all hosts")
	}
	return nil
}

func filterReclaimHosts(client *cluster.Client, hostID string) ([]*cluster.Host, error) {
	hosts, err := client.Hosts()
	if err != nil {
		return nil, fmt.Errorf("could not list hosts: %s", err)
	}
	if len(hosts) == 0 {
		return nil, errors.New("no hosts found")
	}
	if hostID == "" {
		return hosts, nil
	}
	for _, h := range hosts {
		if h.ID() == hostID {
			return []*cluster.Host{h}, nil
		}
	}
	return nil, fmt.Errorf("host %s not found", hostID)
}
