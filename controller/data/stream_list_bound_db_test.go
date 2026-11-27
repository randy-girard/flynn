package data

import (
	"testing"
	"time"

	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/pkg/postgres"
	pgtestutils "github.com/randy-girard/flynn/pkg/testutils/postgres"
)

// NEW-09: the scheduler's formation and volume streams start with a list
// query. When that query blocks (here on a lock another transaction holds),
// the controller used to keep the stream open with keepalives and never
// answer. The list must fail on the server within streamListTimeout, so the
// stream ends with an error and the client reconnects.
func TestStreamListQueriesAreBoundedServerSide(t *testing.T) {
	const dbname = "controller_stream_list_bound_test"
	db := smallPoolDB(t, dbname)
	cfg, err := pgtestutils.ConnConfigForDatabase(dbname)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := NewArtifactRepo(db)
	apps := NewAppRepo(db, "example.com", NewRouteRepo(db))
	releases := NewReleaseRepo(db, artifacts, nil)
	formations := NewFormationRepo(db, apps, releases, artifacts)
	vols := NewVolumeRepo(db)

	old := streamListTimeout
	streamListTimeout = 300 * time.Millisecond
	t.Cleanup(func() { streamListTimeout = old })

	for _, c := range []struct {
		name  string
		table string
		list  func() error
	}{
		{"formations", "formations", func() error { _, err := formations.ListSince(time.Time{}); return err }},
		{"volumes", "volumes", func() error { _, err := vols.ListSince(time.Time{}); return err }},
	} {
		t.Run(c.name, func(t *testing.T) {
			// A pooled connection prepares its statements when it is
			// opened, which would wait on the lock too. Use the pool once
			// so the list runs on a prepared connection, and hold the
			// lock from a connection outside the pool.
			if err := c.list(); err != nil {
				t.Fatal(err)
			}
			conn, err := pgx.Connect(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			lock, err := conn.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback()
			if _, err := lock.Exec("LOCK TABLE " + c.table + " IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}

			done := make(chan error, 1)
			start := time.Now()
			go func() { done <- c.list() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("ListSince succeeded while the table was locked")
				}
				if !postgres.IsPostgresCode(err, "57014") {
					t.Fatalf("ListSince error %v, want query_canceled (57014)", err)
				}
				if d := time.Since(start); d > 3*time.Second {
					t.Fatalf("ListSince took %s, want about %s", d, streamListTimeout)
				}
			case <-time.After(5 * time.Second):
				lock.Rollback()
				<-done
				t.Fatal("ListSince was still waiting on the lock after 5s: no server-side bound")
			}

			// without the lock the same call works, and the timeout does
			// not leak into later queries on the pooled connection
			lock.Rollback()
			if err := c.list(); err != nil {
				t.Fatalf("ListSince after the lock was released: %v", err)
			}
			if err := db.Exec("SELECT pg_sleep(0.5)"); err != nil {
				t.Fatalf("statement_timeout leaked to the pool: %v", err)
			}
		})
	}
}
