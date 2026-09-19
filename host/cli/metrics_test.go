package cli

import (
	"strings"
	"testing"

	host "github.com/randy-girard/flynn/host/types"
)

func TestWriteHostMetricsTable(t *testing.T) {
	var b strings.Builder
	err := writeHostMetricsTable(&b, []*host.HostResourceStats{{
		HostID:           "node-a",
		CPUUsagePercent:  12.5,
		MemoryUsedBytes:  4 << 30,
		MemoryTotalBytes: 16 << 30,
		DiskUsedBytes:    40 << 30,
		DiskTotalBytes:   100 << 30,
		LoadAvg1:         0.5,
		RunningJobsCount: 8,
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{"node-a", "12.5%", "4.0GiB/16.0GiB", "40%", "0.50", "8"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
}

func TestFormatBytesIEC(t *testing.T) {
	if got := formatBytesIEC(512); got != "512B" {
		t.Fatalf("got %s", got)
	}
	if got := formatBytesIEC(1536); !strings.Contains(got, "KiB") {
		t.Fatalf("got %s", got)
	}
}
