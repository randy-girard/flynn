package main

import (
	"strings"

	"github.com/opencontainers/runc/libcontainer/configs"
	"github.com/randy-girard/flynn/pkg/ifname"
	"github.com/vishvananda/netlink"
)

// isNetworkIfaceExistsErr is the runc EEXIST from process_linux.go when a
// leftover host veth still occupies HostInterfaceName. Seen after an AppArmor
// apply failure: the first Run created the veth, Destroy did not always drop
// it before the unconfined retry reused the same name (2026-09-20 5-node
// bootstrap postgres on node5).
func isNetworkIfaceExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "creating network interfaces") && strings.Contains(msg, "file exists")
}

func deleteLinkByName(name string) {
	if name == "" {
		return
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		return
	}
	_ = netlink.LinkDel(link)
}

// regenerateVethHostNames drops leftover host veths and assigns new names so
// a container retry does not hit EEXIST.
func regenerateVethHostNames(config *configs.Config) error {
	return regenerateVethHostNamesWith(config, deleteLinkByName)
}

func regenerateVethHostNamesWith(config *configs.Config, del func(string)) error {
	if config == nil {
		return nil
	}
	for _, n := range config.Networks {
		if n == nil || n.Type != "veth" {
			continue
		}
		old := n.HostInterfaceName
		if del != nil {
			del(old)
		}
		name, err := ifname.Generate("veth", 4)
		if err != nil {
			return err
		}
		n.HostInterfaceName = name
	}
	return nil
}
