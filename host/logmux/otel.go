package logmux

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	ct "github.com/flynn/flynn/controller/types"
	host "github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/logaggregator/utils"
	"github.com/flynn/flynn/pkg/syslog/rfc5424"
)

const otelExportTimeout = 10 * time.Second

// HostMetrics is the host daemon's stats API. Tests replace it.
type HostMetrics interface {
	GetHostStats() (*host.HostResourceStats, error)
	GetAllJobsStats() (*host.AllJobsStats, error)
}

type OTLPSink struct {
	sm *SinkManager

	id       string
	endpoint string
	headers  map[string]string
	insecure bool
	logs     bool
	metrics  bool
	scope    string
	appID    string

	client *http.Client

	mtx          sync.RWMutex
	cursor       *utils.HostCursor
	shutdownOnce sync.Once
	shutdownCh   chan struct{}
}

func NewOTLPSink(sm *SinkManager, info *SinkInfo) (*OTLPSink, error) {
	cfg := &ct.OTLPSinkConfig{}
	if err := json.Unmarshal(info.Config, cfg); err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil, fmt.Errorf("otel sink endpoint is required")
	}
	if _, err := url.Parse(endpoint); err != nil {
		return nil, fmt.Errorf("invalid otel endpoint: %w", err)
	}
	scope, err := ct.ParseSinkScope(cfg.Scope)
	if err != nil {
		return nil, err
	}
	logs, metrics := cfg.Logs, cfg.Metrics
	if !logs && !metrics {
		logs, metrics = true, true
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = dt.Clone()
	}
	if cfg.Insecure {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		transport.TLSClientConfig.InsecureSkipVerify = true
	}
	return &OTLPSink{
		sm:         sm,
		id:         info.ID,
		endpoint:   endpoint,
		headers:    cfg.Headers,
		insecure:   cfg.Insecure,
		logs:       logs,
		metrics:    metrics,
		scope:      scope,
		appID:      info.AppID,
		client:     &http.Client{Timeout: otelExportTimeout, Transport: transport},
		cursor:     info.Cursor,
		shutdownCh: make(chan struct{}),
	}, nil
}

func (s *OTLPSink) Name() string { return s.endpoint }

func (s *OTLPSink) Info() *SinkInfo {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	config, _ := json.Marshal(ct.OTLPSinkConfig{
		Endpoint: s.endpoint,
		Headers:  s.headers,
		Insecure: s.insecure,
		Logs:     s.logs,
		Metrics:  s.metrics,
		Scope:    s.scope,
	})
	return &SinkInfo{
		ID:     s.id,
		Kind:   ct.SinkKindOTLP,
		Config: config,
		Cursor: s.cursor,
		AppID:  s.appID,
	}
}

func (s *OTLPSink) Connect() error { return nil }

func (s *OTLPSink) Close() {}

func (s *OTLPSink) GetCursor(_ string) (*utils.HostCursor, error) {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	return s.cursor, nil
}

func (s *OTLPSink) Write(m message) error {
	if !s.logs {
		return nil
	}
	appID, system := s.sm.jobMeta(m)
	if !ct.AcceptSinkLog(s.scope, s.appID, appID, system) {
		return nil
	}
	body := otlpLogsJSON(m.Message, appID, system)
	if err := s.post("/v1/logs", body); err != nil {
		return err
	}
	s.mtx.Lock()
	s.cursor = m.HostCursor
	s.mtx.Unlock()
	return nil
}

func (s *OTLPSink) ExportMetrics(stats *host.HostResourceStats, jobs *host.AllJobsStats) error {
	if !s.metrics {
		return nil
	}
	return s.post("/v1/metrics", otlpMetricsJSON(stats, jobs))
}

func (s *OTLPSink) Shutdown() {
	s.shutdownOnce.Do(func() { close(s.shutdownCh) })
}

func (s *OTLPSink) ShutdownCh() chan struct{} { return s.shutdownCh }

func (s *OTLPSink) post(path string, payload []byte) error {
	req, err := http.NewRequest(http.MethodPost, s.endpoint+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("otel %s: HTTP %d %s", path, res.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}

type otlpKV struct {
	Key   string     `json:"key"`
	Value otlpAnyVal `json:"value"`
}

type otlpAnyVal struct {
	StringValue string   `json:"stringValue,omitempty"`
	IntValue    string   `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
}

func strKV(k, v string) otlpKV {
	return otlpKV{Key: k, Value: otlpAnyVal{StringValue: v}}
}

func intKV(k string, v int64) otlpKV {
	return otlpKV{Key: k, Value: otlpAnyVal{IntValue: fmt.Sprintf("%d", v)}}
}

func otlpLogsJSON(msg *rfc5424.Message, appID string, system bool) []byte {
	sev := "INFO"
	if msg != nil && len(msg.Msg) > 0 {
		// rfc5424 severity is in the header; keep a stable label.
	}
	body := ""
	attrs := []otlpKV{
		strKV("service.name", "flynn"),
	}
	if msg != nil {
		body = string(msg.Msg)
		if len(msg.AppName) > 0 {
			attrs = append(attrs, strKV("flynn.app_name", string(msg.AppName)))
		}
		if len(msg.ProcID) > 0 {
			attrs = append(attrs, strKV("flynn.proc_id", string(msg.ProcID)))
		}
	}
	if appID != "" {
		attrs = append(attrs, strKV("flynn.app_id", appID))
	}
	sys := system
	attrs = append(attrs, otlpKV{Key: "flynn.system", Value: otlpAnyVal{BoolValue: &sys}})
	payload := map[string]interface{}{
		"resourceLogs": []map[string]interface{}{{
			"resource": map[string]interface{}{
				"attributes": []otlpKV{strKV("service.name", "flynn-host")},
			},
			"scopeLogs": []map[string]interface{}{{
				"scope": map[string]string{"name": "flynn"},
				"logRecords": []map[string]interface{}{{
					"timeUnixNano": fmt.Sprintf("%d", time.Now().UnixNano()),
					"severityText": sev,
					"body":         map[string]string{"stringValue": body},
					"attributes":   attrs,
				}},
			}},
		}},
	}
	b, _ := json.Marshal(payload)
	return b
}

func otlpMetricsJSON(stats *host.HostResourceStats, jobs *host.AllJobsStats) []byte {
	now := fmt.Sprintf("%d", time.Now().UnixNano())
	var metrics []map[string]interface{}
	gauge := func(name string, asInt *int64, asDouble *float64) map[string]interface{} {
		dp := map[string]interface{}{"timeUnixNano": now}
		if asInt != nil {
			dp["asInt"] = fmt.Sprintf("%d", *asInt)
		}
		if asDouble != nil {
			dp["asDouble"] = *asDouble
		}
		return map[string]interface{}{
			"name": name,
			"gauge": map[string]interface{}{
				"dataPoints": []map[string]interface{}{dp},
			},
		}
	}
	i64 := func(v uint64) *int64 { n := int64(v); return &n }
	i := func(v int) *int64 { n := int64(v); return &n }
	f := func(v float64) *float64 { return &v }
	if stats != nil {
		metrics = append(metrics,
			gauge("flynn.host.cpu.usage_percent", nil, f(stats.CPUUsagePercent)),
			gauge("flynn.host.cpu.count", i(stats.CPUCount), nil),
			gauge("flynn.host.memory.used_bytes", i64(stats.MemoryUsedBytes), nil),
			gauge("flynn.host.memory.total_bytes", i64(stats.MemoryTotalBytes), nil),
			gauge("flynn.host.memory.available_bytes", i64(stats.MemoryAvailableBytes), nil),
			gauge("flynn.host.disk.used_bytes", i64(stats.DiskUsedBytes), nil),
			gauge("flynn.host.disk.total_bytes", i64(stats.DiskTotalBytes), nil),
			gauge("flynn.host.load.1", nil, f(stats.LoadAvg1)),
			gauge("flynn.host.load.5", nil, f(stats.LoadAvg5)),
			gauge("flynn.host.load.15", nil, f(stats.LoadAvg15)),
			gauge("flynn.host.jobs.running", i(stats.RunningJobsCount), nil),
			gauge("flynn.host.jobs.total", i(stats.TotalJobsCount), nil),
		)
	}
	if jobs != nil {
		n := int64(len(jobs.Jobs))
		metrics = append(metrics, gauge("flynn.host.jobs.reported", &n, nil))
		var mem uint64
		for _, j := range jobs.Jobs {
			if j != nil {
				mem += j.MemoryUsageBytes
			}
		}
		metrics = append(metrics, gauge("flynn.host.jobs.memory_bytes", i64(mem), nil))
	}
	payload := map[string]interface{}{
		"resourceMetrics": []map[string]interface{}{{
			"resource": map[string]interface{}{
				"attributes": []otlpKV{strKV("service.name", "flynn-host")},
			},
			"scopeMetrics": []map[string]interface{}{{
				"scope":   map[string]string{"name": "flynn"},
				"metrics": metrics,
			}},
		}},
	}
	if stats != nil && stats.HostID != "" {
		payload["resourceMetrics"].([]map[string]interface{})[0]["resource"].(map[string]interface{})["attributes"] = []otlpKV{
			strKV("service.name", "flynn-host"),
			strKV("host.id", stats.HostID),
		}
	}
	b, _ := json.Marshal(payload)
	return b
}
