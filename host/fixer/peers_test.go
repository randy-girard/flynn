package fixer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

func TestParsePeerIPList(t *testing.T) {
	got := parsePeerIPList(" 10.0.0.1,10.0.0.2, 10.0.0.1 ,")
	if strings.Join(got, ",") != "10.0.0.1,10.0.0.2" {
		t.Fatalf("got %v", got)
	}
	if parsePeerIPList("") != nil {
		t.Fatal("empty list")
	}
}

func TestLocalFixPeerIPsIncludesLoopback(t *testing.T) {
	ips := localFixPeerIPs()
	if len(ips) == 0 || ips[0] != "127.0.0.1" {
		t.Fatalf("loopback first, got %v", ips)
	}
}

func TestExtraPeerIPsFromStatus(t *testing.T) {
	got := extraPeerIPsFromStatus(&host.HostStatus{
		URL: "http://192.168.56.20:1113",
		Discoverd: &host.DiscoverdConfig{
			URL: "http://192.168.56.20:1111,192.168.56.21:1111",
		},
	})
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "192.168.56.20") || !strings.Contains(joined, "192.168.56.21") {
		t.Fatalf("got %v", got)
	}
	if extraPeerIPsFromStatus(nil) != nil {
		t.Fatal("nil status")
	}
}

func TestHostsWhenDiscoverdDownUsesLocalAPI(t *testing.T) {
	f := &ClusterFixer{
		LocalPeerIPs: func() []string { return []string{"127.0.0.1"} },
		ConnectPeerIP: func(ip string) (*cluster.Host, error) {
			return cluster.NewHost("node1", "http://"+ip+":1113", nil, nil), nil
		},
		HostStatus: func(*cluster.Host) (*host.HostStatus, error) {
			return &host.HostStatus{ID: "node1", URL: "http://10.0.0.5:1113"}, nil
		},
	}
	hosts, err := f.hostsWhenDiscoverdDown(1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].ID() != "node1" {
		t.Fatalf("%v", hosts)
	}
}

func TestHostsWhenDiscoverdDownUsesPeerIPs(t *testing.T) {
	var tried []string
	f := &ClusterFixer{
		LocalPeerIPs: func() []string { t.Fatal("should not enumerate local IPs"); return nil },
		ConnectPeerIP: func(ip string) (*cluster.Host, error) {
			tried = append(tried, ip)
			return cluster.NewHost("h-"+ip, "http://"+ip+":1113", nil, nil), nil
		},
		HostStatus: func(*cluster.Host) (*host.HostStatus, error) {
			return &host.HostStatus{}, nil
		},
	}
	hosts, err := f.hostsWhenDiscoverdDown(1, "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].ID() != "h-192.0.2.10" {
		t.Fatalf("%v", hosts)
	}
	if strings.Join(tried, ",") != "192.0.2.10" {
		t.Fatalf("tried %v", tried)
	}
}

func TestHostsWhenDiscoverdDownExpandsDiscoverdPeers(t *testing.T) {
	f := &ClusterFixer{
		ConnectPeerIP: func(ip string) (*cluster.Host, error) {
			return cluster.NewHost("h-"+ip, "http://"+ip+":1113", nil, nil), nil
		},
		HostStatus: func(h *cluster.Host) (*host.HostStatus, error) {
			if h.ID() != "h-10.0.0.1" {
				return &host.HostStatus{}, nil
			}
			return &host.HostStatus{
				Discoverd: &host.DiscoverdConfig{URL: "http://10.0.0.1:1111,http://10.0.0.2:1111"},
			}, nil
		},
	}
	hosts, err := f.hostsWhenDiscoverdDown(2, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, h := range hosts {
		ids[h.ID()] = true
	}
	if !ids["h-10.0.0.1"] || !ids["h-10.0.0.2"] {
		t.Fatalf("%v", ids)
	}
}

func TestHostsWhenDiscoverdDownRequiresMinHosts(t *testing.T) {
	f := &ClusterFixer{
		LocalPeerIPs: func() []string { return []string{"127.0.0.1"} },
		ConnectPeerIP: func(ip string) (*cluster.Host, error) {
			return cluster.NewHost("only", "http://127.0.0.1:1113", nil, nil), nil
		},
		HostStatus: func(*cluster.Host) (*host.HostStatus, error) {
			return &host.HostStatus{ID: "only"}, nil
		},
	}
	_, err := f.hostsWhenDiscoverdDown(3, "")
	if err == nil || !strings.Contains(err.Error(), "--min-hosts") {
		t.Fatalf("got %v", err)
	}
}

func TestHostsWhenDiscoverdDownLocalFailure(t *testing.T) {
	f := &ClusterFixer{
		LocalPeerIPs: func() []string { return []string{"127.0.0.1"} },
		ConnectPeerIP: func(ip string) (*cluster.Host, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}
	_, err := f.hostsWhenDiscoverdDown(1, "")
	if err == nil || !strings.Contains(err.Error(), "--peer-ips") {
		t.Fatalf("got %v", err)
	}
}
