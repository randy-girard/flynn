package main

import (
	"net"
	"testing"

	discoverd "github.com/flynn/flynn/discoverd/client"
	host "github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/netpolicy"
	"github.com/inconshreveable/log15"
)

func TestOverlayInstanceIP(t *testing.T) {
	if ip := overlayInstanceIP(nil); ip != nil {
		t.Fatalf("nil instance: %v", ip)
	}
	got := overlayInstanceIP(&discoverd.Instance{Addr: "100.64.82.14:1"})
	if got == nil || !got.Equal(net.ParseIP("100.64.82.14")) {
		t.Fatalf("host:port = %v", got)
	}
	got = overlayInstanceIP(&discoverd.Instance{Addr: "100.64.82.14"})
	if got == nil || !got.Equal(net.ParseIP("100.64.82.14")) {
		t.Fatalf("bare IP = %v", got)
	}
}

func TestTrackSkipsHostNetworkAndNil(t *testing.T) {
	p := newNetPolicy(log15.New())
	p.Track(nil, net.ParseIP("100.64.0.1"))
	p.Track(&host.Job{ID: "j1", Config: host.ContainerConfig{HostNetwork: true}}, net.ParseIP("100.64.0.1"))
	p.Track(&host.Job{ID: "j2"}, nil)
	if len(p.pending) != 0 {
		t.Fatalf("pending=%v", p.pending)
	}
	p.Untrack(nil, nil)
	p.Start(nil)
	if p.isLocalIP(nil) {
		t.Fatal("nil IP is not local")
	}
}

func TestLocalIPsSurviveStaleSnapshot(t *testing.T) {
	p := newNetPolicy(log15.New())
	job := &host.Job{
		ID: "node3-redis",
		Metadata: map[string]string{
			"flynn-system-app":          "true",
			"flynn-controller.app_name": "redis-621e38ec-728c-4eb5-8c1d-a0d38924cbd8",
			"flynn-controller.type":     "redis",
		},
	}
	ip := net.ParseIP("100.64.82.14")
	p.pending[job.ID] = pendingIP{job: job, ip: ip}

	if netpolicy.ClassifyJob(job) != netpolicy.ClassDatastore {
		t.Fatalf("redis appliance should be datastore, got %s", netpolicy.ClassifyJob(job))
	}
	if !p.isLocalIP(ip) {
		t.Fatal("pending redis IP must be treated as local (do not ipset del on a lagged EventDown)")
	}
	got := p.localIPs(netpolicy.ServiceData)
	if len(got) != 1 || !got[0].Equal(ip) {
		t.Fatalf("local data IPs = %v, want [%s]", got, ip)
	}
	if len(p.localIPs(netpolicy.ServiceUser)) != 0 {
		t.Fatal("redis must not be merged into the user set")
	}
}
