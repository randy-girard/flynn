package main

import (
	"time"

	"github.com/inconshreveable/log15"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/host/cli"
	"github.com/randy-girard/flynn/pkg/hostfw"
	"github.com/randy-girard/flynn/pkg/iptables"
)

func startHostFirewall(selfIP string, seedPeers []string, log log15.Logger) {
	if log == nil {
		log = log15.New("component", "hostfw")
	}
	if err := hostfw.ApplySeedPeers(seedPeers); err != nil {
		log.Warn("firewall seed peers", "err", err)
	}
	go func() {
		for {
			syncHostFirewall(selfIP, seedPeers, log)
			time.Sleep(15 * time.Second)
		}
	}()
}

func syncHostFirewall(selfIP string, seedPeers []string, log log15.Logger) {
	extra := hostfw.LoadExtra(hostfw.StatePath())
	livePeers := liveFirewallPeers(selfIP)
	if len(livePeers) == 0 {
		livePeers = seedPeers
	}
	desired := hostfw.MergeDesired(selfIP, extra, livePeers, liveFirewallPorts(log))
	if err := hostfw.Reconcile(hostfw.UFWBackend{}, desired); err != nil {
		log.Warn("firewall reconcile", "err", err)
	}
	if err := iptables.ReplaceSetIPs(iptables.NodeSet, iptables.NodeIPs(selfIP, livePeers)); err != nil {
		log.Warn("node ipset", "err", err)
	}
}

func liveFirewallPeers(selfIP string) []string {
	instances, err := discoverd.NewService("flynn-host").Instances()
	if err != nil {
		return nil
	}
	addrs := make([]string, 0, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		addrs = append(addrs, inst.Addr)
	}
	return hostfw.NormalizePeers(addrs, selfIP)
}

func liveFirewallPorts(log log15.Logger) []int {
	client, err := cli.ClusterController()
	if err != nil {
		return nil
	}
	routes, err := client.RouteList()
	if err != nil {
		log.Debug("firewall route list", "err", err)
		return nil
	}
	return hostfw.TCPPortsFromRoutes(routes)
}
