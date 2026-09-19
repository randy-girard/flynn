package host

import (
	"fmt"
	"math"
	"strings"
)

// FormatContainerMetricsLog is one app-log line of container usage.
// Keys are metric=value so operators can grep/cut without JSON.
func FormatContainerMetricsLog(s *ContainerStats) string {
	if s == nil {
		return ""
	}
	limit := s.MemorySoftLimitBytes
	if limit == 0 {
		limit = s.MemoryLimitBytes
	}
	memPct := 0.0
	if limit > 0 {
		memPct = 100 * float64(s.MemoryUsageBytes) / float64(limit)
	}
	fields := []string{
		fmt.Sprintf("cpu_percent=%s", formatMetricFloat(s.CPUUsagePercent)),
		fmt.Sprintf("memory_bytes=%d", s.MemoryUsageBytes),
		fmt.Sprintf("memory_limit_bytes=%d", limit),
		fmt.Sprintf("memory_percent=%s", formatMetricFloat(memPct)),
		fmt.Sprintf("net_rx_bytes=%d", s.NetworkRxBytes),
		fmt.Sprintf("net_tx_bytes=%d", s.NetworkTxBytes),
		fmt.Sprintf("io_read_bytes=%d", s.IOReadBytes),
		fmt.Sprintf("io_write_bytes=%d", s.IOWriteBytes),
		fmt.Sprintf("pids=%d", s.PIDsCurrent),
	}
	return "metrics " + strings.Join(fields, " ")
}

func formatMetricFloat(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "0"
	}
	return strconvTrimFloat(v)
}

func strconvTrimFloat(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}
