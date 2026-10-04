package utils

import (
	"testing"

	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/pkg/syslog/rfc5424"
)

func TestIsPluginMetricsLine(t *testing.T) {
	if !IsPluginMetricsLine([]byte("flynn-postgres source=postgresql-basin-73690 addon=abc sample#tables=1")) {
		t.Fatal("flynn-postgres sample")
	}
	if !IsPluginMetricsLine([]byte("  flynn-redis source=r addon=x sample#keys=1\n")) {
		t.Fatal("flynn-redis sample with space")
	}
	if !IsPluginMetricsLine([]byte("heroku-postgres source=old sample#tables=1")) {
		t.Fatal("legacy heroku-postgres")
	}
	if IsPluginMetricsLine([]byte("LOG:  statement: SELECT 1")) {
		t.Fatal("engine log is not a sample")
	}
	if IsPluginMetricsLine([]byte("metrics cpu_percent=0.07")) {
		t.Fatal("host container metrics are already system")
	}
	if IsPluginMetricsLine(nil) {
		t.Fatal("empty")
	}
}

func TestStreamTypePromotesPluginSample(t *testing.T) {
	msg := rfc5424.NewMessage(
		&rfc5424.Header{MsgID: []byte(logagg.MsgIDStderr)},
		[]byte("flynn-postgres source=postgresql-basin-73690 sample#service-available=1"),
	)
	if got := StreamType(msg); got != logagg.StreamTypeSystem {
		t.Fatalf("stream=%s", got)
	}
	normal := rfc5424.NewMessage(
		&rfc5424.Header{MsgID: []byte(logagg.MsgIDStderr)},
		[]byte("ERROR:  relation foo does not exist"),
	)
	if got := StreamType(normal); got != logagg.StreamTypeStderr {
		t.Fatalf("engine stderr stream=%s", got)
	}
}
