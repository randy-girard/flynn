package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	ct "github.com/flynn/flynn/controller/types"
	c "github.com/flynn/go-check"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type MongoDBSuite struct {
	Helper
}

var _ = c.ConcurrentSuite(&MongoDBSuite{})

func (s *MongoDBSuite) TestDumpRestore(t *c.C) {
	r := s.newGitRepo(t, "empty")
	t.Assert(r.flynn("create"), Succeeds)

	res := r.flynn("resource", "add", "mongodb")
	t.Assert(res, Succeeds)
	id := strings.Split(res.Output, " ")[2]

	// dumping an empty database should not fail
	file := filepath.Join(t.MkDir(), "db.dump")
	t.Assert(r.flynn("mongodb", "dump", "-f", file), Succeeds)

	t.Assert(r.flynn("mongodb", "mongo", "--", "--eval", `db.foos.insert({data: "foobar"})`), Succeeds)

	t.Assert(r.flynn("mongodb", "dump", "-f", file), Succeeds)
	t.Assert(r.flynn("mongodb", "mongo", "--", "--eval", "db.foos.drop()"), Succeeds)

	r.flynn("mongodb", "restore", "-f", file)
	query := r.flynn("mongodb", "mongo", "--", "--eval", "db.foos.find()")
	t.Assert(query, SuccessfulOutputContains, "foobar")

	t.Assert(r.flynn("resource", "remove", "mongodb", id), Succeeds)
}

// Sirenia integration tests
var sireniaMongoDB = sireniaDatabase{
	appName:    "mongodb",
	serviceKey: "FLYNN_MONGO",
	hostKey:    "MONGO_HOST",
	assertWriteable: func(t *c.C, r *ct.Release, d *sireniaDeploy) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		uri := fmt.Sprintf("mongodb://flynn:%s@leader.%s.discoverd:27017/admin?directConnection=true",
			r.Env["MONGO_PWD"], d.name)
		client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
		t.Assert(err, c.IsNil)
		defer client.Disconnect(ctx)

		_, err = client.Database("test").Collection("test").InsertOne(ctx, bson.M{"test": "test"})
		t.Assert(err, c.IsNil)
	},
}

func (s *MongoDBSuite) TestDeploySingleAsync(t *c.C) {
	testSireniaDeploy(s.controllerClient(t), s.discoverdClient(t), t, &sireniaDeploy{
		name:        "mongodb-single-async",
		db:          sireniaMongoDB,
		sireniaJobs: 3,
		webJobs:     2,
		expected:    testDeploySingleAsync,
	})
}

func (s *MongoDBSuite) TestDeployMultipleAsync(t *c.C) {
	testSireniaDeploy(s.controllerClient(t), s.discoverdClient(t), t, &sireniaDeploy{
		name:        "mongodb-multiple-async",
		db:          sireniaMongoDB,
		sireniaJobs: 5,
		webJobs:     2,
		expected:    testDeployMultipleAsync,
	})
}
