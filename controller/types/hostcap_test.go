package types

import (
	"strings"
	"testing"
)

func TestDefaultHostMaxProcessesUsesSmallestRuntime(t *testing.T) {
	const gib = int64(1 << 30)
	const mib = int64(1 << 20)
	// 8 GiB and a 2 GiB runtime is 4, not the 16 a 512 MiB slot would allow.
	if got := DefaultHostMaxProcesses(8*gib, []int64{2 * gib, 8 * gib}, 253); got != 4 {
		t.Fatalf("largest-catalog slot: got %d want 4", got)
	}
	if got := DefaultHostMaxProcesses(8*gib, []int64{2 * gib, 128 * mib, 4 * gib}, 253); got != 64 {
		t.Fatalf("custom 128MiB must be the slot: got %d want 64", got)
	}
	if got := DefaultHostMaxProcesses(8*gib, []int64{512 * mib, 1 * gib}, 0); got != 16 {
		t.Fatalf("no overlay ceiling: got %d want 16", got)
	}
}

func TestDefaultHostMaxProcessesCapsAtOverlay(t *testing.T) {
	const gib = int64(1 << 30)
	if got := DefaultHostMaxProcesses(64*gib, []int64{1 << 20}, 253); got != 253 {
		t.Fatalf("overlay ceiling: got %d want 253", got)
	}
	if OverlayContainerAddrs(24) != 253 {
		t.Fatalf("/24 addresses: got %d want 253", OverlayContainerAddrs(24))
	}
	if got := DefaultOverlayContainerAddrs(); got != 253 {
		t.Fatalf("default overlay: got %d want 253", got)
	}
	n, ok := OverlayContainerAddrsFromCIDR("100.64.1.1/24")
	if !ok || n != 253 {
		t.Fatalf("cidr: %d ok=%t", n, ok)
	}
	if _, ok := OverlayContainerAddrsFromCIDR("not-a-cidr"); ok {
		t.Fatal("bad cidr")
	}
}

func TestDefaultHostMaxProcessesEdges(t *testing.T) {
	const gib = int64(1 << 30)
	if got := DefaultHostMaxProcesses(0, []int64{gib}, 253); got != 0 {
		t.Fatalf("no memory: %d", got)
	}
	if got := DefaultHostMaxProcesses(8*gib, nil, 253); got != 0 {
		t.Fatalf("no runtimes: %d", got)
	}
	if got := DefaultHostMaxProcesses(8*gib, []int64{0, -1}, 253); got != 0 {
		t.Fatalf("non-positive runtimes: %d", got)
	}
	// A runtime larger than the host still allows one tenant job.
	if got := DefaultHostMaxProcesses(gib, []int64{4 * gib}, 253); got != 1 {
		t.Fatalf("runtime larger than host: got %d want 1", got)
	}
}

func TestParseMemTotal(t *testing.T) {
	got, err := ParseMemTotal(strings.NewReader("MemFree: 1 kB\nMemTotal: 2048 kB\n"))
	if err != nil || got != 2048*1024 {
		t.Fatalf("got %d err %v", got, err)
	}
	if _, err := ParseMemTotal(strings.NewReader("MemFree: 1 kB\n")); err == nil {
		t.Fatal("missing MemTotal")
	}
}

func TestParseHostMaxProcessesAutoIsNotACap(t *testing.T) {
	tags := map[string]string{FormationMaxProcessesTag: HostMaxProcessesAuto}
	if _, ok := ParseHostMaxProcesses(tags); ok {
		t.Fatal("auto must not be a numeric cap")
	}
}
