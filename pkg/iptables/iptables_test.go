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
	allowData := strings.Join(UserToDatastoreArgs(), " ")
	if !strings.Contains(allowData, "flynn-net-data dst") || !strings.Contains(allowData, "ACCEPT") {
		t.Fatalf("user→data = %s", allowData)
	}
	allowDNS := strings.Join(UserToBridgeArgs(bridge), " ")
	if !strings.Contains(allowDNS, "-d "+bridge) || !strings.Contains(allowDNS, "ACCEPT") {
		t.Fatalf("user→bridge = %s", allowDNS)
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
	dataAt := strings.Index(body, "UserToDatastoreArgs()")
	dropAt := strings.Index(body, "UserOverlayDropArgs(overlay)")
	if dataAt < 0 || dropAt < 0 || dataAt < dropAt {
		// EnableJobIsolation inserts last-to-first; UserToDatastoreArgs must
		// appear after UserOverlayDropArgs in the insert loop.
		loop := body
		if i := strings.Index(body, "for _, args := range [][]string{"); i >= 0 {
			loop = body[i:]
		}
		if !strings.Contains(loop, "UserOverlayDropArgs") || !strings.Contains(loop, "UserToDatastoreArgs") {
			t.Fatal("EnableJobIsolation must insert datastore ACCEPT after overlay DROP in the loop (so ACCEPT ends up first)")
		}
		dropPos := strings.Index(loop, "UserOverlayDropArgs")
		dataPos := strings.Index(loop, "UserToDatastoreArgs")
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
