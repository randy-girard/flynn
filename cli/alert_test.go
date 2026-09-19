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
