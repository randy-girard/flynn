package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/randy-girard/flynn/flannel/pkg/ip"
	"github.com/randy-girard/flynn/flannel/subnet"
)

func TestPingLeaseURLsPrefersPublicIP(t *testing.T) {
	pub, err := ip.ParseIP4("192.168.57.21")
	if err != nil {
		t.Fatal(err)
	}
	netw, err := ip.ParseIP4("100.100.31.0")
	if err != nil {
		t.Fatal(err)
	}
	urls := pingLeaseURLs(subnet.SubnetLease{
		Network: ip.IP4Net{IP: netw, PrefixLen: 24},
		Attrs:   subnet.LeaseAttrs{PublicIP: pub, HTTPPort: "5002"},
	})
	if len(urls) < 2 {
		t.Fatalf("urls=%v, want public then overlay", urls)
	}
	if urls[0] != "http://192.168.57.21:5002/ping" {
		t.Fatalf("first URL %s, want underlay PublicIP (status binds EXTERNAL_IP)", urls[0])
	}
	if urls[1] != "http://100.100.31.1:5002/ping" {
		t.Fatalf("second URL %s, want overlay FirstUsable", urls[1])
	}
}

func TestPingLeasesHealthyOnPublicIPWhenOverlayDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ip.ParseIP4(host)
	if err != nil {
		t.Fatal(err)
	}
	selfNet, err := ip.ParseIP4("100.100.15.0")
	if err != nil {
		t.Fatal(err)
	}
	peerNet, err := ip.ParseIP4("100.100.31.0")
	if err != nil {
		t.Fatal(err)
	}
	peerPub, err := ip.ParseIP4("192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	self := subnet.SubnetLease{
		Network: ip.IP4Net{IP: selfNet, PrefixLen: 24},
		Attrs:   subnet.LeaseAttrs{PublicIP: pub, HTTPPort: port},
	}
	peer := subnet.SubnetLease{
		Network: ip.IP4Net{IP: peerNet, PrefixLen: 24},
		Attrs:   subnet.LeaseAttrs{PublicIP: peerPub, HTTPPort: "1"},
	}
	if err := pingLeases([]subnet.SubnetLease{self, peer}, self); err != nil {
		t.Fatalf("public /ping must make flannel healthy when overlay FirstUsable is unreachable: %v", err)
	}
}
