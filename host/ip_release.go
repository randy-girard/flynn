package main

import (
	"net"
	"sort"
	"syscall"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/opencontainers/runc/libcontainer/configs"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/vishvananda/netlink"
)

// A finished job's bridge address must not go to another job while anything
// can still reach the old one. Two things outlive the job:
//
//   - The host veth. The kernel frees a network namespace, and with it the
//     veth pair, only when its last reference goes. A TCP socket that the job
//     closed with unsent data is such a reference: it keeps retransmitting
//     for minutes after every process has exited (2026-11-21: the platform
//     postgres primary's port stayed on flynnbr0 2m14s after the job ended).
//     A second container with the same address on the bridge then splits
//     ARP between the two.
//   - Clients and caches that still hold the old address (discoverd
//     instances, open connections, conntrack).
//
// So cleanup deletes the host veth itself (deleting one end removes both),
// keeps the address allocated while a veth it could not delete is still
// there, and then holds it for ipReleaseQuarantine. On restart the holds are
// rebuilt from the jobs in the state DB and from bridge ports that no
// restored container owns.
var (
	ipReleaseQuarantine = 2 * time.Minute
	ipLinkPollInterval  = 2 * time.Second
	// ipLinkWarnAfter is how long a stuck veth is waited for before a warning.
	ipLinkWarnAfter = time.Minute
)

// bridgeLinks is the part of netlink the address release path uses.
type bridgeLinks interface {
	// Lookup returns the index of the link with this name.
	Lookup(name string) (int, bool)
	// Present reports whether the link with this index still has this name.
	Present(index int, name string) bool
	Delete(index int, name string) error
}

type netlinkLinks struct{}

func (netlinkLinks) Lookup(name string) (int, bool) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return 0, false
	}
	return link.Attrs().Index, true
}

func (netlinkLinks) Present(index int, name string) bool {
	link, err := netlink.LinkByIndex(index)
	return err == nil && link.Attrs().Name == name
}

func (netlinkLinks) Delete(index int, name string) error {
	link, err := netlink.LinkByIndex(index)
	if err != nil || link.Attrs().Name != name {
		return nil // already gone
	}
	return netlink.LinkDel(link)
}

func (l *LibcontainerBackend) bridgeLinks() bridgeLinks {
	if l.links != nil {
		return l.links
	}
	return netlinkLinks{}
}

// bridgePort is a link attached to the container bridge.
type bridgePort struct {
	Index int
	Name  string
}

// strayIP is an address in use behind a bridge port that no running
// container owns.
type strayIP struct {
	IP   net.IP
	Port bridgePort
}

// hostVethNames returns the host-side veth names of a container config.
func hostVethNames(cfg configs.Config) []string {
	var names []string
	for _, n := range cfg.Networks {
		if n != nil && n.Type == "veth" && n.HostInterfaceName != "" {
			names = append(names, n.HostInterfaceName)
		}
	}
	return names
}

// hostVeths returns the host veth names of c's libcontainer config.
func (c *Container) hostVeths() []string {
	if c.container == nil {
		return nil
	}
	cfg := c.container.Config()
	return hostVethNames(cfg)
}

// releaseContainerIP returns a finished job's address to the pool once its
// veths are gone and the quarantine has passed. It deletes the veths now;
// one that cannot be deleted keeps the address allocated until it vanishes.
func (l *LibcontainerBackend) releaseContainerIP(log log15.Logger, jobID string, ip net.IP, veths []string) {
	remaining := l.deleteHostVeths(log, ip, veths)
	if len(remaining) == 0 {
		l.quarantineIP(log, ip)
		return
	}
	go l.holdUntilPortsGone(log, ip, remaining)
}

// deleteHostVeths deletes the named host veths and returns the ones that are
// still present.
func (l *LibcontainerBackend) deleteHostVeths(log log15.Logger, ip net.IP, veths []string) []bridgePort {
	links := l.bridgeLinks()
	var remaining []bridgePort
	for _, name := range veths {
		index, ok := links.Lookup(name)
		if !ok {
			continue
		}
		if err := links.Delete(index, name); err != nil {
			log.Warn("error deleting host veth; holding its address until it goes", "veth", name, "ip", ip, "err", err)
		}
		if links.Present(index, name) {
			remaining = append(remaining, bridgePort{Index: index, Name: name})
		} else {
			log.Info("deleted host veth", "veth", name, "ip", ip)
		}
	}
	return remaining
}

func (l *LibcontainerBackend) quarantineIP(log log15.Logger, ip net.IP) {
	until := time.Now().Add(ipReleaseQuarantine)
	if err := l.ipalloc.ReleaseIPAfter(l.bridgeNet, ip, until); err != nil {
		log.Error("error quarantining ip; releasing it", "ip", ip, "err", err)
		l.ipalloc.ReleaseIP(l.bridgeNet, ip)
		return
	}
	log.Info("ip quarantined", "ip", ip, "until", until.UTC().Format(time.RFC3339))
}

// holdUntilPortsGone keeps ip allocated while any of ports is still a link,
// then quarantines it.
func (l *LibcontainerBackend) holdUntilPortsGone(log log15.Logger, ip net.IP, ports []bridgePort) {
	links := l.bridgeLinks()
	start := time.Now()
	warned := false
	for {
		present := false
		for _, p := range ports {
			if links.Present(p.Index, p.Name) {
				present = true
				break
			}
		}
		if !present {
			break
		}
		if !warned && time.Since(start) >= ipLinkWarnAfter {
			log.Warn("bridge port still present; its address stays allocated", "ip", ip, "ports", ports, "waited", time.Since(start).String())
			warned = true
		}
		time.Sleep(ipLinkPollInterval)
	}
	log.Info("bridge port gone", "ip", ip, "ports", ports, "waited", time.Since(start).String())
	l.quarantineIP(log, ip)
}

// recentlyReleasedIPs returns the bridge addresses of jobs that ended less
// than q before now, with the time each may be reused. If an address was
// used by several jobs, the latest end wins.
func recentlyReleasedIPs(jobs map[string]*host.ActiveJob, now time.Time, q time.Duration) map[string]time.Time {
	out := make(map[string]time.Time)
	for _, j := range jobs {
		if j == nil || j.InternalIP == "" || !statusDown(j.Status) || j.EndedAt.IsZero() {
			continue
		}
		if j.Job != nil && j.Job.Config.HostNetwork {
			continue
		}
		ip := net.ParseIP(j.InternalIP)
		if ip == nil {
			continue
		}
		until := j.EndedAt.Add(q)
		if !until.After(now) {
			continue
		}
		key := ip.String()
		if prev, ok := out[key]; !ok || until.After(prev) {
			out[key] = until
		}
	}
	return out
}

// strayBridgeIPs maps bridge ports that no restored container owns to the
// addresses behind them: the port's learned MACs (bridge FDB) matched against
// the bridge's IPv4 neighbour table. Ports with no such address are returned
// separately.
func strayBridgeIPs(ports []bridgePort, known map[string]bool, fdb, neigh []netlink.Neigh) ([]strayIP, []bridgePort) {
	macPort := make(map[string]bridgePort)
	byIndex := make(map[int]bridgePort)
	for _, p := range ports {
		if !known[p.Name] {
			byIndex[p.Index] = p
		}
	}
	for _, f := range fdb {
		p, ok := byIndex[f.LinkIndex]
		if !ok || f.State&netlink.NUD_PERMANENT != 0 || len(f.HardwareAddr) == 0 {
			continue
		}
		macPort[f.HardwareAddr.String()] = p
	}
	var strays []strayIP
	found := make(map[int]bool)
	for _, n := range neigh {
		if n.IP == nil || n.IP.To4() == nil || len(n.HardwareAddr) == 0 {
			continue
		}
		if n.State&(netlink.NUD_FAILED|netlink.NUD_INCOMPLETE|netlink.NUD_NOARP) != 0 {
			continue
		}
		p, ok := macPort[n.HardwareAddr.String()]
		if !ok {
			continue
		}
		strays = append(strays, strayIP{IP: n.IP.To4(), Port: p})
		found[p.Index] = true
	}
	var unknown []bridgePort
	for _, p := range ports {
		if !known[p.Name] && !found[p.Index] {
			unknown = append(unknown, p)
		}
	}
	sort.Slice(strays, func(i, j int) bool { return strays[i].IP.String() < strays[j].IP.String() })
	return strays, unknown
}

// restoreIPHolds rebuilds the address holds after a restart. owned are the
// addresses of the restored running containers, which are already
// allocated. Stray addresses stay allocated until their port goes; then
// they, like the addresses of recently ended jobs, sit out the quarantine.
func (l *LibcontainerBackend) restoreIPHolds(log log15.Logger, jobs map[string]*host.ActiveJob, owned map[string]bool, strays []strayIP) {
	strayKeys := make(map[string]bool)
	for _, s := range strays {
		key := s.IP.String()
		if owned[key] {
			log.Error("a stray bridge port has a running container's address", "ip", key, "port", s.Port.Name)
			continue
		}
		if strayKeys[key] {
			continue
		}
		strayKeys[key] = true
		if _, err := l.ipalloc.RequestIP(l.bridgeNet, s.IP); err != nil {
			log.Warn("error holding stray bridge address", "ip", key, "port", s.Port.Name, "err", err)
		}
		log.Info("holding address of a stray bridge port", "ip", key, "port", s.Port.Name)
		go l.holdUntilPortsGone(log, s.IP, []bridgePort{s.Port})
	}
	for key, until := range recentlyReleasedIPs(jobs, time.Now(), ipReleaseQuarantine) {
		if owned[key] || strayKeys[key] {
			continue
		}
		ip := net.ParseIP(key)
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
		}
		if err := l.ipalloc.ReleaseIPAfter(l.bridgeNet, ip, until); err != nil {
			log.Warn("error quarantining recently released address", "ip", key, "err", err)
			continue
		}
		log.Info("quarantined recently released address", "ip", key, "until", until.UTC().Format(time.RFC3339))
	}
}

// scanBridgePorts lists the links on the bridge with their FDB and IPv4
// neighbour entries.
func scanBridgePorts(bridge netlink.Link) ([]bridgePort, []netlink.Neigh, []netlink.Neigh, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, nil, nil, err
	}
	var ports []bridgePort
	for _, link := range links {
		a := link.Attrs()
		if a.MasterIndex == bridge.Attrs().Index {
			ports = append(ports, bridgePort{Index: a.Index, Name: a.Name})
		}
	}
	fdb, err := netlink.NeighList(0, syscall.AF_BRIDGE)
	if err != nil {
		return nil, nil, nil, err
	}
	neigh, err := netlink.NeighList(bridge.Attrs().Index, netlink.FAMILY_V4)
	if err != nil {
		return nil, nil, nil, err
	}
	return ports, fdb, neigh, nil
}
