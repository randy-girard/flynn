package vxlan

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/golang/glog"
	"github.com/randy-girard/flynn/flannel/backend"
	"github.com/randy-girard/flynn/flannel/pkg/ip"
	"github.com/randy-girard/flynn/flannel/pkg/task"
	"github.com/randy-girard/flynn/flannel/subnet"
)

const (
	defaultVNI = 1
)

type VXLANBackend struct {
	sm     *subnet.SubnetManager
	rawCfg json.RawMessage
	cfg    struct {
		VNI  int
		Port int
	}
	dev  *vxlanDevice
	stop chan bool
	wg   sync.WaitGroup
	// vtepMAC is the MAC published in our lease; the device must keep it.
	vtepMAC net.HardwareAddr
}

// macCheckInterval bounds how long a udev-rewritten VTEP MAC can blackhole the
// overlay before EnsureMAC restores it.
const macCheckInterval = 10 * time.Second

// stallTimeout bounds how long the overlay loop or the lease renewer may make
// no progress before flanneld dumps every goroutine and exits so flynn-host
// restarts it. A wedged dev-node1 flanneld once stopped republishing its
// lease and reconciling peers for 20 minutes, blackholing overlay to it.
var (
	stallTimeout       = 3 * time.Minute
	stallCheckInterval = 30 * time.Second
	onStall            = dumpGoroutinesAndExit
)

func New(sm *subnet.SubnetManager, config json.RawMessage) backend.Backend {
	vb := &VXLANBackend{
		sm:     sm,
		rawCfg: config,
		stop:   make(chan bool),
	}
	vb.cfg.VNI = defaultVNI

	return vb
}

func newSubnetAttrs(pubIP net.IP, httpPort string, mac net.HardwareAddr) (*subnet.LeaseAttrs, error) {
	data, err := json.Marshal(&vxlanLeaseAttrs{hardwareAddr(mac)})
	if err != nil {
		return nil, err
	}

	return &subnet.LeaseAttrs{
		PublicIP:    ip.FromIP(pubIP),
		HTTPPort:    httpPort,
		BackendType: "vxlan",
		BackendData: json.RawMessage(data),
	}, nil
}

func (vb *VXLANBackend) Init(extIface *net.Interface, extIP net.IP, httpPort string, ipMasq bool) (*backend.SubnetDef, error) {
	// Parse our configuration
	if len(vb.rawCfg) > 0 {
		if err := json.Unmarshal(vb.rawCfg, &vb.cfg); err != nil {
			return nil, fmt.Errorf("error decoding UDP backend config: %v", err)
		}
	}

	devAttrs := vxlanDeviceAttrs{
		vni:       uint32(vb.cfg.VNI),
		name:      fmt.Sprintf("flannel.%v", vb.cfg.VNI),
		vtepIndex: extIface.Index,
		vtepAddr:  extIP,
		vtepPort:  vb.cfg.Port,
	}

	var err error
	vb.dev, err = newVXLANDevice(&devAttrs)
	if err != nil {
		return nil, err
	}

	vb.vtepMAC = append(net.HardwareAddr(nil), vb.dev.MACAddr()...)
	sa, err := newSubnetAttrs(extIP, httpPort, vb.vtepMAC)
	if err != nil {
		return nil, err
	}

	sn, err := vb.sm.AcquireLease(sa, vb.stop)
	if err != nil {
		if err == task.ErrCanceled {
			return nil, err
		} else {
			return nil, fmt.Errorf("failed to acquire lease: %v", err)
		}
	}

	// Assign only a /32 on flannel.1. Using the full overlay prefix (e.g. /16)
	// makes every container IP appear local on flannel.1, so VXLAN-decapsulated
	// packets destined for flynnbr0 containers are delivered on flannel.1 and
	// dropped instead of being forwarded to the bridge.
	vxlanNet := ip.IP4Net{
		IP:        sn.IP,
		PrefixLen: 32,
	}
	if err = vb.dev.Configure(vxlanNet, vb.sm.GetConfig().Network); err != nil {
		return nil, err
	}

	// udev may have already rewritten the MAC between device creation and now.
	if _, err := vb.dev.EnsureMAC(vb.vtepMAC); err != nil {
		log.Error("Error verifying VTEP MAC: ", err)
	}

	return &backend.SubnetDef{Net: sn, MTU: vb.dev.MTU()}, nil
}

func (vb *VXLANBackend) Run() {
	vb.wg.Add(1)
	go func() {
		vb.sm.LeaseRenewer(vb.stop)
		vb.wg.Done()
	}()

	log.Info("Watching for new subnet leases")
	evts := make(chan subnet.EventBatch)
	vb.wg.Add(1)
	go func() {
		vb.sm.WatchLeases(evts, vb.stop)
		vb.wg.Done()
	}()

	defer vb.wg.Wait()

	var loopAlive atomic.Int64
	loopAlive.Store(time.Now().UnixNano())
	vb.wg.Add(1)
	go func() {
		vb.watchStalls(&loopAlive)
		vb.wg.Done()
	}()

	macCheck := time.NewTicker(macCheckInterval)
	defer macCheck.Stop()

	for {
		select {
		case evtBatch := <-evts:
			vb.handleSubnetEvents(evtBatch)
		case <-macCheck.C:
			if _, err := vb.dev.EnsureMAC(vb.vtepMAC); err != nil {
				log.Error("Error verifying VTEP MAC: ", err)
			}
			vb.reconcilePeerOverlay()
		case <-vb.stop:
			return
		}
		loopAlive.Store(time.Now().UnixNano())
	}
}

func unixNanoTime(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// stallReason is non-empty when the overlay loop or lease renewer has not
// made progress within timeout. A zero time means "not started" and is
// never a stall.
func stallReason(now, loopAlive, renewerAlive time.Time, timeout time.Duration) string {
	if !loopAlive.IsZero() && now.Sub(loopAlive) > timeout {
		return fmt.Sprintf("VXLAN loop made no progress for %s", now.Sub(loopAlive).Round(time.Second))
	}
	if !renewerAlive.IsZero() && now.Sub(renewerAlive) > timeout {
		return fmt.Sprintf("lease renewer made no progress for %s", now.Sub(renewerAlive).Round(time.Second))
	}
	return ""
}

func (vb *VXLANBackend) watchStalls(loopAlive *atomic.Int64) {
	t := time.NewTicker(stallCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-vb.stop:
			return
		case now := <-t.C:
			if r := stallReason(now, unixNanoTime(loopAlive.Load()), vb.sm.RenewerAlive(), stallTimeout); r != "" {
				onStall(r)
				return
			}
		}
	}
}

// dumpGoroutinesAndExit writes straight to stderr rather than through glog,
// whose global lock may be what is wedged, then exits non-zero.
func dumpGoroutinesAndExit(reason string) {
	done := make(chan struct{})
	go func() {
		fmt.Fprintf(os.Stderr, "flannel stalled: %s; dumping goroutines and exiting\n", reason)
		pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
	// let flynn-host drain stderr before the job exits
	time.Sleep(2 * time.Second)
	os.Exit(1)
}

func (vb *VXLANBackend) Stop() {
	close(vb.stop)
}

func (vb *VXLANBackend) Name() string {
	return "VXLAN"
}

// So we can make it JSON (un)marshalable
type hardwareAddr net.HardwareAddr

func (hw hardwareAddr) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%q", net.HardwareAddr(hw))), nil
}

func (hw *hardwareAddr) UnmarshalJSON(b []byte) error {
	if len(b) < 2 || b[0] != '"' || b[len(b)-1] != '"' {
		return fmt.Errorf("error parsing hardware addr")
	}

	b = b[1 : len(b)-1]

	mac, err := net.ParseMAC(string(b))
	if err != nil {
		return err
	}

	*hw = hardwareAddr(mac)
	return nil
}

type vxlanLeaseAttrs struct {
	VtepMAC hardwareAddr
}

// reconcilePeerOverlay re-applies FDB, L3 neigh, and overlay routes for every
// known peer. After a crash-style reload Watch can miss a lease (or AddL3 can
// fail once); without this the next-hop stays INCOMPLETE and overlay to that
// /24 is Destination Host Unreachable until flanneld is restarted.
func (vb *VXLANBackend) reconcilePeerOverlay() {
	if vb.sm == nil || vb.dev == nil {
		return
	}
	leases := vb.sm.PeerLeases()
	if len(leases) == 0 {
		return
	}
	batch := make(subnet.EventBatch, 0, len(leases))
	for _, l := range leases {
		batch = append(batch, subnet.Event{Type: subnet.SubnetAdded, Lease: l})
	}
	vb.handleSubnetEvents(batch)
}

func (vb *VXLANBackend) handleSubnetEvents(batch subnet.EventBatch) {
	for _, evt := range batch {
		switch evt.Type {
		case subnet.SubnetAdded:
			log.Info("Subnet added: ", evt.Lease.Network)

			if evt.Lease.Attrs.BackendType != "vxlan" {
				log.Warningf("Ignoring non-vxlan subnet: type=%v", evt.Lease.Attrs.BackendType)
				continue
			}

			var attrs vxlanLeaseAttrs
			if err := json.Unmarshal(evt.Lease.Attrs.BackendData, &attrs); err != nil {
				log.Error("Error decoding subnet lease JSON: ", err)
				continue
			}

			if err := vb.dev.AddL2(neigh{IP: evt.Lease.Attrs.PublicIP, MAC: net.HardwareAddr(attrs.VtepMAC)}); err != nil {
				log.Error("Error adding L2 entry: ", err)
			}
			if err := vb.dev.AddL3(neigh{IP: evt.Lease.Network.IP, MAC: net.HardwareAddr(attrs.VtepMAC)}); err != nil {
				log.Error("Error adding L3 entry: ", err)
			}
			if err := vb.dev.AddRoute(evt.Lease.Network); err != nil {
				log.Error("Error adding route: ", err)
			}

		case subnet.SubnetRemoved:
			log.Info("Subnet removed: ", evt.Lease.Network)

			if evt.Lease.Attrs.BackendType != "vxlan" {
				log.Warningf("Ignoring non-vxlan subnet: type=%v", evt.Lease.Attrs.BackendType)
				continue
			}

			var attrs vxlanLeaseAttrs
			if err := json.Unmarshal(evt.Lease.Attrs.BackendData, &attrs); err != nil {
				log.Error("Error decoding subnet lease JSON: ", err)
				continue
			}

			if err := vb.dev.DelRoute(evt.Lease.Network); err != nil {
				log.Error("Error deleting route: ", err)
			}
			if len(attrs.VtepMAC) > 0 {
				if err := vb.dev.DelL2(neigh{IP: evt.Lease.Attrs.PublicIP, MAC: net.HardwareAddr(attrs.VtepMAC)}); err != nil {
					log.Error("Error deleting L2 entry: ", err)
				}
				if err := vb.dev.DelL3(neigh{IP: evt.Lease.Network.IP, MAC: net.HardwareAddr(attrs.VtepMAC)}); err != nil {
					log.Error("Error deleting L3 entry: ", err)
				}
			}

		default:
			log.Error("Internal error: unknown event type: ", int(evt.Type))
		}
	}
}
