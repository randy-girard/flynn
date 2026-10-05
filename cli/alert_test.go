package main

import (
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
	cfg "github.com/randy-girard/flynn/cli/config"
)

func TestAppAlertCreateBody(t *testing.T) {
	args := &docopt.Args{String: map[string]string{
		"--metric":    "cpu_percent",
		"--op":        "gt",
		"--threshold": "80",
		"--email":     "ops@example.com",
	}}
	body, err := appAlertCreateBody(args)
	if err != nil {
		t.Fatal(err)
	}
	if body["process_type"] != "all" || body["name"] != "cpu_percent gt 80" {
		t.Fatalf("%v", body)
	}
}

func TestDashboardURLFromController(t *testing.T) {
	if got := dashboardURLFromController("https://controller.demo.localflynn.com"); got != "https://dashboard.demo.localflynn.com" {
		t.Fatalf("got %s", got)
	}
	if got := dashboardURLFromController("http://controller.discoverd"); got != "http://dashboard.discoverd" {
		t.Fatalf("got %s", got)
	}
	if got := dashboardURLFromController("https://example.com"); got != "" {
		t.Fatalf("got %s", got)
	}
}

func TestDashboardBaseURLPrefersExplicit(t *testing.T) {
	base, err := dashboardBaseURL(&cfg.Cluster{
		DashboardURL:  "https://dash.example",
		ControllerURL: "https://controller.demo.localflynn.com",
	})
	if err != nil || base != "https://dash.example" {
		t.Fatalf("got %s %v", base, err)
	}
}

func TestWriteAlertTableApp(t *testing.T) {
	var b strings.Builder
	if err := writeAlertTable(&b, []dashboardMetricAlert{{
		ID: "a1", Name: "hot", Metric: "cpu_percent", ProcessType: "web",
		Operator: "gt", Threshold: 80, NotifyWebhook: true, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "cpu_percent/web") || !strings.Contains(b.String(), "ok") {
		t.Fatalf("%s", b.String())
	}
}

func TestAppAlertCreateBodyErrorsAndNotify(t *testing.T) {
	if _, err := appAlertCreateBody(&docopt.Args{String: map[string]string{
		"--metric": "cpu_percent", "--op": "gt", "--threshold": "nope",
	}}); err == nil || !strings.Contains(err.Error(), "threshold") {
		t.Fatalf("threshold: %v", err)
	}
	if _, err := appAlertCreateBody(&docopt.Args{String: map[string]string{
		"--metric": "cpu_percent", "--op": "gt", "--threshold": "80",
	}}); err == nil || !strings.Contains(err.Error(), "--email") {
		t.Fatalf("notify: %v", err)
	}
	if _, err := appAlertCreateBody(&docopt.Args{String: map[string]string{
		"--metric": "cpu_percent", "--op": "gt", "--threshold": "80", "--email": "ops@example.com", "--cooldown": "-1",
	}}); err == nil || !strings.Contains(err.Error(), "cooldown") {
		t.Fatalf("cooldown: %v", err)
	}
	body, err := appAlertCreateBody(&docopt.Args{String: map[string]string{
		"--metric": "memory_bytes", "--op": "GTE", "--threshold": "1", "--name": "hot",
		"--webhook": "https://hooks.example/x", "--process-type": "web", "--cooldown": "10",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if body["name"] != "hot" || body["operator"] != "gte" || body["process_type"] != "web" || body["cooldown_seconds"] != 10 {
		t.Fatalf("%v", body)
	}
	if body["notify_webhook"] != true || body["webhook_url"] != "https://hooks.example/x" {
		t.Fatalf("webhook %v", body)
	}
}

func TestAlertTableCells(t *testing.T) {
	if got := alertMetricCell(dashboardMetricAlert{Metric: "cpu", HostID: "host1"}); got != "cpu@host1" {
		t.Fatalf("%s", got)
	}
	if got := alertMetricCell(dashboardMetricAlert{Metric: "cpu", ProcessType: "all"}); got != "cpu" {
		t.Fatalf("%s", got)
	}
	if got := alertNotifyCell(dashboardMetricAlert{NotifyEmail: true, EmailTo: "ops@example.com", NotifyWebhook: true}); got != "ops@example.com,webhook" {
		t.Fatalf("%s", got)
	}
	if alertStateCell(dashboardMetricAlert{}) != "disabled" {
		t.Fatal("disabled")
	}
	if alertStateCell(dashboardMetricAlert{Enabled: true, Firing: true}) != "firing" {
		t.Fatal("firing")
	}
}

func TestDashboardURLFromNestedControllerHost(t *testing.T) {
	if got := dashboardURLFromController("https://region.controller.example.com"); got != "https://region.dashboard.example.com" {
		t.Fatalf("%s", got)
	}
}
