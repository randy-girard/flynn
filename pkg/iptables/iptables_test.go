package iptables

import (
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestOverlayNetworkFor(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"100.64.57.1/24", "100.64.0.0/16"},
		{"100.64.36.1/24", "100.64.0.0/16"},
		{"10.1.2.3/24", "10.1.0.0/16"},
		{"100.64.0.0/16", "100.64.0.0/16"},
		{"192.168.1.0/24", "192.168.0.0/16"},
		{"10.0.0.1/32", "10.0.0.0/16"},
		{"10.0.0.0/8", "10.0.0.0/8"},
		{"100.64.0.0/10", "100.64.0.0/10"},
		{"100.100.0.0/16", "100.100.0.0/16"},
		{"2001:db8::1/64", "2001:db8::/64"},
		{"", ""},
		{"not-a-cidr", "not-a-cidr"},
	}
	for _, c := range cases {
		if got := OverlayNetworkFor(c.in); got != c.want {
			t.Errorf("OverlayNetworkFor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExclusionCIDRUsesRealOverlay(t *testing.T) {
	// A host /24 inside 100.64.0.0/10 must not be treated as 100.100.0.0/16.
	// OverlayNetworkFor keeps the /16 inference when no explicit overlay is set.
	if got := OverlayNetworkFor("100.100.9.1/24"); got != "100.100.0.0/16" {
		t.Fatalf("default inference = %q", got)
	}
	if got := ExclusionCIDR("100.100.9.1/24", ""); got != "100.100.0.0/16" {
		t.Fatalf("empty explicit = %q", got)
	}
	if NeedsExplicitExclusion("100.100.9.1/24", "") || NeedsExplicitExclusion("100.100.9.1/24", "100.100.0.0/16") {
		t.Fatal("default /16 must not take the explicit-exclusion path")
	}
	if got := ExclusionCIDR("100.100.9.1/24", "100.64.0.1/10"); got != "100.64.0.0/10" {
		t.Fatalf("explicit /10 = %q", got)
	}
	if !NeedsExplicitExclusion("100.100.9.1/24", "100.64.0.0/10") {
		t.Fatal("wider overlay must replace the inferred /16 exclusion")
	}
	got := OutboundMasqueradeArgsFor("flynnbr0", "100.100.9.1/24", "100.64.0.0/10")
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "! -d 100.64.0.0/10") {
		t.Fatalf("masquerade = %s", joined)
	}
	if strings.Contains(joined, "100.100.0.0/16") || strings.Contains(joined, "100.64.0.0/16") {
		t.Fatalf("masquerade still uses a /16: %s", joined)
	}
	if !strings.Contains(joined, "-s 100.100.9.0/24") {
		t.Fatalf("source must be the masked host subnet: %s", joined)
	}
}

func TestMasqueradePlanDropsStaleOverlayDest(t *testing.T) {
	save := strings.Join([]string{
		"-P POSTROUTING ACCEPT",
		"-A POSTROUTING -s 100.100.9.0/24 ! -o flynnbr0 ! -d 100.100.0.0/16 -j MASQUERADE",
		"-A POSTROUTING -s 100.100.9.0/24 ! -o flynnbr0 ! -d 100.64.0.0/12 -j MASQUERADE",
		"-A POSTROUTING -s 100.100.9.0/24 ! -o flynnbr0 -j MASQUERADE",
		"-A POSTROUTING -s 10.1.0.0/24 ! -o flynnbr0 ! -d 10.1.0.0/16 -j MASQUERADE",
	}, "\n")
	plan := PlanMasquerade(save, "flynnbr0", "100.100.9.1/24", "100.64.0.0/10")
	var deleted []string
	for _, rule := range plan.Delete {
		deleted = append(deleted, strings.Join(rule, " "))
	}
	body := strings.Join(deleted, "\n")
	for _, want := range []string{"100.100.0.0/16", "100.64.0.0/12", "-j MASQUERADE"} {
		if !strings.Contains(body, want) {
			t.Fatalf("delete plan missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "100.64.0.0/10") {
		t.Fatalf("delete plan must keep the desired exclusion:\n%s", body)
	}
	if strings.Contains(body, "10.1.0.0/24") {
		t.Fatalf("delete plan must leave other sources alone:\n%s", body)
	}
	inserted := strings.Join(plan.Insert, " ")
	if !strings.Contains(inserted, "! -d 100.64.0.0/10") || !strings.Contains(inserted, "-s 100.100.9.0/24") {
		t.Fatalf("insert = %s", inserted)
	}
	if !strings.Contains(inserted, "-t nat") {
		t.Fatalf("insert must name the nat table: %s", inserted)
	}

	again := strings.Join(append(strings.Split(save, "\n"), "-A POSTROUTING -s 100.100.9.0/24 ! -o flynnbr0 ! -d 100.64.0.0/10 -j MASQUERADE"), "\n")
	kept := PlanMasquerade(again, "flynnbr0", "100.100.9.1/24", "100.64.0.0/10")
	if len(kept.Insert) != 0 {
		t.Fatalf("desired rule already present, insert = %v", kept.Insert)
	}
}

func TestForwardDropPlanReplacesOverlayDest(t *testing.T) {
	save := strings.Join([]string{
		"-P FORWARD ACCEPT",
		"-A FORWARD -m set --match-set flynn-net-data dst -p tcp --dport 5432 -j ACCEPT",
		"-A FORWARD -m set --match-set flynn-net-user src -d 100.100.0.0/16 -m conntrack --ctstate NEW -j DROP",
		"-A FORWARD -i flynnbr0 ! -o flynnbr0 -j ACCEPT",
		"-A FORWARD -d 100.100.9.0/24 -o flynnbr0 -j ACCEPT",
	}, "\n")
	plan := PlanForwardDrop(save, "flynnbr0", "100.64.0.0/10")
	body := strings.Join(flatten(plan.Delete), "\n")
	if !strings.Contains(body, "100.100.0.0/16") || !strings.Contains(body, "DROP") {
		t.Fatalf("delete = %s", body)
	}
	if strings.Contains(body, "flynn-net-data") || strings.Contains(body, "5432") {
		t.Fatalf("delete must leave datastore accepts:\n%s", body)
	}
	inserted := strings.Join(plan.Insert, " ")
	if !strings.Contains(inserted, "-d 100.64.0.0/10") || !strings.Contains(inserted, "flynn-net-user") || !strings.Contains(inserted, "DROP") {
		t.Fatalf("insert = %s", inserted)
	}
	// 1-based line of the bridge egress ACCEPT. The new DROP goes there so
	// datastore ACCEPTs stay ahead of it.
	if plan.InsertBefore != 3 {
		t.Fatalf("insert before = %d, want 3 (the flynnbr0 egress ACCEPT)", plan.InsertBefore)
	}
}

func TestApplyOverlayRulesRemovesStaleExclusion(t *testing.T) {
	nat := strings.Join([]string{
		"-P POSTROUTING ACCEPT",
		"-A POSTROUTING -s 100.100.9.0/24 ! -o flynnbr0 ! -d 100.100.0.0/16 -j MASQUERADE",
	}, "\n")
	fwd := strings.Join([]string{
		"-P FORWARD ACCEPT",
		"-A FORWARD -m set --match-set flynn-net-data dst -p tcp --dport 5432 -j ACCEPT",
		"-A FORWARD -m set --match-set flynn-net-user src -d 100.100.0.0/16 -m conntrack --ctstate NEW -j DROP",
		"-A FORWARD -i flynnbr0 ! -o flynnbr0 -j ACCEPT",
	}, "\n")
	var calls []string
	run := func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		if strings.Contains(joined, "-S POSTROUTING") {
			return []byte(nat), nil
		}
		if strings.Contains(joined, "-S FORWARD") {
			return []byte(fwd), nil
		}
		return nil, nil
	}
	if err := ApplyOverlayRules(run, "flynnbr0", "100.100.9.1/24", "100.64.0.0/10"); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(calls, "\n")
	if !strings.Contains(body, "-D POSTROUTING") || !strings.Contains(body, "100.100.0.0/16") {
		t.Fatalf("did not delete inferred masquerade:\n%s", body)
	}
	if !strings.Contains(body, "-I POSTROUTING") || !strings.Contains(body, "100.64.0.0/10") {
		t.Fatalf("did not insert wider masquerade:\n%s", body)
	}
	dropInsert := strings.Index(body, "-I FORWARD 3")
	dropDelete := strings.Index(body, "-D FORWARD")
	if dropInsert < 0 || !strings.Contains(body, "-d 100.64.0.0/10") {
		t.Fatalf("did not insert the wider DROP before bridge egress:\n%s", body)
	}
	if dropDelete < 0 || dropDelete < dropInsert {
		t.Fatalf("DROP insert must happen before the stale DROP is deleted:\n%s", body)
	}
	if strings.Contains(body, "flynn-net-data") && strings.Contains(body, "-D") && strings.Contains(body, "5432") {
		t.Fatalf("must not delete datastore accepts:\n%s", body)
	}
}

func flatten(rules [][]string) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, strings.Join(rule, " "))
	}
	return out
}

func TestOutboundMasqueradeExcludesOverlayDest(t *testing.T) {
	got := OutboundMasqueradeArgs("flynnbr0", "100.64.57.1/24")
	want := []string{"POSTROUTING", "-t", "nat", "-s", "100.64.57.1/24", "!", "-o", "flynnbr0", "!", "-d", "100.64.0.0/16", "-j", "MASQUERADE"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OutboundMasqueradeArgs = %v\nwant %v", got, want)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "! -d 100.64.0.0/16") {
		t.Fatal("MASQUERADE must exclude overlay destinations; omitting ! -d SNAT's VXLAN to the local VTEP")
	}

	legacy := LegacyOutboundMasqueradeArgs("flynnbr0", "100.64.57.1/24")
	if strings.Contains(strings.Join(legacy, " "), "-d") {
		t.Fatalf("legacy MASQUERADE must not have a dest exclude (that is the bug): %v", legacy)
	}
}

func TestJobIsolationRuleOrder(t *testing.T) {
	overlay := "100.64.0.0/16"
	bridge := "100.64.57.1"
	drop := strings.Join(UserOverlayDropArgs(overlay), " ")
	if !strings.Contains(drop, "flynn-net-user src") || !strings.Contains(drop, "-d "+overlay) || !strings.Contains(drop, "DROP") {
		t.Fatalf("user overlay drop = %s", drop)
	}
	if !strings.Contains(drop, "--ctstate NEW") {
		t.Fatal("user overlay DROP must be NEW-only so ESTABLISHED replies to the router are not blackholed")
	}
	legacy := strings.Join(LegacyUserOverlayDropArgs(overlay), " ")
	if strings.Contains(legacy, "ctstate") {
		t.Fatalf("legacy overlay DROP must match all states (that is the bug): %s", legacy)
	}
	allowData := strings.Join(UserToDatastoreArgs("5432"), " ")
	if !strings.Contains(allowData, "flynn-net-data dst") || !strings.Contains(allowData, "ACCEPT") {
		t.Fatalf("user→data = %s", allowData)
	}
	if !strings.Contains(allowData, "-p tcp") || !strings.Contains(allowData, "--dport 5432") {
		t.Fatal("user→data ACCEPT must be TCP and port-restricted so admin HTTP is not reachable")
	}
	legacyData := strings.Join(LegacyUserToDatastoreArgs(), " ")
	if strings.Contains(legacyData, "dport") {
		t.Fatalf("legacy user→data must accept every port (that is the bug): %s", legacyData)
	}
	seenPorts := map[string]bool{}
	for _, rule := range UserToDatastoreRules() {
		joined := strings.Join(rule, " ")
		if !strings.Contains(joined, "--dport") || !strings.Contains(joined, "-p tcp") {
			t.Fatalf("datastore ACCEPT must be one proto/port: %s", joined)
		}
		dport := ""
		for i, a := range rule {
			if a == "--dport" && i+1 < len(rule) {
				dport = rule[i+1]
			}
		}
		if dport == "" {
			t.Fatalf("missing --dport: %v", rule)
		}
		seenPorts[dport] = true
	}
	for _, p := range []string{"5432", "3306", "27017", "6379", "9092", "9440"} {
		if !seenPorts[p] {
			t.Fatalf("missing data-plane port %s in UserToDatastoreRules", p)
		}
	}
	for _, p := range []string{"5433", "3307", "27018", "6380", "9090", "9093", "9095"} {
		if seenPorts[p] {
			t.Fatalf("admin/controller port %s must not be in UserToDatastoreRules", p)
		}
	}
	allowDNS := strings.Join(UserToBridgeDNSArgs(bridge, "udp"), " ")
	if !strings.Contains(allowDNS, "-d "+bridge) || !strings.Contains(allowDNS, "--dport 53") || !strings.Contains(allowDNS, "ACCEPT") {
		t.Fatalf("user→bridge DNS = %s", allowDNS)
	}
	legacyBridge := strings.Join(LegacyUserToBridgeArgs(bridge), " ")
	if strings.Contains(legacyBridge, "dport") {
		t.Fatalf("legacy user→bridge must accept every port (that is the bug): %s", legacyBridge)
	}
	buildDrop := strings.Join(BuildToUserDropArgs(), " ")
	if !strings.Contains(buildDrop, "flynn-net-build src") || !strings.Contains(buildDrop, "flynn-net-user dst") {
		t.Fatalf("build→user = %s", buildDrop)
	}

	src, err := os.ReadFile("iptables.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	dataAt := strings.Index(body, "UserToDatastoreRules()")
	dropAt := strings.Index(body, "UserOverlayDropArgs(overlay)")
	if dataAt < 0 || dropAt < 0 || dataAt < dropAt {
		// EnableJobIsolation inserts last-to-first; UserToDatastoreRules must
		// appear after UserOverlayDropArgs in the insert loop.
		loop := body
		if i := strings.Index(body, "iso := [][]string{"); i >= 0 {
			loop = body[i:]
		}
		if !strings.Contains(loop, "UserOverlayDropArgs") || !strings.Contains(loop, "UserToDatastoreRules") || !strings.Contains(loop, "UserToNodeDropArgs") {
			t.Fatal("EnableJobIsolation must insert datastore ACCEPT, DNS, and node DROP around overlay DROP")
		}
		dropPos := strings.Index(loop, "UserOverlayDropArgs")
		dataPos := strings.Index(loop, "UserToDatastoreRules")
		if dataPos < dropPos {
			t.Fatal("insert loop must list DROP first so ACCEPT is at the top of FORWARD")
		}
	}
}

func TestOverlayIncomingForwardAcceptsNewConnections(t *testing.T) {
	got := OverlayIncomingForwardArgs("100.64.57.1/24", "flynnbr0")
	want := []string{"FORWARD", "-d", "100.64.57.1/24", "-o", "flynnbr0", "-j", "ACCEPT"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OverlayIncomingForwardArgs = %v\nwant %v", got, want)
	}
	if strings.Contains(strings.Join(got, " "), "ESTABLISHED") {
		t.Fatal("incoming overlay FORWARD must accept NEW connections, not only ESTABLISHED")
	}
}

func TestMasqueradeNewDiffersFromLegacy(t *testing.T) {
	bridge, network := "flynnbr0", "100.64.50.1/24"
	got := OutboundMasqueradeArgs(bridge, network)
	legacy := LegacyOutboundMasqueradeArgs(bridge, network)
	if reflect.DeepEqual(got, legacy) {
		t.Fatal("new MASQUERADE must exclude overlay dest; matching the legacy rule SNAT's VXLAN")
	}
}

func TestEnableOutboundNATDeletesLegacyRule(t *testing.T) {
	src, err := os.ReadFile("iptables.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "LegacyOutboundMasqueradeArgs") {
		t.Fatal("EnableOutboundNAT must know the pre-fix MASQUERADE args")
	}
	if !strings.Contains(body, `Raw(append([]string{"-D"}, legacy...)...)`) {
		t.Fatal("EnableOutboundNAT must delete the legacy overlay-SNAT rule when installing the exclude")
	}
}

func TestEnableJobIsolationDeletesLegacyOverlayDrop(t *testing.T) {
	src, err := os.ReadFile("iptables.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "LegacyUserOverlayDropArgs") {
		t.Fatal("EnableJobIsolation must know the all-states overlay DROP args")
	}
	iso := body
	if i := strings.Index(body, "func EnableJobIsolation"); i >= 0 {
		iso = body[i:]
	}
	if !strings.Contains(iso, `Raw(append([]string{"-D"}, legacyDrop...)...)`) {
		t.Fatal("EnableJobIsolation must delete the legacy all-states DROP so upgrades install NEW-only")
	}
	if !strings.Contains(iso, "LegacyUserToBridgeArgs") || !strings.Contains(iso, "EnableHostIsolation") {
		t.Fatal("EnableJobIsolation must replace unrestricted gateway ACCEPT and install INPUT host isolation")
	}
	if !strings.Contains(iso, "LegacyUserToDatastoreArgs") || !strings.Contains(iso, `Raw(append([]string{"-D"}, legacyData...)...)`) {
		t.Fatal("EnableJobIsolation must delete the unrestricted user→data ACCEPT so upgrades install per-port rules")
	}
}

func TestDatastoreHostRulesDropSSHAndAPIOnly(t *testing.T) {
	for _, port := range []string{"22", "1113"} {
		in := strings.Join(DataToHostPortDropArgs(port), " ")
		if !strings.Contains(in, "INPUT") || !strings.Contains(in, "flynn-net-data src") || !strings.Contains(in, "--dport "+port) || !strings.Contains(in, "DROP") || !strings.Contains(in, "--ctstate NEW") {
			t.Fatalf("data INPUT drop %s = %q", port, in)
		}
		fwd := strings.Join(DataToNodePortDropArgs(port), " ")
		if !strings.Contains(fwd, "FORWARD") || !strings.Contains(fwd, "flynn-net-data src") || !strings.Contains(fwd, NodeSet) || !strings.Contains(fwd, "--dport "+port) || !strings.Contains(fwd, "DROP") {
			t.Fatalf("data node drop %s = %q", port, fwd)
		}
	}
	dns := strings.Join(DataToHostDNSArgs("udp"), " ")
	if !strings.Contains(dns, "INPUT") || !strings.Contains(dns, "flynn-net-data src") || !strings.Contains(dns, "--dport 53") || !strings.Contains(dns, "ACCEPT") {
		t.Fatalf("data DNS allow = %q", dns)
	}
	disc := strings.Join(DataToHostDiscoverdArgs(), " ")
	if !strings.Contains(disc, "INPUT") || !strings.Contains(disc, "flynn-net-data src") || !strings.Contains(disc, "--dport 1111") || !strings.Contains(disc, "ACCEPT") {
		t.Fatalf("data discoverd allow = %q", disc)
	}
	src, err := os.ReadFile("iptables.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	hostIso := body
	if i := strings.Index(body, "func EnableHostIsolation"); i >= 0 {
		hostIso = body[i:]
	}
	for _, needle := range []string{
		`DataToHostPortDropArgs("22")`,
		`DataToHostPortDropArgs("1113")`,
		`DataToHostDNSArgs("udp")`,
		`DataToHostDNSArgs("tcp")`,
		"DataToHostDiscoverdArgs()",
	} {
		if !strings.Contains(hostIso, needle) {
			t.Fatalf("EnableHostIsolation missing %s", needle)
		}
	}
	jobIso := body
	if i := strings.Index(body, "func EnableJobIsolation"); i >= 0 {
		if j := strings.Index(body[i+1:], "\nfunc "); j >= 0 {
			jobIso = body[i : i+1+j]
		} else {
			jobIso = body[i:]
		}
	}
	for _, needle := range []string{
		`DataToNodePortDropArgs("22")`,
		`DataToNodePortDropArgs("1113")`,
	} {
		if !strings.Contains(jobIso, needle) {
			t.Fatalf("EnableJobIsolation missing %s", needle)
		}
	}
}

func TestHostIsolationBlocksSSHAndPublicDiscoverd(t *testing.T) {
	bridge := "100.64.57.1"
	drop := strings.Join(UserToHostDropArgs(), " ")
	if !strings.Contains(drop, "INPUT") || !strings.Contains(drop, "flynn-net-user src") || !strings.Contains(drop, "DROP") {
		t.Fatalf("user→host DROP = %s", drop)
	}
	if !strings.Contains(drop, "--ctstate NEW") {
		t.Fatal("host INPUT DROP must be NEW-only so DNS replies and related traffic are not blackholed")
	}
	disc := strings.Join(UserToHostDiscoverdArgs(bridge), " ")
	if !strings.Contains(disc, "--dport 1111") {
		t.Fatalf("legacy discoverd allow args (deleted on upgrade) = %s", disc)
	}
	src, err := os.ReadFile("iptables.go")
	if err != nil {
		t.Fatal(err)
	}
	iso := string(src)
	if i := strings.Index(iso, "func EnableHostIsolation"); i >= 0 {
		iso = iso[i:]
	}
	if !strings.Contains(iso, `Raw(append([]string{"-D"}, args...)...)`) || !strings.Contains(iso, "UserToHostDiscoverdArgs") {
		t.Fatal("EnableHostIsolation must delete the gateway:1111 allow now that flynn-host registers services")
	}
	if strings.Contains(iso, "insertIfMissing(\"INPUT host isolation\", UserToHostDiscoverdArgs") {
		t.Fatal("must not re-install discoverd INPUT allow for user jobs")
	}
	dns := strings.Join(UserToHostDNSArgs("udp"), " ")
	if !strings.Contains(dns, "--dport 53") || !strings.Contains(dns, "ACCEPT") {
		t.Fatalf("host DNS allow = %s", dns)
	}
	node := strings.Join(UserToNodeDropArgs(), " ")
	if !strings.Contains(node, "FORWARD") || !strings.Contains(node, NodeSet) || !strings.Contains(node, "DROP") {
		t.Fatalf("user→node DROP = %s", node)
	}
	buildDrop := strings.Join(BuildToHostDropArgs(), " ")
	if !strings.Contains(buildDrop, "flynn-net-build src") {
		t.Fatalf("build jobs must also be kept off host SSH: %s", buildDrop)
	}
}

func TestNodeIPsIncludesSelfAndPeers(t *testing.T) {
	got := NodeIPs("50.116.33.9", []string{"10.0.0.2", "50.116.33.9", "not-an-ip"})
	if len(got) != 2 {
		t.Fatalf("NodeIPs=%v", got)
	}
	if got[0].String() != "50.116.33.9" || got[1].String() != "10.0.0.2" {
		t.Fatalf("NodeIPs order/contents=%v", got)
	}
}

func TestIsolationSetsAreDiscoverdServicesNotNodeSet(t *testing.T) {
	for _, s := range IsolationSets() {
		if s == NodeSet {
			t.Fatal("netpolicy must not watch flynn-net-nodes; that set is host underlay IPs, not overlay jobs")
		}
	}
}

func TestUnionIPsKeepsLocalWhenSnapshotIsStale(t *testing.T) {
	local := net.ParseIP("100.64.82.14")
	got := UnionIPs([]net.IP{net.ParseIP("100.64.61.17")}, []net.IP{local, net.ParseIP("100.64.61.17")})
	want := []string{"100.64.61.17", "100.64.82.14"}
	if len(got) != 2 {
		t.Fatalf("UnionIPs len=%d got=%v", len(got), got)
	}
	for i, s := range want {
		if got[i].String() != s {
			t.Fatalf("UnionIPs[%d]=%s want %s (stale snapshot must not drop the local redis IP)", i, got[i], s)
		}
	}
}
