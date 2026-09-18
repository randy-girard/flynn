package vxlan

import (
	"net"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/randy-girard/flynn/flannel/pkg/ip"
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
func TestNewHardwareAddrIsLocallyAdministeredUnicast(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 32; i++ {
		hw, err := newHardwareAddr()
		if err != nil {
			t.Fatal(err)
		}
		if len(hw) != 6 {
			t.Fatalf("len=%d", len(hw))
		}
		if hw[0]&0x01 != 0 {
			t.Fatalf("%s is multicast", hw)
		}
		if hw[0]&0x02 == 0 {
			t.Fatalf("%s is not locally administered", hw)
		}
		seen[hw.String()] = struct{}{}
	}
	if len(seen) < 30 {
		t.Fatalf("expected unique MACs, got %d distinct", len(seen))
	}
}

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

// TestNewVxlanLinkSetsExplicitMAC guards the fix for udev rewriting the VTEP
// MAC. If the MAC is left to the kernel (NET_ADDR_RANDOM), systemd-udevd's
// MACAddressPolicy=persistent (Ubuntu 24.04 default) replaces it right after
// creation, after flanneld has already published the old MAC in its lease.
// Peers then install FDB/neigh entries for a MAC nobody owns and cross-node
// overlay traffic is dropped (observed 2026-09-09 on the Vagrant cluster).
func TestNewVxlanLinkSetsExplicitMAC(t *testing.T) {
	link, err := newVxlanLink(&vxlanDeviceAttrs{vni: 1, name: "flannel.1", vtepIndex: 3, vtepPort: 8472})
	if err != nil {
		t.Fatalf("newVxlanLink: %v", err)
	}
	hw := link.LinkAttrs.HardwareAddr
	if len(hw) != 6 {
		t.Fatalf("HardwareAddr must be set explicitly (len=%d): %v", len(hw), hw)
	}
	if hw[0]&0x01 != 0 {
		t.Errorf("MAC %s is multicast; want unicast", hw)
	}
	if hw[0]&0x02 == 0 {
		t.Errorf("MAC %s is not locally administered", hw)
	}
	if link.Name != "flannel.1" || link.VxlanId != 1 || link.VtepDevIndex != 3 || link.Port != 8472 || link.Learning {
		t.Errorf("unexpected vxlan attrs: %+v", link)
	}

	// Two devices must not collide.
	other, err := newVxlanLink(&vxlanDeviceAttrs{vni: 1, name: "flannel.1"})
	if err != nil {
		t.Fatalf("newVxlanLink: %v", err)
	}
	if bytesEqualMAC(hw, other.LinkAttrs.HardwareAddr) {
		t.Errorf("two generated MACs are identical: %s", hw)
	}
}

// TestApplyHardwareAddrSetsChosenMAC guards the second half of the fix: the
// vendored netlink LinkAdd ignores LinkAttrs.HardwareAddr, so ensureLink must
// explicitly push the chosen MAC with LinkSetHardwareAddr after creation
// (that marks it NET_ADDR_SET so udev does not rewrite it).
func TestApplyHardwareAddrSetsChosenMAC(t *testing.T) {
	orig := linkSetHardwareAddr
	defer func() { linkSetHardwareAddr = orig }()

	var gotLink netlink.Link
	var gotMAC net.HardwareAddr
	linkSetHardwareAddr = func(l netlink.Link, hw net.HardwareAddr) error {
		gotLink, gotMAC = l, hw
		return nil
	}

	link, err := newVxlanLink(&vxlanDeviceAttrs{vni: 1, name: "flannel.1"})
	if err != nil {
		t.Fatalf("newVxlanLink: %v", err)
	}
	if err := applyHardwareAddr(link); err != nil {
		t.Fatalf("applyHardwareAddr: %v", err)
	}
	if gotLink != link {
		t.Errorf("LinkSetHardwareAddr called on %v, want the created link", gotLink)
	}
	if gotMAC.String() != link.HardwareAddr.String() {
		t.Errorf("LinkSetHardwareAddr MAC = %v, want %v", gotMAC, link.HardwareAddr)
	}

	// No chosen MAC (e.g. reused existing device path) must not call netlink.
	gotLink, gotMAC = nil, nil
	if err := applyHardwareAddr(&netlink.Vxlan{LinkAttrs: netlink.LinkAttrs{Name: "flannel.1"}}); err != nil {
		t.Fatalf("applyHardwareAddr(no MAC): %v", err)
	}
	if gotLink != nil || gotMAC != nil {
		t.Errorf("applyHardwareAddr without a MAC must be a no-op, called with %v %v", gotLink, gotMAC)
	}
}

// TestEnsureMACRestoresAdvertisedMAC verifies the runtime backstop: when the
// device MAC drifts from the lease-advertised MAC, EnsureMAC sets it back.
func TestEnsureMACRestoresAdvertisedMAC(t *testing.T) {
	origBy, origSet := linkByIndex, linkSetHardwareAddr
	defer func() { linkByIndex, linkSetHardwareAddr = origBy, origSet }()

	advertised, _ := net.ParseMAC("22:3b:40:88:1b:1a")
	rewritten, _ := net.ParseMAC("26:b2:5e:86:af:bf")

	current := rewritten
	linkByIndex = func(int) (netlink.Link, error) {
		return &netlink.Vxlan{LinkAttrs: netlink.LinkAttrs{Index: 7, Name: "flannel.1", HardwareAddr: current}}, nil
	}
	var setTo net.HardwareAddr
	linkSetHardwareAddr = func(_ netlink.Link, hw net.HardwareAddr) error {
		setTo = hw
		current = hw
		return nil
	}

	dev := &vxlanDevice{link: &netlink.Vxlan{LinkAttrs: netlink.LinkAttrs{Index: 7, Name: "flannel.1", HardwareAddr: advertised}}}

	repaired, err := dev.EnsureMAC(advertised)
	if err != nil {
		t.Fatalf("EnsureMAC: %v", err)
	}
	if !repaired {
		t.Fatal("expected EnsureMAC to report a repair")
	}
	if setTo.String() != advertised.String() {
		t.Errorf("restored MAC = %v, want %v", setTo, advertised)
	}

	// Once consistent, EnsureMAC must be a no-op.
	setTo = nil
	repaired, err = dev.EnsureMAC(advertised)
	if err != nil {
		t.Fatalf("EnsureMAC (steady): %v", err)
	}
	if repaired || setTo != nil {
		t.Errorf("EnsureMAC changed a matching MAC (repaired=%v setTo=%v)", repaired, setTo)
	}

	linkByIndex = func(int) (netlink.Link, error) {
		return nil, syscall.ENODEV
	}
	if _, err := dev.EnsureMAC(advertised); err == nil {
		t.Fatal("EnsureMAC must surface a missing-device error")
	}

	// Empty expectation is ignored.
	if repaired, err := dev.EnsureMAC(nil); err != nil || repaired {
		t.Errorf("EnsureMAC(nil) = (%v, %v), want (false, nil)", repaired, err)
	}
}

func TestMACCheckIntervalBoundsUdevRace(t *testing.T) {
	if macCheckInterval > 15*time.Second {
		t.Errorf("macCheckInterval = %s; udev can rewrite the VTEP MAC in well under this window", macCheckInterval)
	}
	if macCheckInterval < time.Second {
		t.Errorf("macCheckInterval = %s is too aggressive", macCheckInterval)
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
