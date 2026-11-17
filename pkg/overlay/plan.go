// Package overlay plans a live flannel network change.
//
// The discoverd flannel service metadata is the source of truth. Existing
// subnet leases are kept. NAT and the flannel.1 link route are a separate
// host apply, ordered so a failed host never looks like success.
package overlay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/randy-girard/flynn/pkg/hostfw"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/iptables"
)

const (
	// PhaseNone means the requested CIDR and subnet length are already stored.
	PhaseNone = "none"
	// PhaseHostsThenMeta widens host NAT before discoverd can hand out a new subnet.
	PhaseHostsThenMeta = "hosts-then-meta"
	// PhaseMetaThenHosts publishes a shrink or a subnet-length change before narrowing NAT.
	PhaseMetaThenHosts = "meta-then-hosts"

	defaultNetwork = "100.100.0.0/16"
)

// Lease is one allocated host subnet in flannel metadata.
type Lease struct {
	Key      string
	CIDR     string
	PublicIP string
}

// Network is a parsed flannel overlay config.
type Network struct {
	CIDR        string
	SubnetLen   uint
	SubnetMin   string
	SubnetMax   string
	HostSubnets int
}

// Capacity is allocated leases versus the flannel host-subnet pool.
type Capacity struct {
	Allocated   int
	Free        int
	HostSubnets int
}

// Change is the result of planning a set or inspecting live metadata.
type Change struct {
	Current  Network
	Next     Network
	Changed  bool
	Phase    string
	Leases   []Lease
	Capacity Capacity
}

// Host is one cluster node the command must check or update.
type Host struct {
	ID       string
	Subnet   string
	Overlay  string
	Underlay string
}

// HostView is the per-host line in overlay:show.
type HostView struct {
	ID      string
	Subnet  string
	Overlay string
}

// Store is the discoverd flannel metadata CAS.
type Store interface {
	Get() (data []byte, index uint64, err error)
	Set(data []byte, index uint64) error
}

type parsed struct {
	raw     []byte
	config  json.RawMessage
	subnets json.RawMessage
	backend json.RawMessage
	current Network
	leases  []Lease
}

// Inspect reads live metadata without validating a change.
func Inspect(meta []byte) (Change, error) {
	p, err := parseMeta(meta)
	if err != nil {
		return Change{}, err
	}
	return changeFrom(p, p.current, false, PhaseNone), nil
}

// Plan validates want as the next overlay. subnetLen 0 keeps the current
// length when it still fits, otherwise the flannel default.
func Plan(meta []byte, want string, subnetLen uint, underlay []string) (Change, error) {
	p, err := parseMeta(meta)
	if err != nil {
		return Change{}, err
	}
	next, err := validate(p, want, subnetLen, underlay)
	if err != nil {
		return Change{}, err
	}
	changed := next.CIDR != p.current.CIDR || next.SubnetLen != p.current.SubnetLen
	phase := PhaseNone
	if changed {
		phase = phaseFor(p.current.CIDR, next.CIDR)
	}
	return changeFrom(p, next, changed, phase), nil
}

func changeFrom(p parsed, next Network, changed bool, phase string) Change {
	return Change{
		Current:  p.current,
		Next:     next,
		Changed:  changed,
		Phase:    phase,
		Leases:   p.leases,
		Capacity: capacity(next, len(p.leases)),
	}
}

func capacity(n Network, allocated int) Capacity {
	free := n.HostSubnets - allocated
	if free < 0 {
		free = 0
	}
	return Capacity{Allocated: allocated, Free: free, HostSubnets: n.HostSubnets}
}

func phaseFor(current, next string) string {
	if current == next {
		return PhaseMetaThenHosts
	}
	if cidrContains(next, current) {
		return PhaseHostsThenMeta
	}
	return PhaseMetaThenHosts
}

func validate(p parsed, want string, subnetLen uint, underlay []string) (Network, error) {
	want = strings.TrimSpace(want)
	ip, network, err := net.ParseCIDR(want)
	if err != nil || ip.To4() == nil || network == nil {
		return Network{}, fmt.Errorf("overlay %q is not an IPv4 CIDR", want)
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || ones < 8 || ones > 28 {
		return Network{}, fmt.Errorf("overlay prefix /%d is outside 8-28", ones)
	}
	cidr := network.String()
	if !insideCluster(cidr) {
		return Network{}, fmt.Errorf("overlay %s is outside the host firewall cluster ranges", cidr)
	}
	length, err := chooseLen(ones, subnetLen, p.current.SubnetLen)
	if err != nil {
		return Network{}, err
	}
	next, err := normalize(cidr, length)
	if err != nil {
		return Network{}, err
	}
	for _, ip := range underlay {
		addr := underlayIP(ip)
		if addr == "" {
			continue
		}
		if ipInside(next.CIDR, addr) {
			return Network{}, fmt.Errorf("overlay %s contains host underlay IP %s", next.CIDR, addr)
		}
	}
	for _, lease := range p.leases {
		if !cidrContains(next.CIDR, lease.CIDR) {
			return Network{}, fmt.Errorf("overlay %s does not contain lease %s", next.CIDR, lease.CIDR)
		}
	}
	if next.CIDR != p.current.CIDR && !cidrContains(next.CIDR, p.current.CIDR) && !cidrContains(p.current.CIDR, next.CIDR) {
		return Network{}, fmt.Errorf("refusing to move the overlay from %s to %s; expand to a network that contains %s", p.current.CIDR, next.CIDR, p.current.CIDR)
	}
	return next, nil
}

func chooseLen(prefix int, requested, current uint) (uint, error) {
	if requested == 0 {
		if current > uint(prefix) && current < 32 {
			return current, nil
		}
		if prefix < 24 {
			return 24, nil
		}
		return uint(prefix) + 1, nil
	}
	if requested <= uint(prefix) || requested >= 32 {
		return 0, fmt.Errorf("subnet length %d is not valid for a /%d overlay", requested, prefix)
	}
	return requested, nil
}

// normalize applies flannel's ParseConfig defaults: skip the first subnet,
// and end on the last subnet that fits. It does not import flannel/pkg/ip,
// which pulls in Linux netlink.
func normalize(cidr string, length uint) (Network, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil || network == nil || network.IP.To4() == nil {
		return Network{}, fmt.Errorf("overlay %q is not an IPv4 CIDR", cidr)
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || length == 0 || length >= 32 || length < uint(ones) {
		return Network{}, fmt.Errorf("subnet length %d is not valid for %s", length, network.String())
	}
	base := ip4(network.IP)
	subnetSize := uint32(1) << (32 - length)
	netSize := uint32(1) << (32 - uint(ones))
	min := base + subnetSize
	max := base + netSize - subnetSize
	if max < min {
		return Network{}, fmt.Errorf("overlay %s has no host subnets at length %d", network.String(), length)
	}
	return Network{
		CIDR:        network.String(),
		SubnetLen:   length,
		SubnetMin:   formatIP4(min),
		SubnetMax:   formatIP4(max),
		HostSubnets: int((max-min)/subnetSize) + 1,
	}, nil
}

func ip4(ip net.IP) uint32 {
	v := ip.To4()
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

func formatIP4(v uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func parseMeta(meta []byte) (parsed, error) {
	raw, err := unwrapMeta(meta)
	if err != nil {
		return parsed{}, err
	}
	var doc struct {
		Config  json.RawMessage `json:"config"`
		Subnets json.RawMessage `json:"subnets"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return parsed{}, fmt.Errorf("flannel metadata: %s", err)
	}
	config := bytes.TrimSpace(doc.Config)
	if len(config) == 0 || string(config) == "null" {
		config = []byte(`{"Network":"` + defaultNetwork + `"}`)
	}
	var backend struct {
		Backend   json.RawMessage `json:"Backend"`
		Network   string          `json:"Network"`
		SubnetLen uint            `json:"SubnetLen"`
	}
	if err := json.Unmarshal(config, &backend); err != nil {
		return parsed{}, fmt.Errorf("flannel config: %s", err)
	}
	if strings.TrimSpace(backend.Network) == "" {
		backend.Network = defaultNetwork
	}
	current, err := normalize(backend.Network, effectiveLen(backend.Network, backend.SubnetLen))
	if err != nil {
		return parsed{}, err
	}
	leases, err := parseLeases(doc.Subnets)
	if err != nil {
		return parsed{}, err
	}
	return parsed{
		raw:     raw,
		config:  config,
		subnets: doc.Subnets,
		backend: backend.Backend,
		current: current,
		leases:  leases,
	}, nil
}

func effectiveLen(cidr string, stored uint) uint {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil || network == nil {
		return stored
	}
	ones, _ := network.Mask.Size()
	if stored > uint(ones) && stored < 32 {
		return stored
	}
	if ones < 24 {
		return 24
	}
	return uint(ones) + 1
}

func parseLeases(raw json.RawMessage) ([]Lease, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var subs map[string]json.RawMessage
	if err := json.Unmarshal(raw, &subs); err != nil {
		return nil, fmt.Errorf("flannel leases: %s", err)
	}
	leases := make([]Lease, 0, len(subs))
	for key, body := range subs {
		cidr, err := leaseCIDR(key)
		if err != nil {
			return nil, err
		}
		var attrs struct {
			PublicIP string `json:"PublicIP"`
		}
		if len(body) > 0 {
			_ = json.Unmarshal(body, &attrs)
		}
		leases = append(leases, Lease{Key: key, CIDR: cidr, PublicIP: attrs.PublicIP})
	}
	sort.Slice(leases, func(i, j int) bool { return leases[i].CIDR < leases[j].CIDR })
	return leases, nil
}

func leaseCIDR(key string) (string, error) {
	i := strings.LastIndex(key, "-")
	if i <= 0 || i == len(key)-1 {
		return "", fmt.Errorf("cannot parse flannel lease %q", key)
	}
	cidr := key[:i] + "/" + key[i+1:]
	if _, _, err := net.ParseCIDR(cidr); err != nil {
		return "", fmt.Errorf("cannot parse flannel lease %q", key)
	}
	return canonicalCIDR(cidr), nil
}

func unwrapMeta(data []byte) ([]byte, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("flannel meta is empty")
	}
	for i := 0; i < 4; i++ {
		if data[0] != '"' {
			break
		}
		var wrapped string
		if err := json.Unmarshal(data, &wrapped); err != nil {
			return nil, err
		}
		data = bytes.TrimSpace([]byte(wrapped))
		if len(data) == 0 {
			return nil, errors.New("flannel meta is empty")
		}
	}
	return data, nil
}

// ApplyMeta rewrites the config object and leaves the subnets object bytes alone.
func ApplyMeta(meta []byte, next Network) ([]byte, error) {
	p, err := parseMeta(meta)
	if err != nil {
		return nil, err
	}
	cfg := struct {
		Network   string          `json:"Network"`
		SubnetMin string          `json:"SubnetMin,omitempty"`
		SubnetMax string          `json:"SubnetMax,omitempty"`
		SubnetLen uint            `json:"SubnetLen,omitempty"`
		Backend   json.RawMessage `json:"Backend,omitempty"`
	}{
		Network:   next.CIDR,
		SubnetMin: next.SubnetMin,
		SubnetMax: next.SubnetMax,
		SubnetLen: next.SubnetLen,
		Backend:   p.backend,
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	doc := struct {
		Config  json.RawMessage `json:"config"`
		Subnets json.RawMessage `json:"subnets,omitempty"`
	}{Config: encoded, Subnets: p.subnets}
	return json.Marshal(doc)
}

// Commit CAS-writes the next network, retrying when a lease renew wins the index.
func Commit(store Store, network string, subnetLen uint, underlay []string) error {
	var last error
	for i := 0; i < 5; i++ {
		data, index, err := store.Get()
		if err != nil {
			return err
		}
		if index == 0 {
			return errors.New("flannel metadata has no index; re-run flynn-host overlay:set once flannel has created it")
		}
		change, err := Plan(data, network, subnetLen, underlay)
		if err != nil {
			return err
		}
		if !change.Changed {
			return nil
		}
		next, err := ApplyMeta(data, change.Next)
		if err != nil {
			return err
		}
		if err := store.Set(next, index); err != nil {
			if hh.IsPreconditionFailedError(err) {
				last = err
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("flannel metadata changed while updating; re-run flynn-host overlay:set: %v", last)
}

// HostMatches reports whether the host is already enforcing metaNetwork.
// An empty stored overlay uses the /16 inferred from the bridge subnet.
func HostMatches(metaNetwork, hostSubnet, stored string) bool {
	meta := canonicalCIDR(metaNetwork)
	if strings.TrimSpace(stored) != "" {
		return canonicalCIDR(stored) == meta
	}
	if strings.TrimSpace(hostSubnet) == "" {
		return false
	}
	return iptables.OverlayNetworkFor(hostSubnet) == meta
}

// LinkRoutePlan is the flannel.1 link route to add, and the strictly broader
// route to delete. A wider overlay keeps the old narrower route.
func LinkRoutePlan(stored, hostSubnet, desired string) (add, del string) {
	add = canonicalCIDR(desired)
	for _, old := range []string{strings.TrimSpace(stored), iptables.OverlayNetworkFor(hostSubnet)} {
		oldNet := canonicalCIDR(old)
		if oldNet == "" || oldNet == add || strings.Contains(oldNet, " ") {
			continue
		}
		if _, _, err := net.ParseCIDR(oldNet); err != nil {
			continue
		}
		if cidrContains(oldNet, add) && !cidrContains(add, oldNet) {
			del = oldNet
		}
	}
	return add, del
}

// FormatShow is the overlay:show body. It describes change.Next.
func FormatShow(c Change, hosts []HostView) string {
	n := c.Next
	if n.CIDR == "" {
		n = c.Current
	}
	var b strings.Builder
	fmt.Fprintf(&b, "network=%s subnet_len=%d\n", n.CIDR, n.SubnetLen)
	fmt.Fprintf(&b, "allocated=%d free=%d host_subnets=%d\n", c.Capacity.Allocated, c.Capacity.Free, c.Capacity.HostSubnets)
	b.WriteString("leases:\n")
	if len(c.Leases) == 0 {
		b.WriteString("  (none)\n")
	} else {
		for _, lease := range c.Leases {
			fmt.Fprintf(&b, "  %s %s\n", lease.CIDR, lease.PublicIP)
		}
	}
	if len(hosts) > 0 {
		b.WriteString("hosts:\n")
		sorted := append([]HostView(nil), hosts...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
		for _, h := range sorted {
			applied, ok := hostApplied(n.CIDR, h.Subnet, h.Overlay)
			if ok {
				fmt.Fprintf(&b, "  %s applied=%s\n", h.ID, applied)
			} else {
				fmt.Fprintf(&b, "  %s applied=%s behind\n", h.ID, applied)
			}
		}
	}
	return b.String()
}

func hostApplied(metaNetwork, hostSubnet, stored string) (string, bool) {
	if strings.TrimSpace(stored) != "" {
		got := canonicalCIDR(stored)
		return got, got == canonicalCIDR(metaNetwork)
	}
	inferred := iptables.OverlayNetworkFor(hostSubnet)
	return inferred, inferred == canonicalCIDR(metaNetwork)
}

// Execute applies a planned change. A failed host is an error, never a success.
func Execute(store Store, hosts []Host, apply func(id, network string) error, network string, subnetLen uint) (string, error) {
	if len(hosts) == 0 {
		return "", errors.New("no hosts found")
	}
	if apply == nil {
		return "", errors.New("no host apply")
	}
	data, _, err := store.Get()
	if err != nil {
		return "", err
	}
	change, err := Plan(data, network, subnetLen, underlayFrom(hosts))
	if err != nil {
		return "", err
	}
	underlay := underlayFrom(hosts)
	if !change.Changed {
		var behind []Host
		for _, h := range hosts {
			if !HostMatches(change.Next.CIDR, h.Subnet, h.Overlay) {
				behind = append(behind, h)
			}
		}
		if len(behind) == 0 {
			return fmt.Sprintf("overlay unchanged network=%s subnet_len=%d\nallocated=%d free=%d host_subnets=%d\n",
				change.Next.CIDR, change.Next.SubnetLen, change.Capacity.Allocated, change.Capacity.Free, change.Capacity.HostSubnets), nil
		}
		if err := applyAll(behind, change.Next.CIDR, apply); err != nil {
			return "", fmt.Errorf("overlay metadata was not changed; %s. Recovery: fix the host and re-run the same flynn-host overlay:set command", err)
		}
		ids := make([]string, len(behind))
		for i, h := range behind {
			ids[i] = h.ID
		}
		return fmt.Sprintf("reconciled %s onto %s\n", strings.Join(ids, ","), change.Next.CIDR), nil
	}
	if change.Phase == PhaseHostsThenMeta {
		if err := applyAll(hosts, change.Next.CIDR, apply); err != nil {
			return "", fmt.Errorf("overlay metadata was not changed; %s. Recovery: fix the host and re-run the same flynn-host overlay:set command", err)
		}
		if err := Commit(store, network, subnetLen, underlay); err != nil {
			return "", fmt.Errorf("hosts exclude %s but flannel metadata was not updated: %s. Recovery: re-run the same flynn-host overlay:set command", change.Next.CIDR, err)
		}
		return appliedText(change, len(hosts)), nil
	}
	if err := Commit(store, network, subnetLen, underlay); err != nil {
		return "", fmt.Errorf("flannel metadata was not updated and host NAT was not changed: %s. Recovery: re-run the same flynn-host overlay:set command", err)
	}
	if err := applyAll(hosts, change.Next.CIDR, apply); err != nil {
		return "", fmt.Errorf("flannel metadata is %s but %s. Recovery: fix the host and re-run the same flynn-host overlay:set command. Do not shrink the overlay under allocated leases", change.Next.CIDR, err)
	}
	return appliedText(change, len(hosts)), nil
}

func appliedText(c Change, n int) string {
	return fmt.Sprintf("applied %s subnet_len=%d on %d hosts\nallocated=%d free=%d host_subnets=%d\n",
		c.Next.CIDR, c.Next.SubnetLen, n, c.Capacity.Allocated, c.Capacity.Free, c.Capacity.HostSubnets)
}

func applyAll(hosts []Host, network string, apply func(id, network string) error) error {
	var failed []string
	for _, h := range hosts {
		if err := apply(h.ID, network); err != nil {
			failed = append(failed, h.ID+": "+err.Error())
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return errors.New(strings.Join(failed, "; "))
}

func underlayFrom(hosts []Host) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if ip := underlayIP(h.Underlay); ip != "" {
			out = append(out, ip)
		}
	}
	return out
}

func underlayIP(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		return host
	}
	return s
}

func insideCluster(cidr string) bool {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	for _, c := range hostfw.ClusterCIDRs() {
		_, outer, err := net.ParseCIDR(c)
		if err != nil {
			continue
		}
		if cidrInside(outer, n) {
			return true
		}
	}
	return false
}

func cidrContains(outer, inner string) bool {
	_, o, err1 := net.ParseCIDR(outer)
	_, i, err2 := net.ParseCIDR(inner)
	if err1 != nil || err2 != nil {
		return false
	}
	return cidrInside(o, i)
}

func cidrInside(outer, inner *net.IPNet) bool {
	o, obits := outer.Mask.Size()
	i, ibits := inner.Mask.Size()
	if obits != 32 || ibits != 32 || o > i {
		return false
	}
	return outer.Contains(inner.IP)
}

func ipInside(cidr, ip string) bool {
	_, n, err := net.ParseCIDR(cidr)
	addr := net.ParseIP(ip)
	if err != nil || addr == nil || n == nil {
		return false
	}
	return n.Contains(addr)
}

func canonicalCIDR(cidr string) string {
	cidr = strings.TrimSpace(cidr)
	if cidr == "" {
		return ""
	}
	_, network, err := net.ParseCIDR(cidr)
	if err != nil || network == nil {
		return cidr
	}
	return network.String()
}
