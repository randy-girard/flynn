package data

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/random"
	pgtestutils "github.com/randy-girard/flynn/pkg/testutils/postgres"
	router "github.com/randy-girard/flynn/router/types"
)

// smallPoolDB returns a migrated database behind a pool the same size as a
// controller process (postgres.MaxPoolConnections) with a short acquire
// timeout, so a leaked transaction shows up as a timeout instead of a hang.
func smallPoolDB(t *testing.T, dbname string) *postgres.DB {
	t.Helper()
	if err := pgtestutils.SetupPostgres(dbname); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgtestutils.ConnConfigForDatabase(dbname)
	if err != nil {
		t.Fatal(err)
	}
	migratePool, err := pgx.NewConnPool(pgx.ConnPoolConfig{ConnConfig: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateDB(postgres.New(migratePool, nil)); err != nil {
		t.Fatal(err)
	}
	migratePool.Close()
	pool, err := pgx.NewConnPool(pgx.ConnPoolConfig{
		ConnConfig:     cfg,
		AfterConnect:   PrepareStatements,
		MaxConnections: postgres.MaxPoolConnections,
		AcquireTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Close waits for checked-out connections, so it would hang on a leak.
	t.Cleanup(func() { go pool.Close() })
	return postgres.New(pool, nil)
}

// withDeadline fails the test instead of hanging when a leak has used up
// the pool.
func withDeadline(t *testing.T, d time.Duration, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(d):
		t.Fatalf("still blocked after %s: the connection pool is exhausted", d)
	}
}

func assertPoolFree(t *testing.T, db *postgres.DB, apps *AppRepo) {
	t.Helper()
	if stat := db.ConnPool.Stat(); stat.CurrentConnections-stat.AvailableConnections != 0 {
		t.Errorf("%d pool connections still checked out after rejected requests", stat.CurrentConnections-stat.AvailableConnections)
	}
	app := &ct.App{Name: "ok-" + random.String(8)}
	withDeadline(t, 10*time.Second, func() error {
		if err := apps.Add(app); err != nil {
			return fmt.Errorf("valid app add after rejected requests: %s", err)
		}
		if _, err := apps.Get(app.ID); err != nil {
			return fmt.Errorf("app get after rejected requests: %s", err)
		}
		return nil
	})
}

// TestAppAddInvalidNameReleasesConnection: a rejected app name used to return
// from AppRepo.Add with the transaction open, so a handful of requests used
// up the whole pool and every later query timed out (CORE-22).
func TestAppAddInvalidNameReleasesConnection(t *testing.T) {
	db := smallPoolDB(t, "controllertest_tx_leak_app")
	apps := NewAppRepo(db, "example.com", NewRouteRepo(db))
	bad := []string{"controller", "router", "discoverd", random.UUID(), "Not_Valid", "controller"}
	for i := 0; i <= postgres.MaxPoolConnections; i++ {
		name := bad[i%len(bad)]
		if err := apps.Add(&ct.App{Name: name}); err == nil {
			t.Fatalf("expected %q to be rejected", name)
		} else if _, ok := err.(ct.ValidationError); !ok {
			t.Fatalf("expected a validation error for %q, got %T %s", name, err, err)
		}
	}
	assertPoolFree(t, db, apps)
}

// TestRouteAddInvalidTypeReleasesConnection covers the `default:` branch of
// RouteRepo.Add, which returned ErrRouteInvalid with the transaction open.
func TestRouteAddInvalidTypeReleasesConnection(t *testing.T) {
	db := smallPoolDB(t, "controllertest_tx_leak_route")
	routes := NewRouteRepo(db)
	apps := NewAppRepo(db, "example.com", routes)
	for i := 0; i <= postgres.MaxPoolConnections; i++ {
		if err := routes.Add(&router.Route{Type: "bogus"}); err != ErrRouteInvalid {
			t.Fatalf("expected ErrRouteInvalid, got %v", err)
		}
	}
	assertPoolFree(t, db, apps)
}

// TestAppAddLegitNamesStillWork: moving validation ahead of Begin must not
// reject generated names or reserved names on system apps.
func TestAppAddLegitNamesStillWork(t *testing.T) {
	db := smallPoolDB(t, "controllertest_tx_leak_legit")
	apps := NewAppRepo(db, "example.com", NewRouteRepo(db))
	generated := &ct.App{}
	if err := apps.Add(generated); err != nil {
		t.Fatalf("generated name: %s", err)
	}
	if generated.Name == "" {
		t.Fatal("expected a generated name")
	}
	system := &ct.App{Name: "controller", Meta: map[string]string{"flynn-system-app": "true"}}
	if err := apps.Add(system); err != nil {
		t.Fatalf("reserved name on a system app: %s", err)
	}
	dup := &ct.App{Name: generated.Name}
	if err := apps.Add(dup); err == nil {
		t.Fatal("expected a duplicate name to be rejected")
	}
	assertPoolFree(t, db, apps)
}
