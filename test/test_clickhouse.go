package main

import (
	"fmt"

	c "github.com/flynn/go-check"
)

type ClickhouseSuite struct {
	Helper
}

var _ = c.ConcurrentSuite(&ClickhouseSuite{})

func (s *ClickhouseSuite) TestClickhouseEnv(t *c.C) {
	a := s.newCliTestApp(t)

	t.Assert(a.flynn("resource", "add", "clickhouse"), Succeeds)

	client := s.controllerClient(t)
	release, err := client.GetAppRelease(a.id)
	t.Assert(err, c.IsNil)

	service, ok := release.Env["FLYNN_CLICKHOUSE"]
	if !ok {
		t.Fatal("missing FLYNN_CLICKHOUSE")
	}
	clickhouseApp, err := client.GetApp(service)
	t.Assert(err, c.IsNil)
	clickhouseRelease, err := client.GetAppRelease(clickhouseApp.ID)
	t.Assert(err, c.IsNil)
	t.Assert(clickhouseRelease.Processes, c.HasLen, 2)

	keeperProc, ok := clickhouseRelease.Processes["keeper"]
	if !ok {
		t.Fatal("missing keeper process")
	}
	t.Assert(keeperProc.Service, c.Equals, service+"-keeper")

	clickhouseProc, ok := clickhouseRelease.Processes["clickhouse"]
	if !ok {
		t.Fatal("missing clickhouse process")
	}
	t.Assert(clickhouseProc.Service, c.Equals, service)

	password, ok := release.Env["CLICKHOUSE_PASSWORD"]
	if !ok {
		t.Fatal("missing CLICKHOUSE_PASSWORD")
	}

	expected := map[string]string{
		"CLICKHOUSE_HOST":      fmt.Sprintf("leader.%s.discoverd", service),
		"CLICKHOUSE_PORT":      "9000",
		"CLICKHOUSE_HTTP_PORT": "8123",
		"CLICKHOUSE_USER":      "default",
		"CLICKHOUSE_PASSWORD":  password,
		"CLICKHOUSE_CLUSTER":   "flynn",
	}
	for key, val := range expected {
		actual, ok := release.Env[key]
		if !ok {
			t.Fatalf("env missing key %q", key)
		}
		if actual != val {
			t.Fatalf("expected %s to be %s, got %s", key, val, actual)
		}
	}
}

func (s *ClickhouseSuite) TestCreateDatabase(t *c.C) {
	a := s.newCliTestApp(t)

	t.Assert(a.flynn("resource", "add", "clickhouse"), Succeeds)

	release, err := s.controllerClient(t).GetAppRelease(a.id)
	t.Assert(err, c.IsNil)
	a.waitForService(release.Env["FLYNN_CLICKHOUSE"])

	t.Assert(a.flynn("clickhouse", "databases", "create", "analytics"), Succeeds)
	t.Assert(a.flynn("clickhouse", "databases", "info", "analytics"), Succeeds)
	t.Assert(a.flynn("clickhouse", "databases"), SuccessfulOutputContains, "analytics")
	t.Assert(a.flynn("clickhouse", "databases", "destroy", "analytics"), Succeeds)
}
