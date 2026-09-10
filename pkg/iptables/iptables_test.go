package iptables

import (
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
