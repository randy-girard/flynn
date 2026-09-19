package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWriteAppMetricSnapshot(t *testing.T) {
	cpu := 42.2
	mem := 61.0
	raw, _ := json.Marshal(map[string]appMetricAgg{
		"web": {ContainerCount: 2, AvgCpu: &cpu, AvgMemOfLimitPercent: &mem},
		"all": {ContainerCount: 3},
	})
	var b strings.Builder
	err := writeAppMetricSnapshot(&b, appMetricSnapshot{
		CollectedAt: time.Date(2026, 9, 19, 18, 0, 0, 0, time.UTC),
		Aggregates:  raw,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{"2026-09-19T18:00:00Z", "web", "42.2%", "61.0%"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
}

func TestWriteAppMetricSnapshotFilter(t *testing.T) {
	raw, _ := json.Marshal(map[string]appMetricAgg{"web": {ContainerCount: 2}, "worker": {ContainerCount: 1}})
	var b strings.Builder
	if err := writeAppMetricSnapshot(&b, appMetricSnapshot{Aggregates: raw}, "web"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "worker") {
		t.Fatalf("filter leaked worker:\n%s", b.String())
	}
}
