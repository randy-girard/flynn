package vxlan

import (
	"crypto/rand"
	"fmt"
	"net"
	"syscall"

	log "github.com/golang/glog"
	"github.com/vishvananda/netlink"

	"github.com/randy-girard/flynn/flannel/pkg/ip"
)

type vxlanDeviceAttrs struct {
	vni       uint32
	name      string
	vtepIndex int
	vtepAddr  net.IP
	vtepPort  int
}

type vxlanDevice struct {
	link *netlink.Vxlan
}

// Indirections for tests (no root / netlink needed).
var (
	linkByIndex         = netlink.LinkByIndex
	linkSetHardwareAddr = netlink.LinkSetHardwareAddr
)

// newHardwareAddr returns a random locally-administered unicast MAC.
func newHardwareAddr() (net.HardwareAddr, error) {
	hw := make(net.HardwareAddr, 6)
	if _, err := rand.Read(hw); err != nil {
		return nil, fmt.Errorf("failed to generate VTEP MAC: %v", err)
	}
	hw[0] = (hw[0] | 0x02) & 0xfe // locally administered, unicast
	return hw, nil
}

// newVxlanLink builds the netlink description of the VTEP device.
//
// The MAC is assigned explicitly rather than left to the kernel. A kernel-random
// MAC has addr_assign_type=NET_ADDR_RANDOM, and systemd-udevd (>= v242, e.g.
// Ubuntu 24.04's 99-default.link MACAddressPolicy=persistent) rewrites such
// MACs shortly after the device appears. flanneld publishes the VTEP MAC in
// its subnet lease, so if udev changes it afterwards every peer installs
// FDB/neigh entries for a MAC this host no longer owns and cross-node overlay
// traffic is silently dropped. An explicitly set MAC (NET_ADDR_SET) is left
// alone by udev.
func newVxlanLink(devAttrs *vxlanDeviceAttrs) (*netlink.Vxlan, error) {
	hw, err := newHardwareAddr()
	if err != nil {
		return nil, err
	}
	return &netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:         devAttrs.name,
			HardwareAddr: hw,
		},
		VxlanId:      int(devAttrs.vni),
		VtepDevIndex: devAttrs.vtepIndex,
		SrcAddr:      devAttrs.vtepAddr,
		Port:         devAttrs.vtepPort,
		Learning:     false,
	}, nil
}

func newVXLANDevice(devAttrs *vxlanDeviceAttrs) (*vxlanDevice, error) {
	link, err := newVxlanLink(devAttrs)
	if err != nil {
		return nil, err
	}

	link, err = ensureLink(link)
	if err != nil {
		return nil, err
	}

	return &vxlanDevice{
		link: link,
	}, nil
}

// EnsureMAC re-reads the device and, if its MAC no longer matches expected
// (the MAC advertised in our subnet lease), sets it back. This is a backstop
// against anything (udev MACAddressPolicy, NetworkManager, an operator) that
// changes the VTEP MAC after the lease was published. Returns true if a repair
// was made.
func (dev *vxlanDevice) EnsureMAC(expected net.HardwareAddr) (bool, error) {
	if len(expected) == 0 {
		return false, nil
	}
	current, err := linkByIndex(dev.link.Index)
	if err != nil {
		return false, fmt.Errorf("failed to read %s: %v", dev.link.Attrs().Name, err)
	}
	got := current.Attrs().HardwareAddr
	if bytesEqualMAC(got, expected) {
		return false, nil
	}
	log.Warningf("%s MAC changed from advertised %s to %s (udev MACAddressPolicy?); restoring", dev.link.Attrs().Name, expected, got)
	if err := linkSetHardwareAddr(dev.link, expected); err != nil {
		return false, fmt.Errorf("failed to restore MAC %s on %s: %v", expected, dev.link.Attrs().Name, err)
	}
	dev.link.HardwareAddr = expected
	return true, nil
}

func bytesEqualMAC(a, b net.HardwareAddr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func ensureLink(vxlan *netlink.Vxlan) (*netlink.Vxlan, error) {
	err := netlink.LinkAdd(vxlan)
	if err == syscall.EEXIST {
		// it's ok if the device already exists as long as config is similar
		existing, err := netlink.LinkByName(vxlan.Name)
		if err != nil {
			return nil, err
		}

		incompat := vxlanLinksIncompat(vxlan, existing)
		if incompat == "" {
			return existing.(*netlink.Vxlan), nil
		}

		// delete existing
		log.Warningf("%q already exists with incompatible configuration: %v; recreating device", vxlan.Name, incompat)
		if err = netlink.LinkDel(existing); err != nil {
			return nil, fmt.Errorf("failed to delete interface: %v", err)
		}

		// create new
		if err = netlink.LinkAdd(vxlan); err != nil {
			return nil, fmt.Errorf("failed to create vxlan interface: %v", err)
		}
	} else if err != nil {
		return nil, err
	}

	// The vendored netlink LinkAdd does not send IFLA_ADDRESS ("TODO: set mtu
	// and hardware address"), so the kernel still picks a random MAC
	// (NET_ADDR_RANDOM) that udev's MACAddressPolicy=persistent will rewrite.
	// Set it explicitly now; RTM_SETLINK with IFLA_ADDRESS marks the address
	// NET_ADDR_SET, which udev leaves alone. See newVxlanLink.
	if err := applyHardwareAddr(vxlan); err != nil {
		return nil, err
	}

	ifindex := vxlan.Index
	link, err := netlink.LinkByIndex(vxlan.Index)
	if err != nil {
		return nil, fmt.Errorf("can't locate created vxlan device with index %v", ifindex)
	}
	var ok bool
	if vxlan, ok = link.(*netlink.Vxlan); !ok {
		return nil, fmt.Errorf("created vxlan device with index %v is not vxlan", ifindex)
	}

	return vxlan, nil
}

// applyHardwareAddr sets the MAC chosen in newVxlanLink on a freshly created
// device. It is a no-op when no MAC was chosen.
func applyHardwareAddr(vxlan *netlink.Vxlan) error {
	if len(vxlan.HardwareAddr) == 0 {
		return nil
	}
	if err := linkSetHardwareAddr(vxlan, vxlan.HardwareAddr); err != nil {
		return fmt.Errorf("failed to set VTEP MAC %s on %s: %v", vxlan.HardwareAddr, vxlan.Name, err)
	}
	return nil
}

func (dev *vxlanDevice) Configure(ipn ip.IP4Net, overlay ip.IP4Net) error {
	if err := setAddr4(dev.link, ipn.ToIPNet()); err != nil {
		return err
	}

	if err := netlink.LinkSetUp(dev.link); err != nil {
		return fmt.Errorf("failed to set interface %s to UP state: %s", dev.link.Attrs().Name, err)
	}

	if ipn.PrefixLen < 32 {
		// When configured with the full overlay prefix, explicitly add a route
		// since there might be a route for a subnet already installed by Docker
		// and then it won't get auto added.
		route := netlink.Route{
			LinkIndex: dev.link.Attrs().Index,
			Scope:     netlink.SCOPE_UNIVERSE,
			Dst:       ipn.Network().ToIPNet(),
		}
		if err := netlink.RouteAdd(&route); err != nil && err != syscall.EEXIST {
			return fmt.Errorf("failed to add route (%s -> %s): %v", ipn.Network().String(), dev.link.Attrs().Name, err)
		}
		return nil
	}

	// With a /32 address, add a link-scoped route for the overlay so remote
	// subnet routes can be installed via handleSubnetEvents. Use scope link
	// rather than assigning the overlay prefix to the interface address, which
	// would cause decapsulated packets to be delivered locally on flannel.1.
	linkRoute := netlink.Route{
		LinkIndex: dev.link.Attrs().Index,
		Scope:     netlink.SCOPE_LINK,
		Dst:       overlay.ToIPNet(),
	}
	if err := netlink.RouteAdd(&linkRoute); err != nil && err != syscall.EEXIST {
		return fmt.Errorf("failed to add overlay link route (%s -> %s): %v", overlay.String(), dev.link.Attrs().Name, err)
	}

	return nil
}

func (dev *vxlanDevice) Destroy() {
	netlink.LinkDel(dev.link)
}

func (dev *vxlanDevice) MACAddr() net.HardwareAddr {
	return dev.link.HardwareAddr
}

func (dev *vxlanDevice) MTU() int {
	return dev.link.MTU
}

type neigh struct {
	MAC net.HardwareAddr
	IP  ip.IP4
}

// fdbSet installs (or replaces) an FDB entry. It is a variable so tests can
// assert the replace semantics AddL2 relies on. It must use replace semantics
// (netlink.NeighSet / NLM_F_REPLACE), not exclusive-add.
var fdbSet = netlink.NeighSet

func (dev *vxlanDevice) AddL2(n neigh) error {
	// Use NeighSet (NLM_F_REPLACE) rather than NeighAdd (NLM_F_EXCL) so a peer
	// whose flannel.1 device was recreated with a new VTEP MAC (e.g. after a
	// flanneld restart) overwrites the stale FDB entry. NeighAdd would fail with
	// EEXIST and leave the old MAC in place, causing decapsulated frames to carry
	// an inner destination MAC the peer no longer owns, which the kernel drops.
	log.Infof("calling NeighSet (fdb): %v, %v", n.IP, n.MAC)
	return fdbSet(&netlink.Neigh{
		LinkIndex:    dev.link.Index,
		State:        netlink.NUD_PERMANENT,
		Family:       syscall.AF_BRIDGE,
		Flags:        netlink.NTF_SELF,
		IP:           n.IP.ToIP(),
		HardwareAddr: n.MAC,
	})
}

func (dev *vxlanDevice) DelL2(n neigh) error {
	log.Infof("calling NeighDel: %v, %v", n.IP, n.MAC)
	return netlink.NeighDel(&netlink.Neigh{
		LinkIndex:    dev.link.Index,
		Family:       syscall.AF_BRIDGE,
		Flags:        netlink.NTF_SELF,
		IP:           n.IP.ToIP(),
		HardwareAddr: n.MAC,
	})
}

func (dev *vxlanDevice) AddL3(n neigh) error {
	log.Infof("calling NeighSet: %v, %v", n.IP, n.MAC)
	return netlink.NeighSet(&netlink.Neigh{
		LinkIndex:    dev.link.Index,
		State:        netlink.NUD_PERMANENT,
		Type:         syscall.RTN_UNICAST,
		IP:           n.IP.ToIP(),
		HardwareAddr: n.MAC,
	})
}

func (dev *vxlanDevice) DelL3(n neigh) error {
	log.Infof("calling NeighDel: %v, %v", n.IP, n.MAC)
	return netlink.NeighDel(&netlink.Neigh{
		LinkIndex:    dev.link.Index,
		State:        netlink.NUD_PERMANENT,
		Type:         syscall.RTN_UNICAST,
		IP:           n.IP.ToIP(),
		HardwareAddr: n.MAC,
	})
}

func (dev *vxlanDevice) AddRoute(subnet ip.IP4Net) error {
	route := &netlink.Route{
		LinkIndex: dev.link.Index,
		Scope:     netlink.SCOPE_UNIVERSE,
		Dst:       subnet.ToIPNet(),
		Gw:        subnet.IP.ToIP(),
	}

	log.Infof("calling RouteAdd: %s", subnet)
	return netlink.RouteAdd(route)
}

func (dev *vxlanDevice) DelRoute(subnet ip.IP4Net) error {
	route := &netlink.Route{
		LinkIndex: dev.link.Index,
		Scope:     netlink.SCOPE_UNIVERSE,
		Dst:       subnet.ToIPNet(),
		Gw:        subnet.IP.ToIP(),
	}
	log.Infof("calling RouteDel: %s", subnet)
	return netlink.RouteDel(route)
}

func vxlanLinksIncompat(l1, l2 netlink.Link) string {
	if l1.Type() != l2.Type() {
		return fmt.Sprintf("link type: %v vs %v", l1.Type(), l2.Type())
	}

	v1 := l1.(*netlink.Vxlan)
	v2 := l2.(*netlink.Vxlan)

	if v1.VxlanId != v2.VxlanId {
		return fmt.Sprintf("vni: %v vs %v", v1.VxlanId, v2.VxlanId)
	}

	if v1.VtepDevIndex > 0 && v2.VtepDevIndex > 0 && v1.VtepDevIndex != v2.VtepDevIndex {
		return fmt.Sprintf("vtep (external) interface: %v vs %v", v1.VtepDevIndex, v2.VtepDevIndex)
	}

	if len(v1.SrcAddr) > 0 && len(v2.SrcAddr) > 0 && !v1.SrcAddr.Equal(v2.SrcAddr) {
		return fmt.Sprintf("vtep (external) IP: %v vs %v", v1.SrcAddr, v2.SrcAddr)
	}

	if len(v1.Group) > 0 && len(v2.Group) > 0 && !v1.Group.Equal(v2.Group) {
		return fmt.Sprintf("group address: %v vs %v", v1.Group, v2.Group)
	}

	if v1.L2miss != v2.L2miss {
		return fmt.Sprintf("l2miss: %v vs %v", v1.L2miss, v2.L2miss)
	}

	if v1.Port > 0 && v2.Port > 0 && v1.Port != v2.Port {
		return fmt.Sprintf("port: %v vs %v", v1.Port, v2.Port)
	}

	return ""
}

// sets IP4 addr on link removing any existing ones first
func setAddr4(link *netlink.Vxlan, ipn *net.IPNet) error {
	addrs, err := netlink.AddrList(link, syscall.AF_INET)
	if err != nil {
		return err
	}

	addr := netlink.Addr{IPNet: ipn}
	existing := false
	for _, old := range addrs {
		if old.IPNet.String() == addr.IPNet.String() {
			existing = true
			continue
		}
		if err = netlink.AddrDel(link, &old); err != nil {
			return fmt.Errorf("failed to delete IPv4 addr %s from %s", old.String(), link.Attrs().Name)
		}
	}

	if !existing {
		if err = netlink.AddrAdd(link, &addr); err != nil {
			return fmt.Errorf("failed to add IP address %s to %s: %s", ipn.String(), link.Attrs().Name, err)
		}
	}

	return nil
}
