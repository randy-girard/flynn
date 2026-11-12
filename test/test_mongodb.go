package main

import (
	"path/filepath"
	"time"

	c "github.com/flynn/go-check"
)

type MongoDBSuite struct {
	Helper
}

var _ = c.ConcurrentSuite(&MongoDBSuite{})

func (s *MongoDBSuite) SetUpSuite(t *c.C) {
	s.skipUnlessProvider(t, "mongodb")
}

func (s *MongoDBSuite) TestDumpRestore(t *c.C) {
	r := s.newGitRepo(t, "empty")
	created := r.flynn("create")
	t.Assert(created, Succeeds)
	appName := createdName(created.Output)
	t.Assert(appName, c.Not(c.Equals), "")

	res := r.flynn("resource", "add", "mongodb")
	t.Assert(res, Succeeds)
	id := createdName(res.Output)
	t.Assert(id, c.Not(c.Equals), "")

	app, err := s.controllerClient(t).GetApp(appName)
	t.Assert(err, c.IsNil)
	service := attachedService(t, s.controllerClient(t), app.ID, "FLYNN_MONGO")

	// The process registers in discoverd before the app user exists.
	// Retry the first dump until authentication can succeed.
	var dump *CmdResult
	deadline := time.Now().Add(2 * time.Minute)
	for {
		_, _ = s.discoverdClient(t).Instances(service, 5*time.Second)
		dump = r.flynn("mongodb", "dump", "-f", filepath.Join(t.MkDir(), "db.dump"))
		if dump.Err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	t.Assert(dump, Succeeds)

	file := filepath.Join(t.MkDir(), "db.dump")
	t.Assert(r.flynn("mongodb", "mongo", "--", "--eval", `db.foos.insert({data: "foobar"})`), Succeeds)
	t.Assert(r.flynn("mongodb", "dump", "-f", file), Succeeds)
	t.Assert(r.flynn("mongodb", "mongo", "--", "--eval", "db.foos.drop()"), Succeeds)

	r.flynn("mongodb", "restore", "-f", file)
	query := r.flynn("mongodb", "mongo", "--", "--eval", "db.foos.find()")
	t.Assert(query, SuccessfulOutputContains, "foobar")

	t.Assert(r.flynn("resource", "remove", "mongodb", id), Succeeds)
}
