package main

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	host "github.com/randy-girard/flynn/host/types"
)

func TestFillDiskStatsReportsThisMachine(t *testing.T) {
	stats := &host.HostResourceStats{}
	if err := fillDiskStats(stats); err != nil {
		t.Fatal(err)
	}
	if stats.DiskPath != hostRootFS {
		t.Fatalf("disk_path=%q want %q", stats.DiskPath, hostRootFS)
	}
	if stats.DiskTotalBytes == 0 {
		t.Fatal("expected non-zero total bytes")
	}
	if stats.DiskUsedBytes+stats.DiskFreeBytes == 0 {
		t.Fatal("expected used or free bytes")
	}
}

func TestFillDiskStatsUsesHostRoot(t *testing.T) {
	stats := &host.HostResourceStats{}
	if err := fillDiskStats(stats); err != nil {
		t.Fatal(err)
	}
	if stats.DiskPath != "/" {
		t.Fatalf("host disk must be the whole server root, got %q", stats.DiskPath)
	}
}

func TestDiskLow(t *testing.T) {
	cases := []struct {
		name string
		s    host.HostResourceStats
		want bool
	}{
		{name: "empty", want: false},
		{
			name: "healthy",
			s:    host.HostResourceStats{DiskTotalBytes: 100 << 30, DiskUsedBytes: 40 << 30, DiskFreeBytes: 60 << 30},
			want: false,
		},
		{
			name: "90 percent used",
			s:    host.HostResourceStats{DiskTotalBytes: 100 << 30, DiskUsedBytes: 90 << 30, DiskFreeBytes: 10 << 30},
			want: true,
		},
		{
			name: "under 1GiB free",
			s:    host.HostResourceStats{DiskTotalBytes: 20 << 30, DiskUsedBytes: 19 << 30, DiskFreeBytes: 800 << 20},
			want: true,
		},
		{
			name: "tiny fs ignored for min-free",
			s:    host.HostResourceStats{DiskTotalBytes: 200 << 20, DiskUsedBytes: 100 << 20, DiskFreeBytes: 100 << 20},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := diskLow(&tc.s); got != tc.want {
				t.Fatalf("diskLow = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShouldReclaimImageData(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	healthy := &host.HostResourceStats{DiskTotalBytes: 100 << 30, DiskUsedBytes: 40 << 30, DiskFreeBytes: 60 << 30}
	low := &host.HostResourceStats{DiskTotalBytes: 20 << 30, DiskUsedBytes: 19 << 30, DiskFreeBytes: 800 << 20}
	if !shouldReclaimImageData(healthy, time.Time{}, now) {
		t.Fatal("first watch must reclaim leftover image data")
	}
	if shouldReclaimImageData(healthy, now.Add(-time.Minute), now) {
		t.Fatal("healthy disk must wait for the cleanup interval")
	}
	if !shouldReclaimImageData(healthy, now.Add(-imageCleanupInterval), now) {
		t.Fatal("elapsed interval must reclaim even when the disk is healthy")
	}
	if !shouldReclaimImageData(low, now.Add(-time.Minute), now) {
		t.Fatal("low disk must reclaim without waiting for the interval")
	}
	if !shouldReclaimImageData(nil, time.Time{}, now) {
		t.Fatal("periodic reclaim must run even without stats")
	}
}

type scriptedStatsBackend struct {
	MockBackend
	stats []*host.HostResourceStats
	err   error
	calls int
}

func (b *scriptedStatsBackend) GetHostStats() (*host.HostResourceStats, error) {
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	if len(b.stats) == 0 {
		return nil, nil
	}
	i := b.calls - 1
	if i >= len(b.stats) {
		i = len(b.stats) - 1
	}
	return b.stats[i], nil
}

func TestCheckHostDiskReclaimsWhenLow(t *testing.T) {
	low := &host.HostResourceStats{DiskPath: "/", DiskTotalBytes: 20 << 30, DiskUsedBytes: 19 << 30, DiskFreeBytes: 800 << 20}
	healthy := &host.HostResourceStats{DiskPath: "/", DiskTotalBytes: 20 << 30, DiskUsedBytes: 10 << 30, DiskFreeBytes: 10 << 30}
	cleaned := 0
	d := NewWebhookDispatcher("node-a", nil, log15.New())
	h := &Host{
		backend:           &scriptedStatsBackend{stats: []*host.HostResourceStats{low, healthy}},
		webhookDispatcher: d,
		lastImageCleanup:  time.Now(),
		imageCleanup:      func() error { cleaned++; return nil },
		log:               log15.New(),
	}
	h.checkHostDisk()
	if cleaned != 1 {
		t.Fatalf("cleanup calls=%d want 1", cleaned)
	}
	if n := len(d.events); n != 0 {
		t.Fatalf("events=%d want 0 after reclaim recovered space", n)
	}
}

func TestCheckHostDiskAlertsIfStillFull(t *testing.T) {
	full := &host.HostResourceStats{DiskPath: "/", DiskTotalBytes: 20 << 30, DiskUsedBytes: 20<<30 - 100<<20, DiskFreeBytes: 100 << 20}
	cleaned := 0
	d := NewWebhookDispatcher("node-a", nil, log15.New())
	h := &Host{
		backend:           &scriptedStatsBackend{stats: []*host.HostResourceStats{full, full}},
		webhookDispatcher: d,
		lastImageCleanup:  time.Now(),
		imageCleanup:      func() error { cleaned++; return nil },
		log:               log15.New(),
	}
	h.checkHostDisk()
	if cleaned != 1 {
		t.Fatalf("cleanup calls=%d want 1", cleaned)
	}
	if n := len(d.events); n != 1 {
		t.Fatalf("events=%d want 1 when disk stays full", n)
	}
}

func TestCheckHostDiskPeriodicCleanup(t *testing.T) {
	healthy := &host.HostResourceStats{DiskPath: "/", DiskTotalBytes: 100 << 30, DiskUsedBytes: 40 << 30, DiskFreeBytes: 60 << 30}
	cleaned := 0
	h := &Host{
		backend:          &scriptedStatsBackend{stats: []*host.HostResourceStats{healthy, healthy}},
		lastImageCleanup: time.Now().Add(-imageCleanupInterval),
		imageCleanup:     func() error { cleaned++; return nil },
		log:              log15.New(),
	}
	h.checkHostDisk()
	if cleaned != 1 {
		t.Fatalf("cleanup calls=%d want 1", cleaned)
	}
}

func TestCheckHostDiskSkipsRecentCleanupWhenHealthy(t *testing.T) {
	healthy := &host.HostResourceStats{DiskPath: "/", DiskTotalBytes: 100 << 30, DiskUsedBytes: 40 << 30, DiskFreeBytes: 60 << 30}
	cleaned := 0
	h := &Host{
		backend:          &scriptedStatsBackend{stats: []*host.HostResourceStats{healthy}},
		lastImageCleanup: time.Now(),
		imageCleanup:     func() error { cleaned++; return nil },
		log:              log15.New(),
	}
	h.checkHostDisk()
	if cleaned != 0 {
		t.Fatalf("cleanup calls=%d want 0", cleaned)
	}
}

func TestCheckHostDiskAlertsWhenCleanupFails(t *testing.T) {
	full := &host.HostResourceStats{DiskPath: "/", DiskTotalBytes: 20 << 30, DiskUsedBytes: 20<<30 - 100<<20, DiskFreeBytes: 100 << 20}
	d := NewWebhookDispatcher("node-a", nil, log15.New())
	last := time.Now()
	h := &Host{
		backend:           &scriptedStatsBackend{stats: []*host.HostResourceStats{full, full}},
		webhookDispatcher: d,
		lastImageCleanup:  last,
		imageCleanup:      func() error { return errors.New("remove failed") },
		log:               log15.New(),
	}
	h.checkHostDisk()
	if n := len(d.events); n != 1 {
		t.Fatalf("events=%d want 1 when cleanup fails and disk is full", n)
	}
	if !h.lastImageCleanup.Equal(last) {
		t.Fatal("failed cleanup must not advance lastImageCleanup")
	}
}

func TestDiskOutOfSpace(t *testing.T) {
	cases := []struct {
		name string
		s    host.HostResourceStats
		want bool
	}{
		{name: "empty", want: false},
		{
			name: "healthy",
			s:    host.HostResourceStats{DiskTotalBytes: 100 << 30, DiskUsedBytes: 40 << 30, DiskFreeBytes: 60 << 30},
			want: false,
		},
		{
			name: "zero free",
			s:    host.HostResourceStats{DiskTotalBytes: 100 << 30, DiskUsedBytes: 100 << 30, DiskFreeBytes: 0},
			want: true,
		},
		{
			name: "98 percent used",
			s:    host.HostResourceStats{DiskTotalBytes: 100 << 30, DiskUsedBytes: 98 << 30, DiskFreeBytes: 2 << 30},
			want: true,
		},
		{
			name: "under 256MiB free",
			s:    host.HostResourceStats{DiskTotalBytes: 20 << 30, DiskUsedBytes: 20<<30 - 100<<20, DiskFreeBytes: 100 << 20},
			want: true,
		},
		{
			name: "tiny fs ignored for min-free",
			s:    host.HostResourceStats{DiskTotalBytes: 200 << 20, DiskUsedBytes: 100 << 20, DiskFreeBytes: 100 << 20},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := diskOutOfSpace(&tc.s); got != tc.want {
				t.Fatalf("diskOutOfSpace = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsNoSpaceErr(t *testing.T) {
	if isNoSpaceErr(nil) {
		t.Fatal("nil")
	}
	if !isNoSpaceErr(syscall.ENOSPC) {
		t.Fatal("ENOSPC")
	}
	if !isNoSpaceErr(errors.New("write layer: no space left on device")) {
		t.Fatal("message")
	}
	if isNoSpaceErr(errors.New("permission denied")) {
		t.Fatal("other error")
	}
}

func TestSendDiskFullCooldown(t *testing.T) {
	d := NewWebhookDispatcher("node-a", nil, log15.New())
	d.SendDiskFull("Host disk out of space", "", nil, map[string]string{"disk_path": "/"})
	d.SendDiskFull("Host disk out of space", "", nil, nil)
	if got := len(d.events); got != 1 {
		t.Fatalf("events=%d want 1", got)
	}
	ev := <-d.events
	if ev.Code != host.CodeDiskFull {
		t.Fatalf("code %s", ev.Code)
	}
	if ev.Severity != host.SeverityCritical {
		t.Fatalf("severity %s", ev.Severity)
	}
	if ev.HostID != "node-a" || ev.Metadata["disk_path"] != "/" {
		t.Fatalf("event %+v", ev)
	}
}

func TestApplyStatfsUsesBavailForFree(t *testing.T) {
	stats := &host.HostResourceStats{}
	applyStatfs(stats, "/", syscall.Statfs_t{
		Blocks: 1000,
		Bfree:  200,
		Bavail: 150,
		Bsize:  4096,
	})
	if stats.DiskPath != "/" {
		t.Fatalf("path %q", stats.DiskPath)
	}
	if stats.DiskTotalBytes != 1000*4096 {
		t.Fatalf("total %d", stats.DiskTotalBytes)
	}
	if stats.DiskFreeBytes != 150*4096 {
		t.Fatalf("free %d", stats.DiskFreeBytes)
	}
	if stats.DiskUsedBytes != 800*4096 {
		t.Fatalf("used %d", stats.DiskUsedBytes)
	}
}
