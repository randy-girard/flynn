package host

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// MetricsPortEnv is the job env var naming the container port that serves
// MetricsLogPath. Resource plugins set it on instance jobs so flynn-host can
// append resource samples to the job's minute metrics line.
const MetricsPortEnv = "FLYNN_METRICS_PORT"

// MetricsLogPath returns one text/plain line of resource fields, for example
// `flynn-postgres source=postgresql-swamp-20980 addon=d65696 sample#tables=11`.
const MetricsLogPath = "/metrics/log"

// MetricsLogInterval is how often flynn-host writes the metrics line. Lines
// are written on the minute boundary so every job reports at the same time.
const MetricsLogInterval = time.Minute

const metricsFragmentMaxBytes = 4096

// NextMetricsTick is the wait from now until the next minute boundary.
func NextMetricsTick(now time.Time) time.Duration {
	return now.Truncate(MetricsLogInterval).Add(MetricsLogInterval).Sub(now)
}

// FetchMetricsFragment reads the plugin fragment from a job's metrics port.
// It returns "" when the job has no port or the request fails.
func FetchMetricsFragment(client *http.Client, ip, port string) string {
	ip, port = strings.TrimSpace(ip), strings.TrimSpace(port)
	if client == nil || ip == "" || port == "" {
		return ""
	}
	res, err := client.Get("http://" + net.JoinHostPort(ip, port) + MetricsLogPath)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ""
	}
	line, _ := bufio.NewReader(io.LimitReader(res.Body, metricsFragmentMaxBytes)).ReadString('\n')
	return sanitizeMetricsFragment(line)
}

func sanitizeMetricsFragment(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// FormatJobMetricsLog is the single minute line: container usage followed by
// the plugin fragment when the job serves one.
func FormatJobMetricsLog(s *ContainerStats, fragment string) string {
	line := FormatContainerMetricsLog(s)
	if line == "" {
		return ""
	}
	if fragment = sanitizeMetricsFragment(fragment); fragment != "" {
		line += " " + fragment
	}
	return line
}

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
