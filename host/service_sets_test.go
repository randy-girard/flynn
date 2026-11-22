package main

import (
	"errors"
	"net"
	"sort"
	"strings"
	"testing"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/iptables"
	"github.com/randy-girard/flynn/pkg/netpolicy"
)

// TestServiceSetsCoverBuildServicesAndLogaggregator: the build allow set is
// the controller, blobstore and tarreceive instances; the logaggregator set
// is the logaggregator instances (EDGE-04).
func TestServiceSetsCoverBuildServicesAndLogaggregator(t *testing.T) {
	got := map[string]string{}
	for _, s := range serviceSets() {
		svcs := append([]string(nil), s.services...)
		sort.Strings(svcs)
		got[s.set] = strings.Join(svcs, ",")
	}
	if got[iptables.BuildServiceSet] != "blobstore,controller,tarreceive" {
		t.Fatalf("build set services = %q", got[iptables.BuildServiceSet])
	}
	if got[iptables.LogaggregatorSet] != "logaggregator" {
		t.Fatalf("logaggregator set services = %q", got[iptables.LogaggregatorSet])
	}
	if strings.Join(netpolicy.BuildServices, ",") != "controller,blobstore,tarreceive" {
		t.Fatalf("BuildServices = %v", netpolicy.BuildServices)
	}
}

func TestSyncServiceSet(t *testing.T) {
	insts := map[string][]*discoverd.Instance{
		"controller": {{Addr: "100.64.1.5:80"}, {Addr: "100.64.2.5:80"}},
		"blobstore":  {{Addr: "100.64.1.9:80"}, {Addr: "100.64.1.5:80"}},
	}
	var errFor = map[string]error{
		"tarreceive": httphelper.JSONError{Code: httphelper.ObjectNotFoundErrorCode},
	}
	lookup := func(svc string) ([]*discoverd.Instance, error) {
		if err := errFor[svc]; err != nil {
			return nil, err
		}
		return insts[svc], nil
	}
	var replaced []string
	replace := func(set string, ips []net.IP) error {
		var s []string
		for _, ip := range ips {
			s = append(s, ip.String())
		}
		sort.Strings(s)
		replaced = append(replaced, set+"="+strings.Join(s, ","))
		return nil
	}
	set := serviceSet{set: iptables.BuildServiceSet, services: netpolicy.BuildServices}
	if err := syncServiceSet(set, lookup, replace); err != nil {
		t.Fatal(err)
	}
	want := iptables.BuildServiceSet + "=100.64.1.5,100.64.1.9,100.64.2.5"
	if len(replaced) != 1 || replaced[0] != want {
		t.Fatalf("replaced=%q want %q (a service that does not exist yet counts as empty)", replaced, want)
	}

	// Any other lookup error keeps the live set: replacing it with a partial
	// list would cut builds off from a healthy controller.
	errFor["blobstore"] = errors.New("discoverd: timeout")
	replaced = nil
	if err := syncServiceSet(set, lookup, replace); err == nil {
		t.Fatal("lookup error must be returned")
	}
	if len(replaced) != 0 {
		t.Fatalf("set replaced after a failed lookup: %q", replaced)
	}
}
