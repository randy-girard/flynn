package proxy

import (
	"testing"
	"time"
)

func TestPercentileLinearInterpolation(t *testing.T) {
	got := percentile([]float64{10, 20, 30, 40}, 50)
	if got != 25 {
		t.Fatalf("p50 = %v, want 25", got)
	}
	if percentile(nil, 95) != 0 {
		t.Fatal("empty window should be 0")
	}
	if percentile([]float64{7}, 99) != 7 {
		t.Fatal("single sample should return itself")
	}
}

func TestObserveSnapshotPerService(t *testing.T) {
	prev := requestLatencies
	requestLatencies = newLatencyStore()
	t.Cleanup(func() { requestLatencies = prev })

	for i := 0; i < 10; i++ {
		Observe("demo-web", time.Duration(10+i)*time.Millisecond, 200)
	}
	Observe("demo-web", 50*time.Millisecond, 503)
	Observe("other-web", 100*time.Millisecond, 200)

	got := Snapshot()
	if len(got) != 2 {
		t.Fatalf("services = %d, want 2: %+v", len(got), got)
	}
	if got[0].Service != "demo-web" {
		t.Fatalf("first service %q", got[0].Service)
	}
	if got[0].Requests != 11 || got[0].Errors != 1 {
		t.Fatalf("demo-web counts %+v", got[0])
	}
	if got[0].P50Millis <= 0 || got[0].P99Millis < got[0].P50Millis {
		t.Fatalf("percentiles %+v", got[0])
	}
	if got[1].Service != "other-web" || got[1].P50Millis != 100 {
		t.Fatalf("other-web %+v", got[1])
	}
}

func TestObserveSkipsEmptyService(t *testing.T) {
	prev := requestLatencies
	requestLatencies = newLatencyStore()
	t.Cleanup(func() { requestLatencies = prev })
	Observe("", time.Millisecond, 200)
	if len(Snapshot()) != 0 {
		t.Fatal("empty service must not be recorded")
	}
}
