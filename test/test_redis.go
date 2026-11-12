package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	c "github.com/flynn/go-check"
)

type RedisSuite struct {
	Helper
}

var _ = c.ConcurrentSuite(&RedisSuite{})

func (s *RedisSuite) SetUpSuite(t *c.C) {
	s.skipUnlessProvider(t, "redis")
}

func (s *RedisSuite) TestRedisEnv(t *c.C) {
	a := s.newCliTestApp(t)

	t.Assert(a.flynn("resource", "add", "redis"), Succeeds)

	client := s.controllerClient(t)
	release, err := client.GetAppRelease(a.id)
	t.Assert(err, c.IsNil)
	service := attachedService(t, client, a.id, "FLYNN_REDIS")
	if release.Env["FLYNN_REDIS"] != "" {
		t.Fatalf("app release kept FLYNN_REDIS %q; identity stays on the resource", release.Env["FLYNN_REDIS"])
	}
	host := "leader." + service + ".discoverd"
	if !strings.Contains(release.Env["REDIS_URL"], host) {
		t.Fatalf("REDIS_URL %q does not point at %s", release.Env["REDIS_URL"], host)
	}
	if !releaseHasColorURL(release.Env, "FLYNN_REDIS_", host) {
		t.Fatalf("app release missing FLYNN_REDIS_<COLOR>_URL for %s: %#v", host, release.Env)
	}

	redisRelease, err := client.GetAppRelease(service)
	t.Assert(err, c.IsNil)
	t.Assert(redisRelease.Processes, c.HasLen, 1)
	redisProc, ok := redisRelease.Processes["redis"]
	if !ok {
		t.Fatal("missing redis process")
	}
	t.Assert(redisProc.Service, c.Equals, service)
	if redisRelease.Env["REDIS_PASSWORD"] == "" {
		t.Fatal("instance release missing REDIS_PASSWORD")
	}
}

func (s *RedisSuite) TestDumpRestore(t *c.C) {
	a := s.newCliTestApp(t)

	res := a.flynn("resource", "add", "redis")
	t.Assert(res, Succeeds)
	id := createdName(res.Output)
	t.Assert(id, c.Not(c.Equals), "")

	service := attachedService(t, s.controllerClient(t), a.id, "FLYNN_REDIS")
	a.waitForService(service)

	t.Assert(a.flynn("redis", "redis-cli", "set", "foo", "bar"), Succeeds)
	t.Assert(a.flynn("redis", "redis-cli", "get", "foo"), SuccessfulOutputContains, "bar")

	file := filepath.Join(t.MkDir(), "dump.rdb")
	t.Assert(a.flynn("redis", "dump", "-f", file), Succeeds)
	raw, err := os.ReadFile(file)
	t.Assert(err, c.IsNil)
	if !bytes.HasPrefix(raw, []byte("REDIS")) {
		t.Fatalf("dump is not an RDB file (%d bytes)", len(raw))
	}

	t.Assert(a.flynn("resource", "remove", "redis", id), Succeeds)
}
