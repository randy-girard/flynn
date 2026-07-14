package vxlan

import (
	"net"
	"reflect"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/flynn/flynn/flannel/pkg/ip"
)

// TestAddL2UsesReplaceSemantics guards against a regression where AddL2 installs
// FDB entries with exclusive-add (netlink.NeighAdd / NLM_F_EXCL) semantics.
//
// When a peer's flannel.1 device is recreated it gets a new random VTEP MAC.
// Peers receive a lease update and re-run AddL2 for that peer. With exclusive
// add, the update fails with EEXIST and the stale MAC is left in the FDB, so
// decapsulated VXLAN frames carry an inner destination MAC the peer no longer
// owns and the kernel silently drops them, blackholing the whole overlay.
// AddL2 must therefore use replace semantics (netlink.NeighSet / NLM_F_REPLACE),
// matching AddL3.
func TestAddL2UsesReplaceSemantics(t *testing.T) {
	// fdbSet must be bound to the replace variant, never the exclusive-add one.
	got := reflect.ValueOf(fdbSet).Pointer()
	if want := reflect.ValueOf(netlink.NeighSet).Pointer(); got != want {
		t.Errorf("fdbSet is not netlink.NeighSet; AddL2 must use replace semantics")
	}
	if bad := reflect.ValueOf(netlink.NeighAdd).Pointer(); got == bad {
		t.Errorf("fdbSet is netlink.NeighAdd (NLM_F_EXCL); AddL2 must use replace semantics")
	}
}

// TestAddL2BuildsCorrectNeigh verifies the FDB entry AddL2 constructs so that a
// stale-MAC replace actually targets the right link, address family and peer.
func TestAddL2BuildsCorrectNeigh(t *testing.T) {
	orig := fdbSet
	defer func() { fdbSet = orig }()

	var captured *netlink.Neigh
	fdbSet = func(n *netlink.Neigh) error {
		captured = n
		return nil
	}

	dev := &vxlanDevice{
		link: &netlink.Vxlan{LinkAttrs: netlink.LinkAttrs{Index: 42}},
	}

	pubIP, err := ip.ParseIP4("192.168.56.20")
	if err != nil {
		t.Fatalf("ParseIP4: %v", err)
	}
	mac, err := net.ParseMAC("3a:95:14:31:8c:a7")
	if err != nil {
		t.Fatalf("ParseMAC: %v", err)
	}

	if err := dev.AddL2(neigh{IP: pubIP, MAC: mac}); err != nil {
		t.Fatalf("AddL2: %v", err)
	}
	if captured == nil {
		t.Fatal("AddL2 did not call fdbSet")
	}

	if captured.LinkIndex != 42 {
		t.Errorf("LinkIndex = %d, want 42", captured.LinkIndex)
	}
	if captured.Family != syscall.AF_BRIDGE {
		t.Errorf("Family = %d, want AF_BRIDGE (%d)", captured.Family, syscall.AF_BRIDGE)
	}
	if captured.Flags != netlink.NTF_SELF {
		t.Errorf("Flags = %d, want NTF_SELF (%d)", captured.Flags, netlink.NTF_SELF)
	}
	if captured.State != netlink.NUD_PERMANENT {
		t.Errorf("State = %d, want NUD_PERMANENT (%d)", captured.State, netlink.NUD_PERMANENT)
	}
	if !captured.IP.Equal(pubIP.ToIP()) {
		t.Errorf("IP = %v, want %v", captured.IP, pubIP.ToIP())
	}
	if captured.HardwareAddr.String() != mac.String() {
		t.Errorf("HardwareAddr = %v, want %v", captured.HardwareAddr, mac)
	}
}
