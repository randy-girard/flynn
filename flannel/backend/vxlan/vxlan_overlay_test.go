package vxlan

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/randy-girard/flynn/flannel/subnet"
)

func TestVXLANAssignsSlash32OnFlannelDevice(t *testing.T) {
	src, err := os.ReadFile("vxlan.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "PrefixLen: 32") {
		t.Fatal("flannel.1 must be assigned a /32; a /16 overlay prefix makes peer container IPs look local and drops VXLAN frames")
	}
}

func TestVXLANWatchdogRestoresVTEPMAC(t *testing.T) {
	src, err := os.ReadFile("vxlan.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "macCheck := time.NewTicker(macCheckInterval)") {
		t.Fatal("Run() must poll EnsureMAC so udev MACAddressPolicy rewrites are repaired")
	}
	if !strings.Contains(body, "vb.dev.EnsureMAC(vb.vtepMAC)") {
		t.Fatal("Init/Run must call EnsureMAC with the advertised VTEP MAC")
	}
}

func TestVXLANReconcilesPeerOverlay(t *testing.T) {
	src, err := os.ReadFile("vxlan.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "vb.reconcilePeerOverlay()") {
		t.Fatal("Run must re-apply peer FDB/L3/route so a missed Watch or missing ARP neigh cannot blackhole overlay after crash-reload")
	}
	if !strings.Contains(body, "vb.sm.PeerLeases()") {
		t.Fatal("reconcile must walk SubnetManager.PeerLeases")
	}
}

func TestStallReason(t *testing.T) {
	now := time.Now()
	timeout := time.Minute
	cases := []struct {
		name          string
		loop, renewer time.Time
		stalled       bool
	}{
		{"both fresh", now, now, false},
		{"not started", time.Time{}, time.Time{}, false},
		{"loop wedged", now.Add(-2 * timeout), now, true},
		{"renewer wedged", now, now.Add(-2 * timeout), true},
	}
	for _, tc := range cases {
		if got := stallReason(now, tc.loop, tc.renewer, timeout); (got != "") != tc.stalled {
			t.Fatalf("%s: reason=%q, stalled want %v", tc.name, got, tc.stalled)
		}
	}
}

func TestWatchStallsFiresOnWedgedLoop(t *testing.T) {
	oldTimeout, oldInterval, oldOnStall := stallTimeout, stallCheckInterval, onStall
	defer func() { stallTimeout, stallCheckInterval, onStall = oldTimeout, oldInterval, oldOnStall }()
	stallTimeout = 50 * time.Millisecond
	stallCheckInterval = 10 * time.Millisecond
	fired := make(chan string, 1)
	onStall = func(reason string) { fired <- reason }

	vb := &VXLANBackend{sm: &subnet.SubnetManager{}, stop: make(chan bool)}
	defer close(vb.stop)
	var loopAlive atomic.Int64
	loopAlive.Store(time.Now().Add(-time.Hour).UnixNano())
	go vb.watchStalls(&loopAlive)

	select {
	case r := <-fired:
		if !strings.Contains(r, "VXLAN loop") {
			t.Fatalf("reason=%q", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not fire for a wedged VXLAN loop")
	}
}
