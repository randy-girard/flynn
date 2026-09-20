package main

import (
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	logaggc "github.com/randy-girard/flynn/logaggregator/client"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
)

func TestLogJobLabel(t *testing.T) {
	if got := logJobLabel(logaggc.Message{JobName: "web.1", ProcessType: "web", JobID: "localhost-abc"}, nil); got != "web.1" {
		t.Fatalf("named=%q", got)
	}
	if got := logJobLabel(logaggc.Message{ProcessType: "web", JobID: "localhost-abc"}, nil); got != "web.localhost-abc" {
		t.Fatalf("fallback=%q", got)
	}
	if got := logJobLabel(logaggc.Message{JobID: "localhost-abc"}, nil); got != "localhost-abc" {
		t.Fatalf("id only=%q", got)
	}

	names := jobNameIndex([]*ct.Job{{
		ID:   "localhost-6cfb7c1a-cff2-4519-aca2-065e4d47edd2",
		UUID: "6cfb7c1a-cff2-4519-aca2-065e4d47edd2",
		Type: "web",
		Name: "web.1",
	}})
	got := logJobLabel(logaggc.Message{
		ProcessType: "web",
		JobID:       "localhost-6cfb7c1a-cff2-4519-aca2-065e4d47edd2",
	}, names)
	if got != "web.1" {
		t.Fatalf("from job list=%q", got)
	}
}

func TestFormatLogLineUsesShortName(t *testing.T) {
	ts, err := time.Parse(time.RFC3339Nano, "2009-11-10T23:00:00.123456Z")
	if err != nil {
		t.Fatal(err)
	}
	got := formatLogLine(logaggc.Message{
		Source:      "flynn",
		JobName:     "web.1",
		ProcessType: "web",
		JobID:       "localhost-a0ee10eb-a24b-4bab-8f58-87cada82a68c",
		Msg:         "Starting web process",
		Stream:      logagg.StreamTypeSystem,
		Timestamp:   ts,
	}, nil)
	want := "2009-11-10T23:00:00.123456Z flynn[web.1]: Starting web process"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}

	legacy := formatLogLine(logaggc.Message{
		Source:      "app",
		ProcessType: "web",
		JobID:       "localhost-abc",
		Msg:         "hello",
		Timestamp:   ts,
	}, nil)
	if legacy != "2009-11-10T23:00:00.123456Z app[web.localhost-abc]: hello" {
		t.Fatalf("legacy %q", legacy)
	}

	metrics := formatLogLine(logaggc.Message{
		Source:      "flynn",
		ProcessType: "web",
		JobID:       "localhost-6cfb7c1a-cff2-4519-aca2-065e4d47edd2",
		Msg:         "metrics cpu_percent=0",
		Stream:      logagg.StreamTypeSystem,
		Timestamp:   ts,
	}, map[string]string{"localhost-6cfb7c1a-cff2-4519-aca2-065e4d47edd2": "web.1"})
	if metrics != "2009-11-10T23:00:00.123456Z flynn[web.1]: metrics cpu_percent=0" {
		t.Fatalf("metrics %q", metrics)
	}
}
