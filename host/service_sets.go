package main

import (
	"net"
	"time"

	"github.com/inconshreveable/log15"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/iptables"
	"github.com/randy-girard/flynn/pkg/netpolicy"
)

// serviceSet is an ipset that holds the overlay IPs of some discoverd
// services, for rules that allow or drop traffic to those services only.
type serviceSet struct {
	set      string
	services []string
}

// serviceSets are the per-service ipsets flynn-host keeps in sync (EDGE-04):
// the services build jobs may reach, and the logaggregator instances.
func serviceSets() []serviceSet {
	return []serviceSet{
		{set: iptables.BuildServiceSet, services: netpolicy.BuildServices},
		{set: iptables.LogaggregatorSet, services: []string{"logaggregator"}},
	}
}

// syncServiceSet replaces s.set with the IPs of every instance of
// s.services. A service that does not exist yet (tarreceive on a fresh
// cluster) contributes nothing. Any other lookup error leaves the live set
// alone: a partial list would cut builds off from a healthy service.
func syncServiceSet(s serviceSet, lookup func(string) ([]*discoverd.Instance, error), replace func(string, []net.IP) error) error {
	var ips []net.IP
	for _, svc := range s.services {
		insts, err := lookup(svc)
		if err != nil && !discoverd.IsNotFound(err) {
			return err
		}
		for _, inst := range insts {
			if ip := overlayInstanceIP(inst); ip != nil {
				ips = append(ips, ip)
			}
		}
	}
	return replace(s.set, iptables.UnionIPs(nil, ips))
}

// watchServiceSets keeps every serviceSet current: a full resync on any
// instance change of one of its services, and every 15 s as a backstop.
func watchServiceSets(client *discoverd.Client, log log15.Logger) {
	lookup := func(svc string) ([]*discoverd.Instance, error) {
		return client.Service(svc).Instances()
	}
	for _, s := range serviceSets() {
		changed := make(chan struct{}, 1)
		resync := func(s serviceSet) {
			if err := syncServiceSet(s, lookup, iptables.ReplaceSetIPs); err != nil {
				log.Error("sync service ipset", "set", s.set, "err", err)
			}
		}
		go func(s serviceSet) {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			resync(s)
			for {
				select {
				case <-changed:
				case <-ticker.C:
				}
				resync(s)
			}
		}(s)
		for _, svc := range s.services {
			go watchServiceChanges(client, svc, changed, log)
		}
	}
}

func watchServiceChanges(client *discoverd.Client, svc string, changed chan<- struct{}, log log15.Logger) {
	for {
		events := make(chan *discoverd.Event)
		stream, err := client.Service(svc).Watch(events)
		if err != nil {
			log.Error("watch service for ipset", "service", svc, "err", err)
			time.Sleep(time.Second)
			continue
		}
		for range events {
			select {
			case changed <- struct{}{}:
			default:
			}
		}
		stream.Close()
		time.Sleep(time.Second)
	}
}
