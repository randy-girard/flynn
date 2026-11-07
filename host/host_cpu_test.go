package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestHostCPUPercentFromProcStat(t *testing.T) {
	// guest is already inside user, so it must not be added again.
	// 100 ticks of user and 100 ticks of idle -> 50%.
	prev := []byte("cpu  100 0 0 100 0 0 0 0 100 0\n")
	next := []byte("cpu  200 0 0 200 0 0 0 0 200 0\n")
	a, err := parseHostCPUStat(prev)
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseHostCPUStat(next)
	if err != nil {
		t.Fatal(err)
	}
	got := hostCPUPercent(a, b)
	if math.Abs(got-50) > 0.01 {
		t.Fatalf("cpu percent = %v, want 50", got)
	}
}

func TestGetHostStatsReportsHostCPU(t *testing.T) {
	if _, err := os.Stat("/proc/meminfo"); err != nil {
		t.Skip(err.Error())
	}
	dir := t.TempDir()
	statPath := filepath.Join(dir, "stat")
	writeStat := func(body string) {
		t.Helper()
		if err := os.WriteFile(statPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeStat("cpu  10 0 0 90 0 0 0 0 0 0\n")
	old := procStatPath
	procStatPath = statPath
	t.Cleanup(func() { procStatPath = old })

	l := &LibcontainerBackend{
		host:       &Host{id: "node1"},
		containers: map[string]*Container{},
	}
	first, err := l.GetHostStats()
	if err != nil {
		t.Fatal(err)
	}
	if first.CPUUsagePercent != 0 {
		t.Fatalf("first sample = %v, want 0 until a previous /proc/stat exists", first.CPUUsagePercent)
	}
	if first.CPUCount < 1 {
		t.Fatalf("cpu_count = %d", first.CPUCount)
	}
	// +50 user, +50 idle -> 50% busy. A host that never reads /proc/stat stays at 0.
	writeStat("cpu  60 0 0 140 0 0 0 0 0 0\n")
	second, err := l.GetHostStats()
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(second.CPUUsagePercent-50) > 0.01 {
		t.Fatalf("cpu_usage_percent = %v, want 50 (host stats left CPU at 0)", second.CPUUsagePercent)
	}
	if second.HostID != "node1" {
		t.Fatalf("host_id = %q", second.HostID)
	}
}
