package discoverd

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/randy-girard/flynn/discoverd/client"
)

func rawLease(t *testing.T, publicIP string) *json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"PublicIP": publicIP})
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(b)
	return &raw
}

func TestLeasePublicIP(t *testing.T) {
	ip, ok := leasePublicIP([]byte(`{"PublicIP":"10.0.0.1","HTTPPort":"5001"}`))
	if !ok || ip != "10.0.0.1" {
		t.Fatalf("got %q ok=%v", ip, ok)
	}
	if _, ok := leasePublicIP([]byte(`{`)); ok {
		t.Fatal("expected unparseable JSON to fail")
	}
	if _, ok := leasePublicIP([]byte(`{"HTTPPort":"5001"}`)); ok {
		t.Fatal("expected missing PublicIP to fail")
	}
}

func TestLiveInstanceHosts(t *testing.T) {
	hosts := liveInstanceHosts([]*discoverd.Instance{
		{Addr: "10.0.0.1:5001"},
		nil,
		{Addr: "10.0.0.2:5001"},
		{Addr: "10.0.0.1:5002"},
	})
	expected := map[string]struct{}{
		"10.0.0.1": {},
		"10.0.0.2": {},
	}
	if !reflect.DeepEqual(hosts, expected) {
		t.Fatalf("got %#v want %#v", hosts, expected)
	}
}

func TestPruneExpiredSubnets(t *testing.T) {
	live := rawLease(t, "1.2.3.4")
	dead := rawLease(t, "5.6.7.8")
	unparseable := json.RawMessage(`{"PublicIP":`)
	subnets := map[string]*json.RawMessage{
		"10.3.1.0-24": live,
		"10.3.2.0-24": dead,
		"10.3.3.0-24": &unparseable,
		"10.3.4.0-24": nil,
	}
	liveHosts := map[string]struct{}{"1.2.3.4": {}}

	kept, removed := pruneExpiredSubnets(subnets, liveHosts)
	if !reflect.DeepEqual(removed, []string{"10.3.2.0-24"}) {
		t.Fatalf("removed = %v, want the decommissioned host only", removed)
	}
	if _, ok := kept["10.3.1.0-24"]; !ok {
		t.Fatal("live host lease was reclaimed")
	}
	if _, ok := kept["10.3.2.0-24"]; ok {
		t.Fatal("decommissioned host lease was kept")
	}
	if _, ok := kept["10.3.3.0-24"]; !ok {
		t.Fatal("unparseable lease was reclaimed")
	}
	if _, ok := kept["10.3.4.0-24"]; !ok {
		t.Fatal("nil lease was reclaimed")
	}
}

func TestPruneExpiredSubnetsEmpty(t *testing.T) {
	kept, removed := pruneExpiredSubnets(nil, map[string]struct{}{"1.2.3.4": {}})
	if kept != nil || removed != nil {
		t.Fatalf("got kept=%v removed=%v", kept, removed)
	}
}

func TestPruneExpiredSubnetsAllDead(t *testing.T) {
	subnets := map[string]*json.RawMessage{
		"10.3.1.0-24": rawLease(t, "1.2.3.4"),
	}
	kept, removed := pruneExpiredSubnets(subnets, map[string]struct{}{})
	if !reflect.DeepEqual(removed, []string{"10.3.1.0-24"}) {
		t.Fatalf("removed = %v", removed)
	}
	if len(kept) != 0 {
		t.Fatalf("kept %d leases of dead hosts", len(kept))
	}
}
