package logmux

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	ct "github.com/flynn/flynn/controller/types"
	host "github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/syslog/rfc5424"
)

type otelCapture struct {
	mu      sync.Mutex
	logs    int
	metrics int
	bodies  []string
}

func (c *otelCapture) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, r.URL.Path+" "+string(body))
		if strings.HasSuffix(r.URL.Path, "/v1/logs") {
			c.logs++
		}
		if strings.HasSuffix(r.URL.Path, "/v1/metrics") {
			c.metrics++
		}
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
}

type stubJobs struct {
	jobs map[string]*host.ActiveJob
}

func (s stubJobs) GetJob(id string) *host.ActiveJob { return s.jobs[id] }

func TestOTLPSinkLogsAndMetrics(t *testing.T) {
	cap := &otelCapture{}
	ts := httptest.NewServer(cap.handler())
	t.Cleanup(ts.Close)

	cfg, _ := json.Marshal(ct.OTLPSinkConfig{
		Endpoint: ts.URL,
		Logs:     true,
		Metrics:  true,
		Scope:    ct.SinkScopeSystem,
	})
	sm := NewSinkManager("", nil, stubJobs{jobs: map[string]*host.ActiveJob{
		"job-sys": {Job: &host.Job{Metadata: map[string]string{"flynn-controller.app": "ctrl", "flynn-system-app": "true"}}},
		"job-app": {Job: &host.Job{Metadata: map[string]string{"flynn-controller.app": "myapp"}}},
	}}, nil)
	sink, err := NewOTLPSink(sm, &SinkInfo{ID: "otel1", Kind: ct.SinkKindOTLP, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}

	sysMsg := rfc5424.NewMessage(&rfc5424.Header{ProcID: []byte("web.job-sys"), AppName: []byte("controller")}, []byte("controller ready"))
	if err := sink.Write(message{Message: sysMsg}); err != nil {
		t.Fatal(err)
	}
	userMsg := rfc5424.NewMessage(&rfc5424.Header{ProcID: []byte("web.job-app"), AppName: []byte("myapp")}, []byte("hello user"))
	if err := sink.Write(message{Message: userMsg}); err != nil {
		t.Fatal(err)
	}
	if err := sink.ExportMetrics(&host.HostResourceStats{HostID: "host0", MemoryUsedBytes: 42, RunningJobsCount: 3}, &host.AllJobsStats{Jobs: []*host.ContainerStats{{JobID: "a"}}}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		cap.mu.Lock()
		logs, metrics := cap.logs, cap.metrics
		cap.mu.Unlock()
		if logs == 1 && metrics == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.logs != 1 {
		t.Fatalf("system scope should forward 1 log, got %d bodies=%v", cap.logs, cap.bodies)
	}
	if cap.metrics != 1 {
		t.Fatalf("metrics posts=%d", cap.metrics)
	}
	joined := strings.Join(cap.bodies, "\n")
	if !strings.Contains(joined, "controller ready") {
		t.Fatalf("missing system log: %s", joined)
	}
	if strings.Contains(joined, "hello user") {
		t.Fatalf("user log leaked into system sink: %s", joined)
	}
	if !strings.Contains(joined, "flynn.host.memory.used_bytes") {
		t.Fatalf("missing metric: %s", joined)
	}
}

func TestOTLPLogsJSON(t *testing.T) {
	msg := rfc5424.NewMessage(&rfc5424.Header{ProcID: []byte("web.abc"), AppName: []byte("www")}, []byte("hi"))
	body := string(otlpLogsJSON(msg, "app-id", true))
	if !strings.Contains(body, `"hi"`) || !strings.Contains(body, "flynn.app_id") {
		t.Fatalf("payload %s", body)
	}
}
