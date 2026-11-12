package main

import (
	"strings"

	c "github.com/flynn/go-check"
)

type ClickhouseSuite struct {
	Helper
}

var _ = c.ConcurrentSuite(&ClickhouseSuite{})

func (s *ClickhouseSuite) SetUpSuite(t *c.C) {
	s.skipUnlessProvider(t, "clickhouse")
}

func (s *ClickhouseSuite) TestClickhouseEnv(t *c.C) {
	a := s.newCliTestApp(t)

	t.Assert(a.flynn("resource", "add", "clickhouse"), Succeeds)

	client := s.controllerClient(t)
	release, err := client.GetAppRelease(a.id)
	t.Assert(err, c.IsNil)
	service := attachedService(t, client, a.id, "FLYNN_CLICKHOUSE")
	if release.Env["FLYNN_CLICKHOUSE"] != "" {
		t.Fatalf("app release kept FLYNN_CLICKHOUSE %q; identity stays on the resource", release.Env["FLYNN_CLICKHOUSE"])
	}
	host := "leader." + service + ".discoverd"
	if !strings.Contains(release.Env["CLICKHOUSE_URL"], host) {
		t.Fatalf("CLICKHOUSE_URL %q does not point at %s", release.Env["CLICKHOUSE_URL"], host)
	}
	if !releaseHasColorURL(release.Env, "FLYNN_CLICKHOUSE_", host) {
		t.Fatalf("app release missing FLYNN_CLICKHOUSE_<COLOR>_URL for %s: %#v", host, release.Env)
	}

	clickhouseRelease, err := client.GetAppRelease(service)
	t.Assert(err, c.IsNil)
	t.Assert(clickhouseRelease.Processes, c.HasLen, 1)
	clickhouseProc, ok := clickhouseRelease.Processes["clickhouse"]
	if !ok {
		t.Fatal("missing clickhouse process")
	}
	t.Assert(clickhouseProc.Service, c.Equals, service)
}

func (s *ClickhouseSuite) TestCreateDatabase(t *c.C) {
	a := s.newCliTestApp(t)

	t.Assert(a.flynn("resource", "add", "clickhouse"), Succeeds)

	service := attachedService(t, s.controllerClient(t), a.id, "FLYNN_CLICKHOUSE")
	a.waitForService(service)

	t.Assert(a.flynn("clickhouse", "databases", "create", "analytics"), Succeeds)
	t.Assert(a.flynn("clickhouse", "databases", "info", "analytics"), Succeeds)
	t.Assert(a.flynn("clickhouse", "databases"), SuccessfulOutputContains, "analytics")
	t.Assert(a.flynn("clickhouse", "databases", "destroy", "analytics"), Succeeds)
}
