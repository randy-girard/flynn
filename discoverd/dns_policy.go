package main

import (
	"context"
	"net"
	"time"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/discoverd/server"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/iptables"
	"github.com/randy-girard/flynn/pkg/netpolicy"
)

// hostLookupTimeout bounds one DNS query's wait on the local flynn-host. A
// lookup that times out fails closed.
const hostLookupTimeout = time.Second

// applyDNSClientPolicy gives the DNS server what it needs to tell user jobs
// from system jobs when a client's overlay IP is in no netpolicy service
// (CORE-25-F1): the overlay network, this host's subnet of it, the bridge
// address host-network jobs query from, and this host's API. Without a host
// network nothing is set, and every client stays a system client.
func applyDNSClientPolicy(s *server.DNSServer, netcfg *host.NetworkConfig, hostIP string) {
	if netcfg == nil || netcfg.Subnet == "" {
		return
	}
	bridge, local, err := net.ParseCIDR(netcfg.Subnet)
	if err != nil {
		return
	}
	_, overlay, err := net.ParseCIDR(iptables.ExclusionCIDR(netcfg.Subnet, netcfg.Overlay))
	if err != nil {
		return
	}
	s.Overlay = overlay
	s.LocalSubnet = local
	s.SelfIPs = []net.IP{bridge}
	s.LocalJobs = newHostLocalJobs(hostIP+":1113", hostLookupTimeout)
}

// hostLocalJobs answers server.LocalJobs from GET /host/netpolicy/ips/:ip on
// the local flynn-host, with the host auth key from FLYNN_HOST_AUTH_KEY.
type hostLocalJobs struct {
	host    *cluster.Host
	timeout time.Duration
}

func newHostLocalJobs(addr string, timeout time.Duration) *hostLocalJobs {
	return &hostLocalJobs{host: cluster.NewHost("", addr, nil, nil), timeout: timeout}
}

func (j *hostLocalJobs) OverlayInstance(ip net.IP) (*discoverd.Instance, error) {
	ctx, cancel := context.WithTimeout(context.Background(), j.timeout)
	defer cancel()
	res, err := j.host.GetOverlayIP(ctx, ip.String())
	if err != nil || res == nil {
		return nil, err
	}
	return &discoverd.Instance{
		Addr:  net.JoinHostPort(res.IP, netpolicy.DummyPort),
		Proto: "tcp",
		Meta:  res.Meta,
	}, nil
}
