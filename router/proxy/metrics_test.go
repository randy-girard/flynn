package proxy

import (
	"testing"
	"time"
)

func TestObserveSnapshotPerService(t *testing.T) {
	prev := requestLatencies
	requestLatencies = newLatencyStore()
	t.Cleanup(func() { requestLatencies = prev })

	now := time.Now()
	for i := 0; i < 10; i++ {
		ObserveAt("demo-web", time.Duration(10+i)*time.Millisecond, 200, now)
	}
	ObserveAt("demo-web", 50*time.Millisecond, 503, now)
	ObserveAt("other-web", 100*time.Millisecond, 200, now)

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
	if len(got[0].Samples) != 0 {
		t.Fatalf("public snapshot must omit samples: %+v", got[0])
	}
}

func TestSnapshotDropsStaleSamples(t *testing.T) {
	prev := requestLatencies
	requestLatencies = newLatencyStore()
	t.Cleanup(func() { requestLatencies = prev })

	now := time.Now()
	ObserveAt("demo-web", 5*time.Millisecond, 200, now.Add(-2*time.Minute))
	ObserveAt("demo-web", 40*time.Millisecond, 200, now)

	got := Snapshot()
	if len(got) != 1 {
		t.Fatalf("services = %d, want 1: %+v", len(got), got)
	}
	if got[0].Requests != 1 {
		t.Fatalf("stale sample should not count: %+v", got[0])
	}
	if got[0].P50Millis != 40 {
		t.Fatalf("p50 = %v, want 40 (only the live 40ms request)", got[0].P50Millis)
	}
}

func TestSnapshotOmitsIdleServices(t *testing.T) {
	prev := requestLatencies
	requestLatencies = newLatencyStore()
	t.Cleanup(func() { requestLatencies = prev })

	ObserveAt("demo-web", 12*time.Millisecond, 200, time.Now().Add(-2*time.Minute))
	if len(Snapshot()) != 0 {
		t.Fatal("idle services with only stale samples must be omitted")
	}
}

func TestSnapshotWithSamplesKeepsWindow(t *testing.T) {
	prev := requestLatencies
	requestLatencies = newLatencyStore()
	t.Cleanup(func() { requestLatencies = prev })

	now := time.Now()
	ObserveAt("demo-web", 10*time.Millisecond, 200, now)
	ObserveAt("demo-web", 20*time.Millisecond, 200, now)
	got := SnapshotWithSamples()
	if len(got) != 1 || len(got[0].Samples) != 2 {
		t.Fatalf("samples %+v", got)
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
