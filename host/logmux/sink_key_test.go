package logmux

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
)

// TestLogAggregatorSinkSendsClusterKey (EDGE-04): logaggregator's API needs
// the cluster key, so the host's cursor lookup sends the key it resolves at
// connect time. Without one the request is refused and GetCursor errors
// (the mux then ships from its local files, as before).
func TestLogAggregatorSinkSendsClusterKey(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, _ := r.BasicAuth()
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+pass)
		mu.Unlock()
		if pass != "k1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"host1":{"Time":"2026-11-22T00:00:00Z","Seq":7}}`))
	}))
	defer api.Close()
	syslog, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer syslog.Close()
	go func() {
		for {
			c, err := syslog.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	prev := aggregatorAPIURL
	aggregatorAPIURL = func(string) string { return api.URL }
	defer func() { aggregatorAPIURL = prev }()

	key := "k1"
	sm := NewSinkManager("", nil, nil, log15.New())
	sm.SetAggregatorKey(func() string { return key })
	cfg, _ := json.Marshal(ct.LogAggregatorSinkConfig{Addr: syslog.Addr().String()})
	sink, err := NewLogAggregatorSink(sm, &SinkInfo{ID: "s1", Kind: ct.SinkKindLogaggregator, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Connect(); err != nil {
		t.Fatal(err)
	}
	cur, err := sink.GetCursor("host1")
	sink.Close()
	if err != nil || cur == nil || cur.Seq != 7 {
		t.Fatalf("GetCursor with key: cur=%v err=%v", cur, err)
	}

	key = ""
	if err := sink.Connect(); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.GetCursor("host1"); err == nil {
		t.Fatal("GetCursor without a key must fail, not return an empty cursor")
	}
	sink.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 || seen[0] != "/cursors k1" || !strings.HasPrefix(seen[len(seen)-1], "/cursors ") {
		t.Fatalf("requests=%q", seen)
	}
}
