package data

import (
	. "github.com/flynn/go-check"
	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
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
	small.Memory = 768 * 1024 * 1024
	small.CPU = 750
	c.Assert(repo.Update(small), IsNil)
	gotSmall, err := repo.Get(small.ID)
	c.Assert(err, IsNil)
	c.Assert(gotSmall.Memory, Equals, int64(768*1024*1024))
	c.Assert(gotSmall.CPU, Equals, int64(750))
	c.Assert(gotSmall.Builtin, Equals, true)

	settings, err := repo.Settings()
	c.Assert(err, IsNil)
	c.Assert(settings.AllowCustomLimits, Equals, false)

	custom := &ct.RuntimeProfile{Name: "xlarge", Memory: 4 * 1024 * 1024 * 1024, CPU: 4000}
	c.Assert(repo.Add(custom), IsNil)
	c.Assert(custom.ID, Not(Equals), "")
	got, err := repo.GetByName("xlarge")
	c.Assert(err, IsNil)
	c.Assert(got.Memory, Equals, custom.Memory)

	c.Assert(repo.Delete(list[0].ID), NotNil) // builtin
	c.Assert(repo.Delete(custom.ID), IsNil)

	settings.AllowCustomLimits = true
	c.Assert(repo.UpdateSettings(settings), IsNil)
	updated, err := repo.Settings()
	c.Assert(err, IsNil)
	c.Assert(updated.AllowCustomLimits, Equals, true)
}

// Scheduler job fires record object_type "scheduler". events.object_type is a
// foreign key to event_types, and that row was missing, so smoke CLI checks
// failed with events_object_type_fkey (SQLSTATE 23503).
func (s *S) TestSchedulerEventTypeExists(c *C) {
	var n int
	c.Assert(s.db.QueryRow("SELECT count(*) FROM event_types WHERE name = $1", "scheduler").Scan(&n), IsNil)
	c.Assert(n, Equals, 1)
}
