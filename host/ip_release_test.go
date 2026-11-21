package main

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/opencontainers/runc/libcontainer/configs"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/ipallocator"
	"github.com/vishvananda/netlink"
)

// fakeLinks is an in-memory set of host links, keyed by name.
type fakeLinks struct {
	mu      sync.Mutex
	links   map[string]int
	stuck   map[string]bool // Delete fails and the link stays
	deleted []string
	order   *[]string
}

func newFakeLinks(names ...string) *fakeLinks {
	f := &fakeLinks{links: map[string]int{}, stuck: map[string]bool{}}
	for i, n := range names {
		f.links[n] = 100 + i
	}
	return f
}

func (f *fakeLinks) Lookup(name string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.links[name]
	return i, ok
}

func (f *fakeLinks) Present(index int, name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.links[name]
	return ok && i == index
}

func (f *fakeLinks) Delete(index int, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.order != nil {
		*f.order = append(*f.order, "delete "+name)
	}
	if f.stuck[name] {
		return errors.New("device or resource busy")
	}
	if i, ok := f.links[name]; ok && i == index {
		delete(f.links, name)
		f.deleted = append(f.deleted, name)
	}
	return nil
}

func (f *fakeLinks) vanish(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.links, name)
}

func testIPBackend(t *testing.T, links bridgeLinks) *LibcontainerBackend {
	t.Helper()
	_, bridgeNet, _ := net.ParseCIDR("100.100.61.0/29")
	return &LibcontainerBackend{
		LibcontainerConfig: &LibcontainerConfig{Logger: log15.New()},
		ipalloc:            ipallocator.New(),
		bridgeNet:          bridgeNet,
		links:              links,
	}
}

func setShortIPTimers(t *testing.T, quarantine time.Duration) {
	t.Helper()
	oldQ, oldPoll := ipReleaseQuarantine, ipLinkPollInterval
	ipReleaseQuarantine, ipLinkPollInterval = quarantine, 5*time.Millisecond
	t.Cleanup(func() { ipReleaseQuarantine, ipLinkPollInterval = oldQ, oldPoll })
}

func requestSpecific(l *LibcontainerBackend, ip string) error {
	_, err := l.ipalloc.RequestIP(l.bridgeNet, net.ParseIP(ip).To4())
	return err
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHostVethNames(t *testing.T) {
	cfg := configs.Config{Networks: []*configs.Network{
		{Type: "loopback"},
		{Type: "veth", HostInterfaceName: "veth22c1"},
		nil,
		{Type: "veth"},
	}}
	got := hostVethNames(cfg)
	if len(got) != 1 || got[0] != "veth22c1" {
		t.Fatalf("got %q, want [veth22c1]", got)
	}
}

// The finished job's host veth is removed before its address can go back to
// the pool, and the address then sits out the quarantine.
func TestReleaseContainerIPDeletesVethThenQuarantines(t *testing.T) {
	setShortIPTimers(t, 200*time.Millisecond)
	var order []string
	links := newFakeLinks("veth22c1")
	links.order = &order
	l := testIPBackend(t, links)
	if err := requestSpecific(l, "100.100.61.3"); err != nil {
		t.Fatal(err)
	}

	l.releaseContainerIP(l.Logger, "job-1", net.ParseIP("100.100.61.3").To4(), []string{"veth22c1"})

	if len(links.deleted) != 1 || links.deleted[0] != "veth22c1" {
		t.Fatalf("host veth not deleted: %q", links.deleted)
	}
	if err := requestSpecific(l, "100.100.61.3"); err != ipallocator.ErrIPAlreadyAllocated {
		t.Fatalf("address was free right after release (err %v); want it in quarantine", err)
	}
	time.Sleep(250 * time.Millisecond)
	if err := requestSpecific(l, "100.100.61.3"); err != nil {
		t.Fatalf("address still held after the quarantine: %v", err)
	}
}

// If the veth cannot be deleted, the address stays allocated for as long as
// the link is on the bridge, then sits out the quarantine.
func TestReleaseContainerIPHoldsWhileVethRemains(t *testing.T) {
	setShortIPTimers(t, 50*time.Millisecond)
	links := newFakeLinks("veth22c1")
	links.stuck["veth22c1"] = true
	l := testIPBackend(t, links)
	ip := "100.100.61.3"
	if err := requestSpecific(l, ip); err != nil {
		t.Fatal(err)
	}

	l.releaseContainerIP(l.Logger, "job-1", net.ParseIP(ip).To4(), []string{"veth22c1"})

	time.Sleep(150 * time.Millisecond) // longer than the quarantine
	for i := 0; i < 5; i++ {
		got, err := l.ipalloc.RequestIP(l.bridgeNet, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.Equal(net.ParseIP(ip)) {
			t.Fatalf("handed out %s while its old veth is still on the bridge", ip)
		}
	}
	links.vanish("veth22c1")
	eventually(t, "address freed after the veth went away", func() bool {
		_, err := l.ipalloc.RequestIP(l.bridgeNet, net.ParseIP(ip).To4())
		return err == nil
	})
}

func TestReleaseContainerIPWithoutVeth(t *testing.T) {
	setShortIPTimers(t, 50*time.Millisecond)
	l := testIPBackend(t, newFakeLinks())
	ip := "100.100.61.4"
	if err := requestSpecific(l, ip); err != nil {
		t.Fatal(err)
	}
	l.releaseContainerIP(l.Logger, "job-1", net.ParseIP(ip).To4(), nil)
	if err := requestSpecific(l, ip); err != ipallocator.ErrIPAlreadyAllocated {
		t.Fatalf("got %v, want quarantine", err)
	}
	eventually(t, "address freed after the quarantine", func() bool { return requestSpecific(l, ip) == nil })
}

func downJob(id, ip string, status host.JobStatus, ended time.Time) *host.ActiveJob {
	return &host.ActiveJob{
		Job:        &host.Job{ID: id},
		InternalIP: ip,
		Status:     status,
		EndedAt:    ended,
	}
}

func TestRecentlyReleasedIPs(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 24, 7, 0, time.UTC)
	q := 2 * time.Minute
	hostNet := downJob("hostnet", "100.100.61.9", host.StatusDone, now.Add(-time.Second))
	hostNet.Job.Config.HostNetwork = true
	jobs := map[string]*host.ActiveJob{
		"old-primary": downJob("old-primary", "100.100.61.7", host.StatusDone, now.Add(-38*time.Second)),
		"crashed":     downJob("crashed", "100.100.61.8", host.StatusCrashed, now.Add(-time.Minute)),
		"long-gone":   downJob("long-gone", "100.100.61.10", host.StatusDone, now.Add(-10*time.Minute)),
		"running":     downJob("running", "100.100.61.11", host.StatusRunning, time.Time{}),
		"no-ip":       downJob("no-ip", "", host.StatusFailed, now.Add(-time.Second)),
		"bad-ip":      downJob("bad-ip", "nope", host.StatusFailed, now.Add(-time.Second)),
		"hostnet":     hostNet,
		// the same address used twice: the later end wins
		"reuse-a": downJob("reuse-a", "100.100.61.12", host.StatusDone, now.Add(-90*time.Second)),
		"reuse-b": downJob("reuse-b", "100.100.61.12", host.StatusDone, now.Add(-30*time.Second)),
	}
	got := recentlyReleasedIPs(jobs, now, q)
	want := map[string]time.Time{
		"100.100.61.7":  now.Add(-38 * time.Second).Add(q),
		"100.100.61.8":  now.Add(-time.Minute).Add(q),
		"100.100.61.12": now.Add(-30 * time.Second).Add(q),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for ip, until := range want {
		if !got[ip].Equal(until) {
			t.Fatalf("%s: got %v, want %v", ip, got[ip], until)
		}
	}
}

func TestStrayBridgeIPs(t *testing.T) {
	mac := func(s string) net.HardwareAddr { m, _ := net.ParseMAC(s); return m }
	ports := []bridgePort{
		{Index: 5, Name: "veth22c1"}, // left over from a finished job
		{Index: 6, Name: "veth4685"}, // a restored container's
		{Index: 7, Name: "veth9999"}, // stray, no neighbour entry
	}
	known := map[string]bool{"veth4685": true}
	fdb := []netlink.Neigh{
		{LinkIndex: 5, HardwareAddr: mac("fe:69:01:4e:5d:7e")},
		{LinkIndex: 5, HardwareAddr: mac("aa:aa:aa:aa:aa:05"), State: netlink.NUD_PERMANENT}, // the port's own MAC
		{LinkIndex: 6, HardwareAddr: mac("fe:00:00:00:00:06")},
		{LinkIndex: 7, HardwareAddr: mac("fe:00:00:00:00:07")},
	}
	neigh := []netlink.Neigh{
		{IP: net.ParseIP("100.100.61.7"), HardwareAddr: mac("fe:69:01:4e:5d:7e"), State: 0x08}, // STALE
		{IP: net.ParseIP("100.100.61.6"), HardwareAddr: mac("fe:00:00:00:00:06"), State: 0x02}, // REACHABLE
		{IP: net.ParseIP("100.100.61.5"), HardwareAddr: mac("aa:aa:aa:aa:aa:05"), State: 0x02}, // a port's own MAC
		{IP: net.ParseIP("100.100.61.20"), HardwareAddr: mac("fe:69:01:4e:5d:7e"), State: netlink.NUD_FAILED},
	}
	got, unknown := strayBridgeIPs(ports, known, fdb, neigh)
	if len(got) != 1 || !got[0].IP.Equal(net.ParseIP("100.100.61.7")) || got[0].Port.Name != "veth22c1" {
		t.Fatalf("got %+v, want 100.100.61.7 on veth22c1", got)
	}
	if len(unknown) != 1 || unknown[0].Name != "veth9999" {
		t.Fatalf("ports with no known address: got %+v, want veth9999", unknown)
	}
}

// The incident: the platform postgres primary (100.100.61.7) ended 38 s
// before flynn-host restarted and its veth was still on the bridge. The fresh
// allocator must not hand that address to the first job after the restart,
// neither while the old port is there nor during the quarantine after it goes.
func TestRestoreIPHoldsAfterHostRestart(t *testing.T) {
	setShortIPTimers(t, time.Hour)
	links := newFakeLinks("veth22c1")
	l := testIPBackend(t, links) // .1 to .6 usable
	now := time.Now()
	jobs := map[string]*host.ActiveJob{
		"old-primary": downJob("old-primary", "100.100.61.1", host.StatusDone, now.Add(-38*time.Second)),
		"restored":    downJob("restored", "100.100.61.2", host.StatusRunning, time.Time{}),
	}
	if err := requestSpecific(l, "100.100.61.2"); err != nil { // the restored container's
		t.Fatal(err)
	}
	strays := []strayIP{{IP: net.ParseIP("100.100.61.3").To4(), Port: bridgePort{Index: 100, Name: "veth22c1"}}}

	l.restoreIPHolds(l.Logger, jobs, map[string]bool{"100.100.61.2": true}, strays)

	for i := 0; i < 3; i++ {
		got, err := l.ipalloc.RequestIP(l.bridgeNet, nil)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		switch got.String() {
		case "100.100.61.1":
			t.Fatalf("request %d: handed out an address released 38 s before the restart", i)
		case "100.100.61.2":
			t.Fatalf("request %d: handed out a running container's address", i)
		case "100.100.61.3":
			t.Fatalf("request %d: handed out an address still on a bridge port", i)
		}
	}
	// .4, .5, .6 are now taken. The full pool may fall back to the
	// quarantined .1, but never to .3 while its port exists.
	if got, err := l.ipalloc.RequestIP(l.bridgeNet, nil); err != nil || got.String() != "100.100.61.1" {
		t.Fatalf("full pool: got %v, %v; want the quarantined 100.100.61.1", got, err)
	}
	if got, err := l.ipalloc.RequestIP(l.bridgeNet, nil); err != ipallocator.ErrNoAvailableIPs {
		t.Fatalf("got %v, %v; the stray port's address must stay held", got, err)
	}
	links.vanish("veth22c1")
	// After the port goes, .3 is quarantined, so the full pool can take it.
	eventually(t, "stray address quarantined after its port went away", func() bool {
		got, err := l.ipalloc.RequestIP(l.bridgeNet, nil)
		return err == nil && got.String() == "100.100.61.3"
	})
}

// Real links: needs root (the Docker unit runner is privileged). Deleting
// the host end removes the peer too, and the bridge scan sees the port.
func TestNetlinkLinksDeleteVethPairAndScanBridge(t *testing.T) {
	br := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "f1tbr0"}}
	if err := netlink.LinkAdd(br); err != nil {
		t.Skipf("cannot create links here: %v", err)
	}
	defer netlink.LinkDel(br)
	veth := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "f1tveth"}, PeerName: "f1tpeer"}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Skipf("cannot create veth here: %v", err)
	}
	defer deleteLinkByName("f1tveth")
	bridge, err := netlink.LinkByName("f1tbr0")
	if err != nil {
		t.Fatal(err)
	}
	hostEnd, err := netlink.LinkByName("f1tveth")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetMaster(hostEnd, bridge.(*netlink.Bridge)); err != nil {
		t.Fatal(err)
	}
	ports, _, _, err := scanBridgePorts(bridge)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 || ports[0].Name != "f1tveth" {
		t.Fatalf("bridge ports: got %+v, want [f1tveth]", ports)
	}

	links := netlinkLinks{}
	index, ok := links.Lookup("f1tveth")
	if !ok || !links.Present(index, "f1tveth") {
		t.Fatal("veth not found")
	}
	if links.Present(index, "f1tother") {
		t.Fatal("Present must match the name as well as the index")
	}
	if err := links.Delete(index, "f1tveth"); err != nil {
		t.Fatal(err)
	}
	if links.Present(index, "f1tveth") {
		t.Fatal("host end still present after Delete")
	}
	if _, ok := links.Lookup("f1tpeer"); ok {
		t.Fatal("peer still present after deleting the host end")
	}
	if err := links.Delete(index, "f1tveth"); err != nil {
		t.Fatalf("deleting a gone link: %v", err)
	}
}

// Before networking is configured (a restored container that failed to
// reconnect), cleanup only deletes the veth; it reports any it could not.
func TestDeleteHostVethsReportsStuckLinks(t *testing.T) {
	links := newFakeLinks("vethaaaa", "vethbbbb")
	links.stuck["vethbbbb"] = true
	l := testIPBackend(t, links)
	remaining := l.deleteHostVeths(l.Logger, nil, []string{"vethaaaa", "vethbbbb", "vethgone"})
	if len(links.deleted) != 1 || links.deleted[0] != "vethaaaa" {
		t.Fatalf("deleted %q, want [vethaaaa]", links.deleted)
	}
	if len(remaining) != 1 || remaining[0].Name != "vethbbbb" {
		t.Fatalf("remaining %+v, want vethbbbb", remaining)
	}
}
