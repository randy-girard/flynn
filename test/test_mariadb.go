package main

import (
	"path/filepath"
	"time"

	c "github.com/flynn/go-check"
)

// The MySQL plugin (flynn-plugin-mysql) is installed as mysql; mariadb is a
// deprecated alias. Plugin app is mysql-plugin, data service is mysql.
// This file name is historical.
//
// Each resource is one node. Followers are a separate resource, so this suite
// does not scale the plugin release into a sirenia cluster.

type MariaDBSuite struct {
	Helper
}

var _ = c.ConcurrentSuite(&MariaDBSuite{})

func (s *MariaDBSuite) SetUpSuite(t *c.C) {
	s.skipUnlessProvider(t, "mysql")
}

func (s *MariaDBSuite) TestDumpRestore(t *c.C) {
	r := s.newGitRepo(t, "empty")
	t.Assert(r.flynn("create"), Succeeds)

	res := r.flynn("resource", "add", "mysql")
	t.Assert(res, Succeeds)
	id := createdName(res.Output)
	t.Assert(id, c.Not(c.Equals), "")
	// mysql:wait reports a primary ready as soon as the resource exists.
	// The server is registered in discoverd later.
	t.Assert(r.flynn("mysql", "wait", id), Succeeds)
	var console *CmdResult
	deadline := time.Now().Add(2 * time.Minute)
	for {
		console = r.flynn("mysql", "console", "--", "-e",
			"CREATE TABLE T (F text); INSERT INTO T (F) VALUES ('abc')")
		if console.Err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	t.Assert(console, Succeeds)

	file := filepath.Join(t.MkDir(), "db.dump")
	t.Assert(r.flynn("mysql", "dump", "-f", file), Succeeds)
	t.Assert(r.flynn("mysql", "console", "--", "-e", "DROP TABLE T"), Succeeds)

	r.flynn("mysql", "restore", "-f", file)

	query := r.flynn("mysql", "console", "--", "-e", "SELECT * FROM T")
	t.Assert(query, SuccessfulOutputContains, "abc")

	t.Assert(r.flynn("resource", "remove", "mysql", id), Succeeds)
}
