package discoverd

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
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

func TestReturningHostLeaseIsKeptAfterPrune(t *testing.T) {
	returning := rawLease(t, "192.168.57.20")
	peer := rawLease(t, "192.168.57.21")
	subnets := map[string]*json.RawMessage{
		"100.100.15.0-24": returning,
		"100.100.16.0-24": peer,
	}
	kept, removed := pruneExpiredSubnets(subnets, map[string]struct{}{"192.168.57.21": {}})
	if !reflect.DeepEqual(removed, []string{"100.100.15.0-24"}) {
		t.Fatalf("crash-rebooting host must be pruned while its flannel instance is down, removed=%v", removed)
	}
	if _, ok := kept["100.100.15.0-24"]; ok {
		t.Fatal("returning host lease must be gone until UpdateSubnet re-inserts it")
	}
	// LeaseRenewer UpdateSubnet writes the /24 back even after prune dropped it.
	kept["100.100.15.0-24"] = returning
	kept, removed = pruneExpiredSubnets(kept, map[string]struct{}{
		"192.168.57.20": {},
		"192.168.57.21": {},
	})
	if len(removed) != 0 {
		t.Fatalf("live returning host must not be pruned again, removed=%v", removed)
	}
	if _, ok := kept["100.100.15.0-24"]; !ok {
		t.Fatal("UpdateSubnet re-insert must survive the next prune")
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

func TestCreateAndUpdateSubnetDoNotPrunePeers(t *testing.T) {
	src, err := os.ReadFile("registry.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	create := funcBody(body, "func (r *registry) CreateSubnet")
	update := funcBody(body, "func (r *registry) UpdateSubnet")
	if create == "" || update == "" {
		t.Fatal("could not find CreateSubnet/UpdateSubnet")
	}
	if strings.Contains(create, "pruneOrphans") || strings.Contains(update, "pruneOrphans") {
		t.Fatal("Create/UpdateSubnet must not prune: a returning host would persist-drop a peer that is still registering")
	}
	if !strings.Contains(create, "getNetwork()") || !strings.Contains(update, "getNetwork()") {
		t.Fatal("Create/UpdateSubnet must load the full lease map")
	}
}

func funcBody(src, sig string) string {
	i := strings.Index(src, sig)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	brace := strings.Index(rest, "{")
	if brace < 0 {
		return ""
	}
	depth := 0
	for j, r := range rest[brace:] {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return rest[:brace+j+1]
			}
		}
	}
	return rest
}

func TestIncompleteFlannelView(t *testing.T) {
	subnets := map[string]*json.RawMessage{
		"100.100.15.0-24": rawLease(t, "192.168.57.20"),
		"100.100.31.0-24": rawLease(t, "192.168.57.21"),
		"100.100.70.0-24": rawLease(t, "192.168.57.22"),
	}
	if !incompleteFlannelView(subnets, map[string]struct{}{"192.168.57.20": {}, "192.168.57.22": {}}) {
		t.Fatal("two live instances and three lease IPs is an incomplete view; Create/Update must not prune")
	}
	if incompleteFlannelView(subnets, map[string]struct{}{
		"192.168.57.20": {},
		"192.168.57.21": {},
		"192.168.57.22": {},
	}) {
		t.Fatal("all lease IPs live is a complete view")
	}
	if incompleteFlannelView(subnets, map[string]struct{}{
		"192.168.57.20": {},
		"192.168.57.21": {},
		"192.168.57.22": {},
		"192.168.57.23": {},
	}) {
		t.Fatal("more instances than leases is not incomplete")
	}
}

func TestPruneLeasesForHostOnlyDropsThatHost(t *testing.T) {
	subnets := map[string]*json.RawMessage{
		"100.100.15.0-24": rawLease(t, "192.168.57.20"),
		"100.100.31.0-24": rawLease(t, "192.168.57.21"),
		"100.100.70.0-24": rawLease(t, "192.168.57.22"),
	}
	kept, removed := pruneLeasesForHost(subnets, "192.168.57.21")
	if !reflect.DeepEqual(removed, []string{"100.100.31.0-24"}) {
		t.Fatalf("removed=%v, want only the down host", removed)
	}
	if _, ok := kept["100.100.15.0-24"]; !ok {
		t.Fatal("peer 15 was dropped with the down host")
	}
	if _, ok := kept["100.100.70.0-24"]; !ok {
		t.Fatal("peer 70 was dropped with the down host")
	}
}

func TestDecodeNetworkAcceptsDiscoverdStringData(t *testing.T) {
	inner := `{"config":{"Network":"100.100.0.0/16","Backend":{"Type":"vxlan"}},"subnets":{"100.100.15.0-24":{"PublicIP":"192.168.57.20"}}}`
	wrapped, err := json.Marshal(inner)
	if err != nil {
		t.Fatal(err)
	}
	net, err := decodeNetwork(wrapped)
	if err != nil {
		t.Fatalf("discoverd stores ServiceMeta.Data as a JSON string; decodeNetwork must unwrap it: %v", err)
	}
	if _, ok := net.Subnets["100.100.15.0-24"]; !ok {
		t.Fatalf("subnets=%v, want the wrapped /24", net.Subnets)
	}
	obj, err := decodeNetwork([]byte(inner))
	if err != nil {
		t.Fatalf("object form: %v", err)
	}
	if _, ok := obj.Subnets["100.100.15.0-24"]; !ok {
		t.Fatal("object form dropped subnets")
	}
}

func TestDecodeNetworkFromDiscoverdServiceMeta(t *testing.T) {
	// Exact GET /services/flannel/meta body from the laptop cluster when
	// status.flannel was unhealthy: Data is a JSON string, not an object.
	body := []byte(`{"data":"{\"config\": {\"Network\": \"100.100.0.0/16\", \"Backend\": {\"Type\": \"vxlan\"}}, \"subnets\": {\"100.100.15.0-24\": {\"PublicIP\": \"192.168.57.20\"}}}","index":25104}`)
	var meta struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		t.Fatal(err)
	}
	net, err := decodeNetwork(meta.Data)
	if err != nil {
		t.Fatalf("GetMeta path Data=%s: %v", meta.Data, err)
	}
	if _, ok := net.Subnets["100.100.15.0-24"]; !ok {
		t.Fatalf("subnets=%v", net.Subnets)
	}

	inner := `{"config":{"Network":"100.100.0.0/16","Backend":{"Type":"vxlan"}},"subnets":{"100.100.15.0-24":{"PublicIP":"192.168.57.20"}}}`
	once, err := json.Marshal(inner)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := json.Marshal(string(once))
	if err != nil {
		t.Fatal(err)
	}
	dup, err := decodeNetwork(twice)
	if err != nil {
		t.Fatalf("double-wrapped ServiceMeta.Data must unwrap: %v", err)
	}
	if _, ok := dup.Subnets["100.100.15.0-24"]; !ok {
		t.Fatalf("double-wrapped subnets=%v", dup.Subnets)
	}
}
