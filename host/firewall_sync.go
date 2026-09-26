package main

import (
	"time"

	"github.com/inconshreveable/log15"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/host/cli"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/hostfw"
	"github.com/randy-girard/flynn/pkg/instanceport"
	"github.com/randy-girard/flynn/pkg/iptables"
)

func startHostFirewall(hostID, selfIP string, seedPeers []string, log log15.Logger, jobs func() []instanceport.Job) {
	if log == nil {
		log = log15.New("component", "hostfw")
	}
	if err := hostfw.ApplySeedPeers(seedPeers); err != nil {
		log.Warn("firewall seed peers", "err", err)
	}
	go func() {
		for {
			syncHostFirewall(hostID, selfIP, seedPeers, log, jobs)
			time.Sleep(15 * time.Second)
		}
	}()
}

func syncHostFirewall(hostID, selfIP string, seedPeers []string, log log15.Logger, jobs func() []instanceport.Job) {
	extra := hostfw.LoadExtra(hostfw.StatePath())
	livePeers := liveFirewallPeers(selfIP)
	if len(livePeers) == 0 {
		livePeers = seedPeers
	}
	desired := hostfw.MergeDesired(selfIP, extra, livePeers, liveFirewallPorts(log))
	if err := hostfw.Reconcile(hostfw.UFWBackend{}, desired); err != nil {
		log.Warn("firewall reconcile", "err", err)
	}
	if jobs != nil {
		ports := hostfw.InstancePorts(hostID, jobs())
		if err := hostfw.ReconcileInstancePorts(hostfw.UFWBackend{}, ports); err != nil {
			log.Warn("firewall instance ports", "err", err)
		}
	}
	if err := iptables.ReplaceSetIPs(iptables.NodeSet, iptables.NodeIPs(selfIP, livePeers)); err != nil {
		log.Warn("node ipset", "err", err)
	}
}

// instanceJobsFromActive reads instance id and port from jobs running on this host.
func instanceJobsFromActive(hostID string, active map[string]*host.ActiveJob) []instanceport.Job {
	if len(active) == 0 {
		return nil
	}
	var jobs []instanceport.Job
	for _, aj := range active {
		if aj == nil || aj.Job == nil {
			continue
		}
		if aj.Status != host.StatusRunning && aj.Status != host.StatusStarting {
			continue
		}
		j, ok := instanceport.ParseJob(hostID, aj.Job.Metadata, aj.Job.Config.Env)
		if !ok {
			continue
		}
		jobs = append(jobs, j)
	}
	return jobs
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
