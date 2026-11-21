package main

import (
	"errors"
	"net"
	"testing"

	"github.com/inconshreveable/log15"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/netpolicy"
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

type fakeHeartbeater struct{ closed bool }

func (f *fakeHeartbeater) SetMeta(map[string]string) error { return nil }
func (f *fakeHeartbeater) Close() error                    { f.closed = true; return nil }
func (f *fakeHeartbeater) Addr() string                    { return "" }
func (f *fakeHeartbeater) SetClient(*discoverd.Client)     {}

func userJob(id string) *host.Job {
	return &host.Job{ID: id, Metadata: map[string]string{"flynn-controller.app": "a1", "flynn-controller.type": "web"}}
}

func TestTrackFailsJobWhenIpsetAddFails(t *testing.T) {
	for _, tc := range []struct {
		name    string
		job     *host.Job
		wantErr bool
	}{
		{"user", userJob("u1"), true},
		{"build", &host.Job{ID: "b1", Metadata: map[string]string{"flynn-controller.type": "slugbuilder"}}, true},
		{"system", &host.Job{ID: "s1", Partition: "system", Metadata: map[string]string{"flynn-system-app": "true"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newNetPolicy(log15.New())
			p.addSetIP = func(string, net.IP) error { return errors.New("ipset: kernel error") }
			err := p.Track(tc.job, net.ParseIP("100.64.0.7"))
			if tc.wantErr && err == nil {
				t.Fatal("Track must fail a user/build job whose IP is not in its isolation set")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("system job: %v", err)
			}
		})
	}
}

func TestRegisterRetriesUntilRegistered(t *testing.T) {
	p := newNetPolicy(log15.New())
	p.addSetIP = func(string, net.IP) error { return nil }
	calls := 0
	hb := &fakeHeartbeater{}
	p.registerInstance = func(svc string, inst *discoverd.Instance) (discoverd.Heartbeater, error) {
		calls++
		if svc != netpolicy.ServiceUser {
			t.Fatalf("service = %s", svc)
		}
		if calls == 1 {
			return nil, errors.New("discoverd: no leader")
		}
		return hb, nil
	}
	if err := p.Track(userJob("u1"), net.ParseIP("100.64.0.7")); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.local["u1"]; ok {
		t.Fatal("first registration failed; job must not be marked registered")
	}
	p.registerPending(netpolicy.ServiceUser)
	if calls != 2 {
		t.Fatalf("registerPending must retry the failed registration, calls=%d", calls)
	}
	if p.local["u1"] != hb {
		t.Fatal("job not registered after retry")
	}
	p.registerPending(netpolicy.ServiceUser)
	p.registerPending(netpolicy.ServiceData)
	if calls != 2 {
		t.Fatalf("registered jobs and other services must not re-register, calls=%d", calls)
	}
	p.Untrack(userJob("u1"), nil)
	if !hb.closed {
		t.Fatal("Untrack must close the heartbeater")
	}
}
