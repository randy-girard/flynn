package postgresql

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx"
	host "github.com/randy-girard/flynn/host/types"
)

// metricsSnapshotSQL matches the postgres plugin instance probe, plus a count
// of active queries running longer than 500ms.
const metricsSnapshotSQL = `SELECT concat_ws('|',
  COALESCE((SELECT SUM(pg_database_size(oid)) FROM pg_database WHERE datallowconn),0),
  COALESCE((SELECT COUNT(*) FROM pg_stat_user_tables),0),
  COALESCE((SELECT COUNT(*) FROM pg_stat_activity WHERE state IS DISTINCT FROM 'idle' AND pid <> pg_backend_pid()),0),
  COALESCE((SELECT COUNT(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'),0),
  COALESCE((SELECT setting::int FROM pg_settings WHERE name = 'max_connections'),0),
  COALESCE((SELECT CASE WHEN SUM(idx_blks_hit+idx_blks_read)=0 THEN 1 ELSE SUM(idx_blks_hit)::float/SUM(idx_blks_hit+idx_blks_read) END FROM pg_statio_user_indexes),1),
  COALESCE((SELECT CASE WHEN SUM(heap_blks_hit+heap_blks_read)=0 THEN 1 ELSE SUM(heap_blks_hit)::float/SUM(heap_blks_hit+heap_blks_read) END FROM pg_statio_user_tables),1),
  COALESCE((SELECT SUM(xact_commit) FROM pg_stat_database),0),
  COALESCE((SELECT SUM(wal_bytes) FROM pg_stat_wal),0),
  COALESCE((SELECT CASE WHEN pg_is_in_recovery() THEN 0 ELSE pg_current_xact_id()::text::bigint END),0),
  COALESCE((SELECT CASE WHEN pg_is_in_recovery() THEN EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp())) ELSE 0 END),0),
  COALESCE((SELECT CASE WHEN pg_is_in_recovery() THEN pg_wal_lsn_diff(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn()) ELSE 0 END),0),
  COALESCE((SELECT COUNT(*) FROM pg_stat_activity WHERE state = 'active' AND pid <> pg_backend_pid() AND query_start < now() - interval '500 milliseconds' AND query NOT ILIKE '%pg_stat%'),0)
)`

var metricsSnapshotKeys = []string{
	"db_size_bytes",
	"tables",
	"active_connections",
	"waiting_connections",
	"max_connections",
	"index_cache_hit_rate",
	"table_cache_hit_rate",
	"xact_commit",
	"wal_bytes",
	"current_transaction",
	"replay_lag_seconds",
	"follower_lag_bytes",
	"slow_query_count",
}

// metricsQueryTimeout stays under flynn-host's 2s fragment timeout.
var metricsQueryTimeout = 1500 * time.Millisecond

// MetricsSeries runs the snapshot on a dedicated connection so a slow probe
// never holds the single sirenia pool connection.
func (p *Process) MetricsSeries() map[string]float64 {
	down := map[string]float64{"service_available": 0}
	if !p.running() {
		return down
	}
	portNum, _ := strconv.Atoi(p.port)
	conn, err := pgx.Connect(pgx.ConnConfig{
		Host: "127.0.0.1",
		User: "postgres",
		Port: uint16(portNum),
		Dial: func(network, addr string) (net.Conn, error) {
			c, err := net.DialTimeout(network, addr, metricsQueryTimeout)
			if err != nil {
				return nil, err
			}
			return c, c.SetDeadline(time.Now().Add(metricsQueryTimeout))
		},
	})
	if err != nil {
		return down
	}
	defer conn.Close()
	var raw string
	if err := conn.QueryRow(metricsSnapshotSQL).Scan(&raw); err != nil {
		return down
	}
	series, ok := ParseMetricsSnapshot(raw)
	if !ok {
		return down
	}
	series["service_available"] = 1
	return series
}

// ParseMetricsSnapshot parses the pipe-separated metricsSnapshotSQL row.
func ParseMetricsSnapshot(raw string) (map[string]float64, bool) {
	parts := strings.Split(strings.TrimSpace(raw), "|")
	if len(parts) != len(metricsSnapshotKeys) {
		return nil, false
	}
	series := make(map[string]float64, len(parts)+1)
	for i, key := range metricsSnapshotKeys {
		n, err := strconv.ParseFloat(strings.TrimSpace(parts[i]), 64)
		if err != nil {
			return nil, false
		}
		series[key] = n
	}
	return series, true
}

// FormatMetricsLine is the flynn-postgres fragment flynn-host appends to the
// job's minute metrics line. Keys missing from series are omitted so a failed
// probe does not chart as zero.
func FormatMetricsLine(source string, series map[string]float64) string {
	if source == "" {
		source = "postgres"
	}
	parts := []string{"flynn-postgres", "source=" + source, "addon=" + source}
	for _, s := range []struct{ key, format string }{
		{"service_available", "sample#service-available=%.0f"},
		{"db_size_bytes", "sample#db_size=%.0fbytes"},
		{"tables", "sample#tables=%.0f"},
		{"active_connections", "sample#active-connections=%.0f"},
		{"waiting_connections", "sample#waiting-connections=%.0f"},
		{"max_connections", "sample#max-connections=%.0f"},
		{"index_cache_hit_rate", "sample#index-cache-hit-rate=%.5f"},
		{"table_cache_hit_rate", "sample#table-cache-hit-rate=%.5f"},
		{"current_transaction", "sample#current_transaction=%.0f"},
		{"xact_commit", "sample#xact-commit=%.0f"},
		{"wal_bytes", "sample#wal-bytes=%.0f"},
		{"follower_lag_bytes", "sample#follower-lag-bytes=%.0f"},
		{"replay_lag_seconds", "sample#replay-lag-seconds=%.3f"},
		{"slow_query_count", "sample#slow-queries=%.0f"},
	} {
		if v, ok := series[s.key]; ok {
			parts = append(parts, fmt.Sprintf(s.format, v))
		}
	}
	return strings.Join(parts, " ")
}

// MetricsHandler serves only GET /metrics/log. It is mounted on its own
// $FLYNN_METRICS_PORT listener because the main API requires the cluster key.
func MetricsHandler(source string, series func() map[string]float64) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(host.MetricsLogPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, FormatMetricsLine(source, series())+"\n")
	})
	return mux
}
