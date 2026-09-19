package host

import (
	"strings"
	"testing"
)

func TestFormatContainerMetricsLog(t *testing.T) {
	if FormatContainerMetricsLog(nil) != "" {
		t.Fatal("nil stats must be empty")
	}
	line := FormatContainerMetricsLog(&ContainerStats{
		CPUUsagePercent:      12.345,
		MemoryUsageBytes:     64 * 1024 * 1024,
		MemorySoftLimitBytes: 512 * 1024 * 1024,
		MemoryLimitBytes:     1024 * 1024 * 1024,
		NetworkRxBytes:       10,
		NetworkTxBytes:       20,
		IOReadBytes:          3,
		IOWriteBytes:         4,
		PIDsCurrent:          7,
	})
	if !strings.HasPrefix(line, "metrics ") {
		t.Fatalf("prefix: %q", line)
	}
	want := []string{
		"cpu_percent=12.35",
		"memory_bytes=67108864",
		"memory_limit_bytes=536870912",
		"memory_percent=12.5",
		"net_rx_bytes=10",
		"net_tx_bytes=20",
		"io_read_bytes=3",
		"io_write_bytes=4",
		"pids=7",
	}
	for _, field := range want {
		if !strings.Contains(line, field) {
			t.Fatalf("missing %q in %q", field, line)
		}
	}
}

func TestFormatContainerMetricsLogUsesHardLimit(t *testing.T) {
	line := FormatContainerMetricsLog(&ContainerStats{
		MemoryUsageBytes: 50,
		MemoryLimitBytes: 100,
	})
	if !strings.Contains(line, "memory_limit_bytes=100") || !strings.Contains(line, "memory_percent=50") {
		t.Fatalf("hard limit: %q", line)
	}
}
