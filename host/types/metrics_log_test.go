package host

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNextMetricsTickIsMinuteBoundary(t *testing.T) {
	now := time.Date(2026, 10, 11, 15, 56, 42, 500e6, time.UTC)
	if got := NextMetricsTick(now); got != 17500*time.Millisecond {
		t.Fatalf("got %s", got)
	}
	if got := NextMetricsTick(now.Truncate(time.Minute)); got != time.Minute {
		t.Fatalf("on the boundary wait a full minute, got %s", got)
	}
}

func TestFetchMetricsFragmentReadsOneLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != MetricsLogPath {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("flynn-postgres source=pg-1 addon=r1\tsample#tables=11\nsecond line\n"))
	}))
	defer srv.Close()
	ip, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	got := FetchMetricsFragment(srv.Client(), ip, port)
	if got != "flynn-postgres source=pg-1 addon=r1 sample#tables=11" {
		t.Fatalf("got %q", got)
	}
	if FetchMetricsFragment(srv.Client(), ip, "") != "" {
		t.Fatal("no port means no fragment")
	}
}

func TestFetchMetricsFragmentIgnoresErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	ip, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if got := FetchMetricsFragment(srv.Client(), ip, port); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatJobMetricsLogIsOneLine(t *testing.T) {
	stats := &ContainerStats{CPUUsagePercent: 1, MemoryUsageBytes: 2, MemoryLimitBytes: 4}
	line := FormatJobMetricsLog(stats, "flynn-redis source=r1 addon=a1 sample#keys=7\n")
	if !strings.HasPrefix(line, "metrics cpu_percent=1 ") || !strings.HasSuffix(line, " flynn-redis source=r1 addon=a1 sample#keys=7") {
		t.Fatalf("got %q", line)
	}
	if strings.ContainsAny(line, "\n\r\t") {
		t.Fatalf("must be one line: %q", line)
	}
	if FormatJobMetricsLog(stats, "") != FormatContainerMetricsLog(stats) {
		t.Fatal("without a fragment the line is container usage only")
	}
}

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
