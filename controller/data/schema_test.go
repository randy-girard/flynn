package data

import (
	"time"

	. "github.com/flynn/go-check"
	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/dbruntime"
	"github.com/randy-girard/flynn/pkg/postgres"
	pgtestutils "github.com/randy-girard/flynn/pkg/testutils/postgres"
)

const bufSize = 1024 * 1024

type S struct {
	db *postgres.DB
}

var _ = Suite(&S{})

func (s *S) SetUpSuite(c *C) {
	dbname := "controllerschematest"
	db := setupTestDB(c, dbname)
	if err := MigrateDB(db); err != nil {
		c.Fatal(err)
	}

	// reconnect with que statements prepared now that schema is migrated
	cfg, err := pgtestutils.ConnConfigForDatabase(dbname)
	c.Assert(err, IsNil)
	pgxpool, err := pgx.NewConnPool(pgx.ConnPoolConfig{
		ConnConfig:   cfg,
		AfterConnect: PrepareStatements,
	})
	if err != nil {
		c.Fatal(err)
	}
	db = postgres.New(pgxpool, nil)
	s.db = db
}

func (s *S) matchLabelFilters(c *C, labelFilters []ct.LabelFilter, labels map[string]string) bool {
	var ret bool
	c.Assert(s.db.QueryRow("SELECT match_label_filters($1, $2)", labelFilters, labels).Scan(&ret), IsNil)
	return ret
}

func (s *S) TestMatchLabelFilters(c *C) {
	labels := map[string]string{
		"one":   "ONE",
		"two":   "TWO",
		"three": "THREE",
		"four":  "FOUR",
	}

	// OP_IN
	c.Assert(s.matchLabelFilters(c, []ct.LabelFilter{
		{
			&ct.LabelFilterExpression{
				Op:     ct.LabelFilterExpressionOpIn,
				Key:    "one",
				Values: []string{"1", "ONE"},
			},
		},
	}, labels), Equals, true)
	c.Assert(s.matchLabelFilters(c, []ct.LabelFilter{
		{
			&ct.LabelFilterExpression{
				Op:     ct.LabelFilterExpressionOpIn,
				Key:    "one",
				Values: []string{"1"},
			},
		},
	}, labels), Equals, false)

	// OP_NOT_IN
	c.Assert(s.matchLabelFilters(c, []ct.LabelFilter{
		{
			&ct.LabelFilterExpression{
				Op:     ct.LabelFilterExpressionOpNotIn,
				Key:    "one",
				Values: []string{"1", "foo", "bar"},
			},
		},
	}, labels), Equals, true)
	c.Assert(s.matchLabelFilters(c, []ct.LabelFilter{
		{
			&ct.LabelFilterExpression{
				Op:     ct.LabelFilterExpressionOpNotIn,
				Key:    "one",
				Values: []string{"1", "ONE"},
			},
		},
	}, labels), Equals, false)

	// OP_EXISTS
	c.Assert(s.matchLabelFilters(c, []ct.LabelFilter{
		{
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "one",
			},
		},
	}, labels), Equals, true)
	c.Assert(s.matchLabelFilters(c, []ct.LabelFilter{
		{
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "foo",
			},
		},
	}, labels), Equals, false)

	// OP_NOT_EXISTS
	c.Assert(s.matchLabelFilters(c, []ct.LabelFilter{
		{
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpNotExists,
				Key: "foo",
			},
		},
	}, labels), Equals, true)
	c.Assert(s.matchLabelFilters(c, []ct.LabelFilter{
		{
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpNotExists,
				Key: "one",
			},
		},
	}, labels), Equals, false)

	// Multiple Expressions
	c.Assert(s.matchLabelFilters(c, []ct.LabelFilter{
		{
			&ct.LabelFilterExpression{
				Op:     ct.LabelFilterExpressionOpIn,
				Key:    "one",
				Values: []string{"1", "ONE", "one"},
			},
			&ct.LabelFilterExpression{
				Op:     ct.LabelFilterExpressionOpNotIn,
				Key:    "one",
				Values: []string{"foo", "bar", "baz"},
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "two",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "three",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpNotExists,
				Key: "foo",
			},
		},
	}, labels), Equals, true) // expressions are ANDed
	c.Assert(s.matchLabelFilters(c, []ct.LabelFilter{
		{
			&ct.LabelFilterExpression{
				Op:     ct.LabelFilterExpressionOpIn,
				Key:    "one",
				Values: []string{"1", "ONE", "one"},
			},
			&ct.LabelFilterExpression{
				Op:     ct.LabelFilterExpressionOpNotIn,
				Key:    "one",
				Values: []string{"foo", "bar", "baz"},
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "two",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "three",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpNotExists,
				Key: "foo",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpNotExists,
				Key: "four", // it exists
			},
		},
	}, labels), Equals, false) // expressions are ANDed

	// Multiple Filters
	c.Assert(s.matchLabelFilters(c, []ct.LabelFilter{
		{
			&ct.LabelFilterExpression{
				Op:     ct.LabelFilterExpressionOpIn,
				Key:    "three",
				Values: []string{"foo", "bar"},
			},
		}, // false
		{
			&ct.LabelFilterExpression{
				Op:     ct.LabelFilterExpressionOpIn,
				Key:    "one",
				Values: []string{"1", "ONE"},
			},
		}, // true
		{
			&ct.LabelFilterExpression{
				Op:     ct.LabelFilterExpressionOpIn,
				Key:    "two",
				Values: []string{"2", "TWO"},
			},
		}, // true
	}, labels), Equals, true) // filtered are ORed

	// Multiple Filters with Multiple Expressions
	c.Assert(s.matchLabelFilters(c, []ct.LabelFilter{
		{
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "one",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "two",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "three",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "foo",
			},
		}, // false
		{
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "one",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "two",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "three",
			},
		}, // true
	}, labels), Equals, true) // filtered are ORed
	c.Assert(s.matchLabelFilters(c, []ct.LabelFilter{
		{
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "one",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "two",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "three",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "foo",
			},
		}, // false
		{
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "one",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "two",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpExists,
				Key: "three",
			},
			&ct.LabelFilterExpression{
				Op:  ct.LabelFilterExpressionOpNotExists,
				Key: "four",
			},
		}, // false
	}, labels), Equals, false) // filtered are ORed

	// Empty List of Filters should always match
	c.Assert(s.matchLabelFilters(c, []ct.LabelFilter{}, labels), Equals, true)
}

func (s *S) TestRuntimeProfilesBootstrapped(c *C) {
	repo := NewRuntimeProfileRepo(s.db)
	list, err := repo.List()
	c.Assert(err, IsNil)
	c.Assert(len(list), Equals, 3)
	names := map[string]bool{}
	for _, p := range list {
		names[p.Name] = true
		c.Assert(p.Builtin, Equals, true)
		c.Assert(p.Memory > 0, Equals, true)
		c.Assert(p.CPU > 0, Equals, true)
	}
	c.Assert(names["small"], Equals, true)
	c.Assert(names["medium"], Equals, true)
	c.Assert(names["large"], Equals, true)
	var small *ct.RuntimeProfile
	for _, p := range list {
		if p.Name == "small" {
			small = p
		}
	}
	c.Assert(small, NotNil)
	c.Assert(small.Memory, Equals, int64(512*1024*1024))
	c.Assert(small.CPU, Equals, int64(500))
	c.Assert(small.ReserveResources, Equals, false)
	small.Memory = 768 * 1024 * 1024
	small.CPU = 750
	small.ReserveResources = true
	c.Assert(repo.Update(small), IsNil)
	gotSmall, err := repo.Get(small.ID)
	c.Assert(err, IsNil)
	c.Assert(gotSmall.Memory, Equals, int64(768*1024*1024))
	c.Assert(gotSmall.CPU, Equals, int64(750))
	c.Assert(gotSmall.Builtin, Equals, true)
	c.Assert(gotSmall.ReserveResources, Equals, true)

	settings, err := repo.Settings()
	c.Assert(err, IsNil)
	c.Assert(settings.AllowCustomLimits, Equals, false)
	c.Assert(settings.ReserveResources, Equals, false)
	c.Assert(settings.BlobGCKeep, Equals, 10)
	c.Assert(settings.BlobGCMaxAge, Equals, "")
	c.Assert(settings.JobRestartInterval, Equals, ct.DefaultJobRestartInterval)
	c.Assert(settings.AllowUserScale, Equals, true)
	c.Assert(settings.AllowUserScaleTags, Equals, false)

	custom := &ct.RuntimeProfile{Name: "xlarge", Memory: 4 * 1024 * 1024 * 1024, CPU: 4000}
	c.Assert(repo.Add(custom), IsNil)
	c.Assert(custom.ID, Not(Equals), "")
	got, err := repo.GetByName("xlarge")
	c.Assert(err, IsNil)
	c.Assert(got.Memory, Equals, custom.Memory)
	c.Assert(got.ReserveResources, Equals, false)

	c.Assert(repo.Delete(list[0].ID), NotNil) // builtin
	c.Assert(repo.Delete(custom.ID), IsNil)

	settings.AllowCustomLimits = true
	c.Assert(repo.UpdateSettings(settings), IsNil)
	updated, err := repo.Settings()
	c.Assert(err, IsNil)
	c.Assert(updated.AllowCustomLimits, Equals, true)
	c.Assert(updated.ReserveResources, Equals, false)

	updated.ReserveResources = true
	c.Assert(repo.UpdateSettings(updated), IsNil)
	reserved, err := repo.Settings()
	c.Assert(err, IsNil)
	c.Assert(reserved.ReserveResources, Equals, true)
	c.Assert(reserved.AllowCustomLimits, Equals, true)
	c.Assert(reserved.BlobGCKeep, Equals, 10)

	reserved.BlobGCKeep = 5
	reserved.BlobGCMaxAge = "720h"
	c.Assert(repo.UpdateSettings(reserved), IsNil)
	blobs, err := repo.Settings()
	c.Assert(err, IsNil)
	c.Assert(blobs.BlobGCKeep, Equals, 5)
	c.Assert(blobs.BlobGCMaxAge, Equals, "720h")
	c.Assert(blobs.AllowCustomLimits, Equals, true)
	c.Assert(blobs.JobRestartInterval, Equals, "24h")

	// an empty interval keeps the stored value; "0" turns restarts off
	blobs.JobRestartInterval = ""
	c.Assert(repo.UpdateSettings(blobs), IsNil)
	kept, err := repo.Settings()
	c.Assert(err, IsNil)
	c.Assert(kept.JobRestartInterval, Equals, "24h")

	kept.JobRestartInterval = "0"
	c.Assert(repo.UpdateSettings(kept), IsNil)
	off, err := repo.Settings()
	c.Assert(err, IsNil)
	c.Assert(off.JobRestartInterval, Equals, ct.JobRestartIntervalOff)
	c.Assert(off.JobRestartIntervalDuration(), Equals, time.Duration(0))

	off.JobRestartInterval = "5m"
	c.Assert(repo.UpdateSettings(off), NotNil)
}

func (s *S) TestDBRuntimesPersist(c *C) {
	repo := NewDBRuntimeRepo(s.db)
	empty, err := repo.Load()
	c.Assert(err, IsNil)
	c.Assert(len(empty.Runtimes), Equals, 0)
	c.Assert(empty.AllowCustomSizes, Equals, false)

	var cat dbruntime.Catalog
	c.Assert(cat.EnsureEngine("postgres"), IsNil)
	c.Assert(cat.Create(dbruntime.Runtime{Engine: "postgres", Name: "cache", CPU: 100, Memory: 128 << 20, Disk: 1 << 30}), IsNil)
	c.Assert(cat.Create(dbruntime.Runtime{Engine: "postgres", Name: "db-small", CPU: 500, Memory: 1 << 30, Disk: 10 << 30, Dedicated: true}), IsNil)
	cat.AllowCustomSizes = true
	c.Assert(repo.Replace(cat), IsNil)

	got, err := repo.Load()
	c.Assert(err, IsNil)
	c.Assert(got.AllowCustomSizes, Equals, true)
	if _, ok := got.Find("postgres", "small"); !ok {
		c.Fatal("builtin small missing")
	}
	if _, ok := got.Find("postgres", "cache"); !ok {
		c.Fatal("custom runtime missing")
	}
	dedicated, ok := got.Find("postgres", "db-small")
	if !ok || !dedicated.Dedicated {
		c.Fatal("dedicated runtime missing")
	}

	updated, err := repo.Mutate(func(next *dbruntime.Catalog) error {
		return next.RemoveEngine("postgres")
	})
	c.Assert(err, IsNil)
	if _, ok := updated.Find("postgres", "small"); ok {
		c.Fatal("remove engine left postgres runtimes")
	}
	again, err := repo.Load()
	c.Assert(err, IsNil)
	c.Assert(len(again.Runtimes), Equals, 0)
}

// Scheduler job fires record object_type "scheduler". events.object_type is a
// foreign key to event_types, and that row was missing, so smoke CLI checks
// failed with events_object_type_fkey (SQLSTATE 23503).
func (s *S) TestSchedulerEventTypeExists(c *C) {
	var n int
	c.Assert(s.db.QueryRow("SELECT count(*) FROM event_types WHERE name = $1", "scheduler").Scan(&n), IsNil)
	c.Assert(n, Equals, 1)
}

func (s *S) TestRuntimeEventTypeExists(c *C) {
	var n int
	c.Assert(s.db.QueryRow("SELECT count(*) FROM event_types WHERE name = $1", "runtime").Scan(&n), IsNil)
	c.Assert(n, Equals, 1)
}

func (s *S) TestRunnableFormationsIncludeOldRelease(c *C) {
	apps := NewAppRepo(s.db, "example.com", NewRouteRepo(s.db))
	artifacts := NewArtifactRepo(s.db)
	releases := NewReleaseRepo(s.db, artifacts, nil)
	formations := NewFormationRepo(s.db, apps, releases, artifacts)
	tenancy := NewTenancyRepo(s.db)

	owner := &ct.App{Name: "quota-owner", OwnerAccount: "user:quota-owner"}
	other := &ct.App{Name: "quota-other", OwnerAccount: "user:quota-other"}
	c.Assert(apps.Add(owner), IsNil)
	c.Assert(apps.Add(other), IsNil)

	limit := int64(256 * 1024 * 1024)
	procs := map[string]ct.ProcessType{
		"web": {Resources: resource.Resources{
			resource.TypeMemory: {Limit: &limit},
		}},
	}
	current := &ct.Release{AppID: owner.ID, Processes: procs}
	old := &ct.Release{AppID: owner.ID, Processes: procs}
	zero := &ct.Release{AppID: owner.ID, Processes: procs}
	foreign := &ct.Release{AppID: other.ID, Processes: procs}
	c.Assert(releases.Add(current), IsNil)
	c.Assert(releases.Add(old), IsNil)
	c.Assert(releases.Add(zero), IsNil)
	c.Assert(releases.Add(foreign), IsNil)
	c.Assert(apps.SetRelease(owner, current.ID), IsNil)

	scale := func(appID, releaseID string, n int) {
		counts := map[string]int{"web": n}
		_, err := formations.AddScaleRequest(&ct.ScaleRequest{
			AppID:        appID,
			ReleaseID:    releaseID,
			NewProcesses: &counts,
		}, false)
		c.Assert(err, IsNil)
	}
	scale(owner.ID, current.ID, 1)
	scale(owner.ID, old.ID, 2)
	scale(owner.ID, zero.ID, 0)
	scale(other.ID, foreign.ID, 4)

	rows, err := tenancy.RunnableFormations("user:quota-owner")
	c.Assert(err, IsNil)
	got := map[string]int{}
	for _, row := range rows {
		c.Assert(row.Owner, Equals, "user:quota-owner")
		got[row.ReleaseID] = row.Processes["web"]
		spec := row.Types["web"].Resources[resource.TypeMemory]
		c.Assert(spec.Limit, NotNil)
		c.Assert(*spec.Limit, Equals, limit)
	}
	c.Assert(got, HasLen, 2)
	c.Assert(got[current.ID], Equals, 1)
	c.Assert(got[old.ID], Equals, 2)

	zeros := map[string]int{"web": 0}
	_, err = formations.AddScaleRequest(&ct.ScaleRequest{
		AppID:        owner.ID,
		ReleaseID:    old.ID,
		NewProcesses: &zeros,
	}, true)
	c.Assert(err, IsNil)
	rows, err = tenancy.RunnableFormations("user:quota-owner")
	c.Assert(err, IsNil)
	c.Assert(rows, HasLen, 1)
	c.Assert(rows[0].ReleaseID, Equals, current.ID)
}

func (s *S) TestPostgresAvailabilityRoundTrip(c *C) {
	repo := NewTenancyRepo(s.db)
	got, err := repo.GetPostgresAvailability()
	c.Assert(err, IsNil)
	c.Assert(got, Equals, "manual")

	c.Assert(repo.SetPostgresAvailability("system"), IsNil)
	got, err = repo.GetPostgresAvailability()
	c.Assert(err, IsNil)
	c.Assert(got, Equals, "system")

	// tenancy:mode must not rewrite the stored value.
	c.Assert(repo.SetSettings("hosted", nil), IsNil)
	got, err = repo.GetPostgresAvailability()
	c.Assert(err, IsNil)
	c.Assert(got, Equals, "system")

	c.Assert(repo.SetPostgresAvailability("manual"), IsNil)
	c.Assert(repo.SetSettings("self_hosted", nil), IsNil)
	got, err = repo.GetPostgresAvailability()
	c.Assert(err, IsNil)
	c.Assert(got, Equals, "manual")

	c.Assert(repo.SetPostgresAvailability("nope"), NotNil)
	got, err = repo.GetPostgresAvailability()
	c.Assert(err, IsNil)
	c.Assert(got, Equals, "manual")
}

func (s *S) TestEngineAvailabilityRoundTrip(c *C) {
	repo := NewTenancyRepo(s.db)
	for _, engine := range []struct {
		get func() (string, error)
		set func(string) error
	}{
		{repo.GetMySQLAvailability, repo.SetMySQLAvailability},
		{repo.GetRedisAvailability, repo.SetRedisAvailability},
		{repo.GetMongoDBAvailability, repo.SetMongoDBAvailability},
		{repo.GetKafkaAvailability, repo.SetKafkaAvailability},
		{repo.GetClickHouseAvailability, repo.SetClickHouseAvailability},
	} {
		got, err := engine.get()
		c.Assert(err, IsNil)
		c.Assert(got, Equals, "manual")

		c.Assert(engine.set("system"), IsNil)
		got, err = engine.get()
		c.Assert(err, IsNil)
		c.Assert(got, Equals, "system")

		c.Assert(repo.SetSettings("hosted", nil), IsNil)
		got, err = engine.get()
		c.Assert(err, IsNil)
		c.Assert(got, Equals, "system")

		c.Assert(engine.set("manual"), IsNil)
		c.Assert(repo.SetSettings("self_hosted", nil), IsNil)
		got, err = engine.get()
		c.Assert(err, IsNil)
		c.Assert(got, Equals, "manual")

		c.Assert(engine.set("nope"), NotNil)
		got, err = engine.get()
		c.Assert(err, IsNil)
		c.Assert(got, Equals, "manual")
	}
}
