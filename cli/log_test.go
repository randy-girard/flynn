package main

import (
	"testing"
	"time"

	logaggc "github.com/randy-girard/flynn/logaggregator/client"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
)

func TestLogJobLabel(t *testing.T) {
	if got := logJobLabel(logaggc.Message{JobName: "web.1", ProcessType: "web", JobID: "localhost-abc"}); got != "web.1" {
		t.Fatalf("named=%q", got)
	}
	if got := logJobLabel(logaggc.Message{ProcessType: "web", JobID: "localhost-abc"}); got != "web.localhost-abc" {
		t.Fatalf("fallback=%q", got)
	}
	if got := logJobLabel(logaggc.Message{JobID: "localhost-abc"}); got != "localhost-abc" {
		t.Fatalf("id only=%q", got)
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
	})
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
	})
	if legacy != "2009-11-10T23:00:00.123456Z app[web.localhost-abc]: hello" {
		t.Fatalf("legacy %q", legacy)
	}
}
