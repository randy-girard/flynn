package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/randy-girard/flynn/discoverd/server"
	host "github.com/randy-girard/flynn/host/types"
)

// CORE-25-F1: the DNS server learns the overlay network and this host's
// subnet from the host status, and asks this host's API about unlisted local
// overlay clients.
func TestDNSClientPolicyFromHostNetwork(t *testing.T) {
	for _, tc := range []struct {
		net                  *host.NetworkConfig
		overlay, local, self string
	}{
		{&host.NetworkConfig{Subnet: "100.100.34.1/24"}, "100.100.0.0/16", "100.100.34.0/24", "100.100.34.1"},
		{&host.NetworkConfig{Subnet: "10.5.3.1/24", Overlay: "10.0.0.0/8"}, "10.0.0.0/8", "10.5.3.0/24", "10.5.3.1"},
		{&host.NetworkConfig{Subnet: ""}, "", "", ""},
		{nil, "", "", ""},
	} {
		srv := &server.DNSServer{}
		applyDNSClientPolicy(srv, tc.net, "192.0.2.1")
		if got := cidrString(srv.Overlay); got != tc.overlay {
			t.Errorf("%+v: overlay %q, want %q", tc.net, got, tc.overlay)
		}
		if got := cidrString(srv.LocalSubnet); got != tc.local {
			t.Errorf("%+v: local %q, want %q", tc.net, got, tc.local)
		}
		var self string
		if len(srv.SelfIPs) == 1 {
			self = srv.SelfIPs[0].String()
		} else if len(srv.SelfIPs) > 1 {
			self = "many"
		}
		if self != tc.self {
			t.Errorf("%+v: self %q, want %q", tc.net, self, tc.self)
		}
		if (srv.LocalJobs != nil) != (tc.local != "") {
			t.Errorf("%+v: jobs = %v", tc.net, srv.LocalJobs)
		}
	}
}

func cidrString(n *net.IPNet) string {
	if n == nil {
		return ""
	}
	return n.String()
}

func TestHostLocalJobsOverlayInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/host/netpolicy/ips/100.100.34.5":
			_ = json.NewEncoder(w).Encode(host.OverlayIP{IP: "100.100.34.5", JobID: "j1",
				Meta: map[string]string{"class": "user", "flynn-discoverd-allow": "redis-a"}})
		case "/host/netpolicy/ips/100.100.34.8":
			time.Sleep(500 * time.Millisecond)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":"object_not_found","message":"no job"}`))
		}
	}))
	defer srv.Close()
	jobs := newHostLocalJobs(strings.TrimPrefix(srv.URL, "http://"), 100*time.Millisecond)

	inst, err := jobs.OverlayInstance(net.ParseIP("100.100.34.5"))
	if err != nil || inst == nil || inst.Meta["class"] != "user" || inst.Meta["flynn-discoverd-allow"] != "redis-a" {
		t.Fatalf("found: %+v %v", inst, err)
	}
	if inst, err := jobs.OverlayInstance(net.ParseIP("100.100.34.6")); err != nil || inst != nil {
		t.Fatalf("unknown: %+v %v", inst, err)
	}
	start := time.Now()
	if _, err := jobs.OverlayInstance(net.ParseIP("100.100.34.8")); err == nil {
		t.Fatal("a slow host must time out with an error")
	}
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Fatalf("lookup took %s, want the 100ms timeout", d)
	}
}

// CORE-41: a proxy-mode DNS server keeps one ProxyStore (and so one set of
// netpolicy streams) while it stays a proxy, and closes it on promotion.
func TestSetDNSStoreReusesAndClosesProxy(t *testing.T) {
	m := &Main{peers: []string{"127.0.0.1:1"}, store: server.NewStore(t.TempDir())}
	s := &server.DNSServer{}
	m.setDNSStore(s, true)
	first, ok := s.GetStore().(*server.ProxyStore)
	if !ok || first != m.dnsProxy {
		t.Fatalf("proxy store = %T, want the Main's ProxyStore", s.GetStore())
	}
	m.setDNSStore(s, true)
	if s.GetStore() != server.DNSStore(first) {
		t.Fatal("a second demotion made a new ProxyStore")
	}
	m.setDNSStore(s, false)
	if s.GetStore() != server.DNSStore(m.store) || m.dnsProxy != nil {
		t.Fatalf("promotion: store %T, proxy %v", s.GetStore(), m.dnsProxy)
	}
}
