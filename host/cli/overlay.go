package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flynn/go-docopt"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/overlay"
)

func init() {
	Register("overlay:show", runOverlayShow, overlayShowUsage)
	Register("overlay:set", runOverlaySet, overlaySetUsage)
}

const overlayShowUsage = `
usage: flynn-host overlay:show

Show the live flannel overlay.

Prints the overlay CIDR, per-host subnet length, allocated leases, and
free capacity. A host with no stored overlay is using the /16 inferred
from its bridge subnet, which is the default install. Raft voters and
sirenia replica sizes are not read.

Examples:

    $ sudo flynn-host overlay:show
`

const overlaySetUsage = `
usage: flynn-host overlay:set --network=<cidr> [--subnet-len=<bits>]

Change the flannel overlay on a running cluster.

Writes discoverd flannel metadata and applies the CIDR on every host.
Existing leases stay. New hosts allocate from the expanded pool. Repeating
the same CIDR and subnet length does not write metadata. A host that is
behind is updated. If a host does not apply the CIDR, the command fails
and names that host. Fix the host and re-run the same command. Do not
shrink the overlay under allocated leases.

This does not restart jobs, change Raft voters, or change sirenia replica
sizes. FLANNEL_NETWORK and FLANNEL_SUBNET_LEN apply only when flannel
metadata does not exist yet.

Examples:

    $ sudo flynn-host overlay:set --network 100.64.0.0/10
    $ sudo flynn-host overlay:set --network 100.64.0.0/10 --subnet-len 24
    $ sudo flynn-host overlay:show

Options:
    --network=<cidr>     overlay CIDR, for example 100.64.0.0/10
    --subnet-len=<bits>  per-host prefix. Omitted keeps the current length
`

func runOverlayShow(_ *docopt.Args, client *cluster.Client) error {
	text, err := showOverlay(client, flannelMetaStore{})
	if err != nil {
		return err
	}
	fmt.Print(text)
	return nil
}

func runOverlaySet(args *docopt.Args, client *cluster.Client) error {
	network := strings.TrimSpace(args.String["--network"])
	var length uint
	if raw := strings.TrimSpace(args.String["--subnet-len"]); raw != "" {
		n, err := overlay.ParseSubnetLen(raw)
		if err != nil {
			return fmt.Errorf("invalid --subnet-len %q", raw)
		}
		length = n
	}
	text, err := setOverlay(client, flannelMetaStore{}, network, length)
	if err != nil {
		return err
	}
	fmt.Print(text)
	return nil
}

type flannelMetaStore struct{}

func (flannelMetaStore) Get() ([]byte, uint64, error) {
	meta, err := discoverd.NewService("flannel").GetMeta()
	if err != nil {
		return nil, 0, err
	}
	if meta == nil || len(meta.Data) == 0 {
		return nil, 0, errors.New("flannel meta is empty")
	}
	return append([]byte(nil), meta.Data...), meta.Index, nil
}

func (flannelMetaStore) Set(data []byte, index uint64) error {
	return discoverd.NewService("flannel").SetMeta(&discoverd.ServiceMeta{
		Data:  data,
		Index: index,
	})
}

func showOverlay(client *cluster.Client, store overlay.Store) (string, error) {
	data, _, err := store.Get()
	if err != nil {
		return "", err
	}
	change, err := overlay.Inspect(data)
	if err != nil {
		return "", err
	}
	hosts, _, err := overlayHostMap(client)
	if err != nil {
		return "", err
	}
	views := make([]overlay.HostView, len(hosts))
	for i, h := range hosts {
		views[i] = overlay.HostView{ID: h.ID, Subnet: h.Subnet, Overlay: h.Overlay}
	}
	return overlay.FormatShow(change, views), nil
}

func setOverlay(client *cluster.Client, store overlay.Store, network string, subnetLen uint) (string, error) {
	hosts, byID, err := overlayHostMap(client)
	if err != nil {
		return "", err
	}
	return overlay.Execute(store, hosts, func(id, network string) error {
		h := byID[id]
		if h == nil {
			return fmt.Errorf("host %s not found", id)
		}
		if err := h.ApplyOverlay(network); err != nil {
			if errors.Is(err, cluster.ErrNotFound) {
				return fmt.Errorf("%s does not expose overlay apply. Run flynn-host update on this host, then re-run overlay:set", id)
			}
			return err
		}
		return nil
	}, network, subnetLen)
}

func overlayHostMap(client *cluster.Client) ([]overlay.Host, map[string]*cluster.Host, error) {
	if client == nil {
		client = cluster.NewClient()
	}
	listed, err := client.Hosts()
	if err != nil {
		return nil, nil, fmt.Errorf("could not list hosts: %s", err)
	}
	if len(listed) == 0 {
		return nil, nil, errors.New("no hosts found")
	}
	out := make([]overlay.Host, 0, len(listed))
	byID := make(map[string]*cluster.Host, len(listed))
	for _, h := range listed {
		status, err := h.GetStatus()
		if err != nil {
			return nil, nil, fmt.Errorf("host %s status: %s", h.ID(), err)
		}
		item := overlay.Host{ID: h.ID(), Underlay: h.Addr()}
		if status != nil && status.Network != nil {
			item.Subnet = status.Network.Subnet
			item.Overlay = status.Network.Overlay
		}
		out = append(out, item)
		byID[h.ID()] = h
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, byID, nil
}
