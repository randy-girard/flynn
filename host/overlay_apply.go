package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"

	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/iptables"
	"github.com/randy-girard/flynn/pkg/overlay"
)

// ApplyOverlayNetwork excludes network from masquerade and installs the
// flannel.1 link route for it. The bridge subnet and existing leases stay.
// An empty stored overlay keeps today's /16 inference until this runs.
func (h *Host) ApplyOverlayNetwork(network string) (string, error) {
	network = strings.TrimSpace(network)
	_, ipnet, err := net.ParseCIDR(network)
	if err != nil || ipnet == nil || ipnet.IP.To4() == nil {
		return "", fmt.Errorf("invalid overlay network %q", network)
	}
	network = ipnet.String()

	h.statusMtx.RLock()
	var subnet string
	if h.status != nil && h.status.Network != nil {
		subnet = h.status.Network.Subnet
	}
	h.statusMtx.RUnlock()
	if strings.TrimSpace(subnet) == "" {
		return "", errors.New("host has no bridge subnet yet; start flannel and re-run overlay:set")
	}

	bridge := "flynnbr0"
	source := subnet
	if l, ok := h.backend.(*LibcontainerBackend); ok && l != nil {
		if l.BridgeName != "" {
			bridge = l.BridgeName
		}
		if l.bridgeNet != nil {
			source = l.bridgeNet.String()
		}
	}
	if err := iptables.UseOverlayExclusion(bridge, source, network); err != nil {
		return "", err
	}
	if err := syncFlannelLinkRoute(h, subnet, network); err != nil {
		return "", err
	}

	h.statusMtx.Lock()
	defer h.statusMtx.Unlock()
	if h.status == nil {
		h.status = &host.HostStatus{}
	}
	if h.status.Network == nil {
		h.status.Network = &host.NetworkConfig{Subnet: subnet}
	}
	h.status.Network.Overlay = network
	h.backend.SetNetworkConfig(h.status.Network)
	return network, nil
}

func syncFlannelLinkRoute(h *Host, hostSubnet, desired string) error {
	h.statusMtx.RLock()
	stored := ""
	if h.status != nil && h.status.Network != nil {
		stored = h.status.Network.Overlay
	}
	h.statusMtx.RUnlock()

	add, del := overlay.LinkRoutePlan(stored, hostSubnet, desired)
	link, err := netlink.LinkByName("flannel.1")
	if err != nil {
		return fmt.Errorf("flannel.1 is not up; start flannel on this host and re-run overlay:set: %s", err)
	}
	if del != "" {
		if err := delLinkRoute(link, del); err != nil {
			return err
		}
	}
	if add != "" {
		if err := addLinkRoute(link, add); err != nil {
			return err
		}
	}
	return nil
}

func addLinkRoute(link netlink.Link, cidr string) error {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return err
	}
	err = netlink.RouteAdd(&netlink.Route{
		LinkIndex: link.Attrs().Index,
		Scope:     netlink.SCOPE_LINK,
		Dst:       n,
	})
	if err != nil && err != syscall.EEXIST && !errors.Is(err, syscall.EEXIST) {
		return fmt.Errorf("adding overlay route %s: %s", cidr, err)
	}
	return nil
}

func delLinkRoute(link netlink.Link, cidr string) error {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return err
	}
	err = netlink.RouteDel(&netlink.Route{
		LinkIndex: link.Attrs().Index,
		Scope:     netlink.SCOPE_LINK,
		Dst:       n,
	})
	if err != nil && err != syscall.ESRCH && err != syscall.ENOENT && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("removing overlay route %s: %s", cidr, err)
	}
	return nil
}
