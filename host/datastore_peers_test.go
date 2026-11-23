package main

import (
	"errors"
	"net"
	"sort"
	"strings"
	"testing"

	"github.com/inconshreveable/log15"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/iptables"
	"github.com/randy-girard/flynn/pkg/netpolicy"
)

func dataJob(id, app string) *host.Job {
	return &host.Job{ID: id, Metadata: map[string]string{
		"flynn-system-app":           "true",
		"flynn-datastore":            "true",
		"flynn-controller.app":       app,
		"flynn-controller.app_name":  "kafka-" + app,
		"flynn-controller.type":      "kafka",
		"flynn-controller.formation": "true",
	}}
}

// TestSyncDatastorePeersPairsSameAppEngines (EDGE-29): the peer set pairs
// engines of the same app, from discoverd registrations and from this
// host's own datastore jobs (which may not be registered yet). Other
// classes' local jobs are ignored.
func TestSyncDatastorePeersPairsSameAppEngines(t *testing.T) {
	p := newNetPolicy(log15.New())
	p.pending["local-kafka"] = pendingIP{job: dataJob("local-kafka", "app-a"), ip: net.ParseIP("100.64.1.3")}
	p.pending["local-web"] = pendingIP{job: &host.Job{ID: "local-web", Metadata: map[string]string{"flynn-controller.app_name": "shop", "flynn-controller.type": "web"}}, ip: net.ParseIP("100.64.1.4")}
	lookup := func() ([]*discoverd.Instance, error) {
		return []*discoverd.Instance{
			{Addr: "100.64.2.3:1", Meta: map[string]string{"class": "datastore", netpolicy.MetaDatastoreApp: "app-a"}},
			{Addr: "100.64.3.3:1", Meta: map[string]string{"class": "datastore", netpolicy.MetaDatastoreApp: "app-b"}},
			{Addr: "100.64.4.3:1", Meta: map[string]string{"class": "datastore", netpolicy.MetaDatastoreApp: "pg", netpolicy.MetaDatastoreSystem: "true"}},
		}, nil
	}
	var set string
	var got []string
	var sysIPs []string
	p.replaceSystemIPs = func(s string, ips []net.IP) error {
		if s != iptables.DataSystemSet {
			t.Fatalf("system set = %q", s)
		}
		for _, ip := range ips {
			sysIPs = append(sysIPs, ip.String())
		}
		return nil
	}
	replace := func(s string, pairs [][2]net.IP) error {
		set = s
		for _, pr := range pairs {
			got = append(got, pr[0].String()+">"+pr[1].String())
		}
		sort.Strings(got)
		return nil
	}
	if err := p.syncDatastorePeers(lookup, replace); err != nil {
		t.Fatal(err)
	}
	if set != iptables.DataPeerSet {
		t.Fatalf("replaced %q, want %s", set, iptables.DataPeerSet)
	}
	want := "100.64.1.3>100.64.2.3 100.64.2.3>100.64.1.3"
	if strings.Join(got, " ") != want {
		t.Fatalf("pairs = %v, want %s", got, want)
	}
	if strings.Join(sysIPs, " ") != "100.64.4.3" {
		t.Fatalf("system datastore IPs = %v", sysIPs)
	}
}

// TestSyncDatastorePeersKeepsLiveSetOnLookupError: a failed discoverd
// lookup must not replace the set with only the local engines, which would
// cut same-app peers on other hosts off from each other.
func TestSyncDatastorePeersKeepsLiveSetOnLookupError(t *testing.T) {
	p := newNetPolicy(log15.New())
	p.pending["local-kafka"] = pendingIP{job: dataJob("local-kafka", "app-a"), ip: net.ParseIP("100.64.1.3")}
	called := false
	replace := func(string, [][2]net.IP) error { called = true; return nil }
	p.replaceSystemIPs = func(string, []net.IP) error { called = true; return nil }
	if err := p.syncDatastorePeers(func() ([]*discoverd.Instance, error) { return nil, errors.New("leader election") }, replace); err == nil {
		t.Fatal("want the lookup error")
	}
	if called {
		t.Fatal("live set replaced after a failed lookup")
	}
	// No flynn-net-data service yet (fresh cluster): local engines only.
	notFound := func() ([]*discoverd.Instance, error) {
		return nil, httphelper.JSONError{Code: httphelper.ObjectNotFoundErrorCode}
	}
	if err := p.syncDatastorePeers(notFound, replace); err != nil || !called {
		t.Fatalf("not-found must replace from local jobs: err=%v called=%v", err, called)
	}
}

// TestTrackNudgesDatastorePeerSync: a new local engine is paired at once,
// not on the next 15-second resync.
func TestTrackNudgesDatastorePeerSync(t *testing.T) {
	p := newNetPolicy(log15.New())
	p.addSetIP = func(string, net.IP) error { return nil }
	if err := p.Track(dataJob("local-kafka", "app-a"), net.ParseIP("100.64.1.3")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.dataPeersChanged:
	default:
		t.Fatal("Track of a datastore engine must nudge the peer set sync")
	}
}
