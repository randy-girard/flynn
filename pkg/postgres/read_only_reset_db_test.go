package postgres

import (
	"testing"

	"github.com/jackc/pgx"
	pgtestutils "github.com/randy-girard/flynn/pkg/testutils/postgres"
)

// NEW-46: after a failover the controller kept a pooled connection to the
// old primary, now read-only, for about a minute: every write on it failed
// with read_only_sql_transaction (25006) while new connections would have
// reached the new primary. A 25006 must drop the pool's connections so the
// next query dials the leader again.
//
// The read-only peer is a pooled session with default_transaction_read_only
// set; the database itself (what a new connection reaches) is read-write.
func TestReadOnlyErrorResetsThePool(t *testing.T) {
	const dbname = "pkg_postgres_read_only_reset_test"
	if err := pgtestutils.SetupPostgres(dbname); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgtestutils.ConnConfigForDatabase(dbname)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgx.NewConnPool(pgx.ConnPoolConfig{ConnConfig: cfg, MaxConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := New(pool, nil)
	if err := db.Exec("CREATE TABLE t (id int)"); err != nil {
		t.Fatal(err)
	}
	readOnlySession := func() {
		t.Helper()
		if err := db.Exec("SET SESSION default_transaction_read_only = on"); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range []struct {
		name  string
		write func() error
	}{
		{"Exec", func() error { return db.Exec("INSERT INTO t (id) VALUES (0)") }},
		{"QueryRow", func() error {
			var n int
			return db.QueryRow("INSERT INTO t (id) VALUES (1) RETURNING id").Scan(&n)
		}},
		{"Query", func() error {
			rows, err := db.Query("INSERT INTO t (id) VALUES (2) RETURNING id")
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
			}
			return rows.Err()
		}},
		{"Tx", func() error {
			tx, err := db.Begin()
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err := tx.Exec("INSERT INTO t (id) VALUES (3)"); err != nil {
				return err
			}
			return tx.Commit()
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			readOnlySession()
			err := c.write()
			if !IsPostgresCode(err, ReadOnlySQLTransaction) {
				t.Fatalf("write on the read-only session: %v, want 25006", err)
			}
			if err := c.write(); err != nil {
				t.Fatalf("second write still failed (pool kept the read-only connection): %v", err)
			}
		})
	}
}
