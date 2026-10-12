package cli

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
)

func TestAlertCreateBodyRequiresChannel(t *testing.T) {
	args := &docopt.Args{String: map[string]string{
		"--metric": "disk_percent", "--op": "gte", "--threshold": "90",
	}}
	if _, err := alertCreateBody(args, ""); err == nil || !strings.Contains(err.Error(), "email") {
		t.Fatalf("got %v", err)
	}
}

func TestAlertCreateBodyDefaultsName(t *testing.T) {
	args := &docopt.Args{String: map[string]string{
		"--metric": "disk_percent", "--op": "gte", "--threshold": "90",
		"--email": "ops@example.com", "--host": "node-a",
	}}
	body, err := alertCreateBody(args, "")
	if err != nil {
		t.Fatal(err)
	}
	if body["name"] != "disk_percent gte 90" || body["host_id"] != "node-a" || body["notify_email"] != true {
		t.Fatalf("%v", body)
	}
}

func TestAlertCreateBodyEvent(t *testing.T) {
	args, err := docopt.Parse(commands["alert:add"].usage, []string{"alert:add", "--event", "a10", "--webhook", "https://hooks.example/x"}, true, "", false)
	if err != nil {
		t.Fatal(err)
	}
	body, err := alertCreateBody(args, "")
	if err != nil {
		t.Fatal(err)
	}
	if body["metric"] != "event:A10" || body["operator"] != "gte" || body["threshold"] != 1.0 || body["name"] != "on A10" {
		t.Fatalf("%v", body)
	}
	if got := alertWhenCell(dashboardMetricAlert{Metric: "event:A10"}); got != "on event" {
		t.Fatalf("when %q", got)
	}
	if _, err := docopt.Parse(commands["alert:add"].usage, []string{"alert:add", "--metric", "disk_percent", "--op", "gte", "--threshold", "90"}, true, "", false); err != nil {
		t.Fatalf("metric form: %v", err)
	}
}

func TestWriteAlertTable(t *testing.T) {
	var b strings.Builder
	err := writeAlertTable(&b, []dashboardMetricAlert{{
		ID: "a1", Name: "disk", Metric: "disk_percent", HostID: "node-a",
		Operator: "gte", Threshold: 90, NotifyEmail: true, EmailTo: "ops@example.com",
		Enabled: true, Firing: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{"a1", "disk_percent@node-a", "GTE 90", "ops@example.com", "firing"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
}

func TestDashboardDoJSONError(t *testing.T) {
	err := dashboardDoJSON(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":"unknown metric"}`)), Header: make(http.Header)}, nil
	})}, http.MethodGet, "http://dashboard.discoverd/api/alerts", "key", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown metric") {
		t.Fatalf("got %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
