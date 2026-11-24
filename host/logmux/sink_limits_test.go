package logmux

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
)

// EDGE-85: a logaggregator sink learns at connect time how many lines per app
// the aggregator keeps and for how long, from its /cursors response, so the
// host's resend cannot drift from the aggregator's settings. An aggregator
// that does not say keeps the 10,000-line ring and may keep lines of any age.
func TestLogAggregatorSinkResendLimits(t *testing.T) {
	headers := map[string]string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
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

	sm := NewSinkManager("", nil, nil, log15.New())
	cfg, _ := json.Marshal(ct.LogAggregatorSinkConfig{Addr: syslog.Addr().String()})
	sink, err := NewLogAggregatorSink(sm, &SinkInfo{ID: "s1", Kind: ct.SinkKindLogaggregator, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	limits := func() resendLimits {
		t.Helper()
		if err := sink.Connect(); err != nil {
			t.Fatal(err)
		}
		defer sink.Close()
		if _, err := sink.GetCursor("host1"); err != nil {
			t.Fatal(err)
		}
		return sink.resendLimits()
	}

	if got := limits(); got != (resendLimits{Lines: 10000}) {
		t.Fatalf("no advertised limits: %+v, want the 10,000-line ring and no age limit", got)
	}
	headers[logagg.BufferLinesHeader] = "5000"
	headers[logagg.BufferTTLHeader] = "72h0m0s"
	if got := limits(); got != (resendLimits{Lines: 5000, MaxAge: 72 * time.Hour}) {
		t.Fatalf("advertised 5000 lines and 72h: %+v", got)
	}
	// Nonsense is ignored, not taken as "keep nothing".
	headers[logagg.BufferLinesHeader] = "-1"
	headers[logagg.BufferTTLHeader] = "soon"
	if got := limits(); got != (resendLimits{Lines: 10000}) {
		t.Fatalf("invalid headers: %+v", got)
	}

	// The app ids the controller knows come from the sink manager's lister.
	if _, err := sink.knownApps(); err == nil {
		t.Fatal("no lister set: want an error, so no app is skipped as deleted")
	}
	sm.SetAppLister(func() ([]string, error) { return []string{appA, appB}, nil })
	known, err := sink.knownApps()
	if err != nil || len(known) != 2 || !known[appA] || !known[appB] {
		t.Fatalf("known=%v err=%v", known, err)
	}
	sm.SetAppLister(func() ([]string, error) { return nil, errors.New("controller down") })
	if _, err := sink.knownApps(); err == nil {
		t.Fatal("a failed list must be an error, not an empty list (which would skip every app)")
	}
	sm.SetAppLister(func() ([]string, error) { return nil, nil })
	if _, err := sink.knownApps(); err == nil {
		t.Fatal("an empty list must not skip every app")
	}
}
