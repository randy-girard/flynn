package main

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	docopt "github.com/flynn/go-docopt"
	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/blobstore/backend"
	"github.com/randy-girard/flynn/blobstore/data"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/random"
	pgtestutils "github.com/randy-girard/flynn/pkg/testutils/postgres"
)

// singleConnDB is a migrated test database behind a pool of one connection
// that gives up after a short wait, so a transaction that is never ended
// shows up as an acquire timeout on the next call instead of a hang.
func singleConnDB(t *testing.T) *postgres.DB {
	t.Helper()
	// its own database, so a leaked connection cannot block the setup of
	// the tests that run after this one
	const dbname = "blobstore_txleak"
	setup := createDB(t, dbname)
	migrateDB(t, setup)
	setup.Close()
	cfg, err := pgtestutils.ConnConfigForDatabase(dbname)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgx.NewConnPool(pgx.ConnPoolConfig{
		ConnConfig:     cfg,
		MaxConnections: 1,
		AcquireTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	db := postgres.New(pool, nil)
	t.Cleanup(func() {
		// Close waits for every connection to be released, which never
		// happens when a transaction leaked, so do not let it hang the run
		done := make(chan struct{})
		go func() { db.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("closing the pool timed out: a connection is still held")
		}
	})
	return db
}

func TestCopyMissingSourceReleasesTx(t *testing.T) {
	db := singleConnDB(t)
	r := data.NewFileRepo(db, []backend.Backend{backend.Postgres}, "postgres")
	missing := "/missing-" + random.String(8)

	for i := 0; i < 3; i++ {
		if err := r.Copy("/copy-"+random.String(8), missing); err != backend.ErrNotFound {
			t.Fatalf("copy %d of a missing source: expected ErrNotFound, got %v", i, err)
		}
	}
	// with the only connection still held by a leaked transaction this
	// times out acquiring a connection
	name := "/after-" + random.String(8)
	if err := r.Put(name, bytes.NewReader([]byte("x")), 0, "text/plain"); err != nil {
		t.Fatalf("put after copies of a missing source: %s", err)
	}
}

func TestDeleteSkipsUnknownBackend(t *testing.T) {
	db := initDB(t)
	defer db.Close()
	ext := newFakeExternalBackend()
	dir := "/del-" + random.String(8)

	// one object on an external backend, one in postgres
	old := data.NewFileRepo(db, []backend.Backend{backend.Postgres, ext}, ext.Name())
	if err := old.Put(dir+"/ext", bytes.NewReader([]byte("e")), 0, "text/plain"); err != nil {
		t.Fatal(err)
	}
	pg := data.NewFileRepo(db, []backend.Backend{backend.Postgres}, "postgres")
	if err := pg.Put(dir+"/pg", bytes.NewReader([]byte("p")), 0, "text/plain"); err != nil {
		t.Fatal(err)
	}

	// the external backend is no longer configured
	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("delete with an unconfigured backend panicked: %v", p)
			}
		}()
		return pg.Delete(dir)
	}()
	if err == nil || !strings.Contains(err.Error(), `unknown backend "fake-external"`) {
		t.Fatalf("expected an unknown backend error, got %v", err)
	}

	for _, name := range []string{dir + "/ext", dir + "/pg"} {
		if _, err := pg.Get(name, false); err != backend.ErrNotFound {
			t.Fatalf("%s: expected deleted, got %v", name, err)
		}
	}

	// the external object is still listed for cleanup, and a repo that has
	// the backend again finds it there
	files, err := old.ListDeletedFilesForCleanup()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range files {
		if f.FileInfo.Name == dir+"/ext" && f.Backend != nil && f.Backend.Name() == ext.Name() {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %s/ext to stay listed for cleanup", dir)
	}
	if len(ext.deleted) != 0 {
		t.Fatalf("nothing should have been deleted from the unconfigured backend: %v", ext.deleted)
	}
}

func TestDeleteReturnsEveryBackendError(t *testing.T) {
	db := initDB(t)
	defer db.Close()
	ext := newFakeExternalBackend()
	dir := "/delerr-" + random.String(8)
	r := data.NewFileRepo(db, []backend.Backend{backend.Postgres, ext}, ext.Name())
	for _, n := range []string{"/a", "/b"} {
		if err := r.Put(dir+n, bytes.NewReader([]byte("x")), 0, "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	ext.failDel = true
	err := r.Delete(dir)
	if err == nil {
		t.Fatal("expected the failed backend deletes to be returned")
	}
	if n := strings.Count(err.Error(), "fake delete failed"); n != 2 {
		t.Fatalf("expected both delete errors, got %d in %q", n, err)
	}
}

type fakeCleanupRepo struct {
	mtx      sync.Mutex
	files    []data.BackendFile
	listErr  error
	purged   []string
	purgeErr error
}

func (r *fakeCleanupRepo) ListDeletedFilesForCleanup() ([]data.BackendFile, error) {
	return r.files, r.listErr
}

func (r *fakeCleanupRepo) PurgeDeleted(id string) error {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	if r.purgeErr != nil {
		return r.purgeErr
	}
	r.purged = append(r.purged, id)
	return nil
}

func stubCleanupOpen(t *testing.T, repo cleanupRepo, err error) {
	old := cleanupOpen
	cleanupOpen = func() (*postgres.DB, cleanupRepo, error) { return nil, repo, err }
	t.Cleanup(func() { cleanupOpen = old })
}

func cleanupArgs() *docopt.Args {
	return &docopt.Args{String: map[string]string{"--concurrency": "2"}}
}

func TestRunCleanupReturnsDeleteErrors(t *testing.T) {
	good := &fakeBackend{name: "s3"}
	bad := &fakeBackend{name: "gcs", delErr: errors.New("access denied")}
	repo := &fakeCleanupRepo{files: []data.BackendFile{
		{Backend: good, FileInfo: backend.FileInfo{ID: "1", Name: "/a"}},
		{Backend: bad, FileInfo: backend.FileInfo{ID: "2", Name: "/b"}},
		{Backend: bad, FileInfo: backend.FileInfo{ID: "3", Name: "/c"}},
	}}
	stubCleanupOpen(t, repo, nil)

	err := runCleanup(cleanupArgs())
	if err == nil || !strings.Contains(err.Error(), "2 of 3") {
		t.Fatalf("expected cleanup to report 2 of 3 failed, got %v", err)
	}
	if good.deletes != 1 || bad.deletes != 2 {
		t.Fatalf("deletes good=%d bad=%d", good.deletes, bad.deletes)
	}
	// only the row whose object is gone is removed; the others stay listed
	if len(repo.purged) != 1 || repo.purged[0] != "1" {
		t.Fatalf("expected only file 1 to be purged, got %v", repo.purged)
	}
}

func TestRunCleanupReturnsPurgeErrors(t *testing.T) {
	good := &fakeBackend{name: "s3"}
	repo := &fakeCleanupRepo{
		files:    []data.BackendFile{{Backend: good, FileInfo: backend.FileInfo{ID: "1", Name: "/a"}}},
		purgeErr: errors.New("connection lost"),
	}
	stubCleanupOpen(t, repo, nil)
	if err := runCleanup(cleanupArgs()); err == nil || !strings.Contains(err.Error(), "1 of 1") {
		t.Fatalf("expected a purge failure to fail the run, got %v", err)
	}
}

func TestPurgeDeletedRemovesOnlyDeletedRows(t *testing.T) {
	db := initDB(t)
	defer db.Close()
	ext := newFakeExternalBackend()
	r := data.NewFileRepo(db, []backend.Backend{backend.Postgres, ext}, ext.Name())
	dir := "/purge-" + random.String(8)
	for _, n := range []string{"/gone", "/live"} {
		if err := r.Put(dir+n, bytes.NewReader([]byte("x")), 0, "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Delete(dir + "/gone"); err != nil {
		t.Fatal(err)
	}
	live, err := r.Get(dir+"/live", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PurgeDeleted(live.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(dir+"/live", false); err != nil {
		t.Fatalf("a live row must not be purged: %v", err)
	}

	listed := func() []string {
		files, err := r.ListDeletedFilesForCleanup()
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, f := range files {
			if strings.HasPrefix(f.FileInfo.Name, dir) {
				ids = append(ids, f.ID)
			}
		}
		return ids
	}
	ids := listed()
	if len(ids) != 1 {
		t.Fatalf("expected one deleted row, got %v", ids)
	}
	if err := r.PurgeDeleted(ids[0]); err != nil {
		t.Fatal(err)
	}
	if ids := listed(); len(ids) != 0 {
		t.Fatalf("expected the purged row to be gone, got %v", ids)
	}
}

func TestRunCleanupCountsUnconfiguredBackend(t *testing.T) {
	good := &fakeBackend{name: "s3"}
	repo := &fakeCleanupRepo{files: []data.BackendFile{
		{Backend: good, FileInfo: backend.FileInfo{ID: "1", Name: "/a"}},
		{FileInfo: backend.FileInfo{ID: "2", Name: "/b"}},
	}}
	stubCleanupOpen(t, repo, nil)

	err := runCleanup(cleanupArgs())
	if err == nil || !strings.Contains(err.Error(), "1 of 2") {
		t.Fatalf("expected a skipped file to fail the run, got %v", err)
	}
}

func TestRunCleanupSucceedsWhenEveryDeleteWorks(t *testing.T) {
	good := &fakeBackend{name: "s3"}
	repo := &fakeCleanupRepo{files: []data.BackendFile{
		{Backend: good, FileInfo: backend.FileInfo{ID: "1", Name: "/a"}},
		{Backend: good, FileInfo: backend.FileInfo{ID: "2", Name: "/b"}},
	}}
	stubCleanupOpen(t, repo, nil)
	if err := runCleanup(cleanupArgs()); err != nil {
		t.Fatal(err)
	}
	if good.deletes != 2 {
		t.Fatalf("deletes=%d", good.deletes)
	}
}

func TestRunCleanupReturnsOpenAndListErrors(t *testing.T) {
	want := errors.New("unknown default backend")
	stubCleanupOpen(t, nil, want)
	if err := runCleanup(cleanupArgs()); !errors.Is(err, want) {
		t.Fatalf("expected open error, got %v", err)
	}
	stubCleanupOpen(t, &fakeCleanupRepo{listErr: errors.New("connection refused")}, nil)
	if err := runCleanup(cleanupArgs()); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected list error, got %v", err)
	}
}

// Rows carried over from the first schema can have a NULL size. Checking the
// scan error must not make `flynn-blobstore migrate` refuse to list them.
func TestListFilesExcludingDefaultBackendAcceptsNullSize(t *testing.T) {
	db := initDB(t)
	defer db.Close()
	name := "/legacy-" + random.String(8)
	if err := db.Exec("INSERT INTO files (backend, name, type) VALUES ('fake-external', $1, 'text/plain')", name); err != nil {
		t.Fatal(err)
	}
	ext := newFakeExternalBackend()
	r := data.NewFileRepo(db, []backend.Backend{backend.Postgres, ext}, "postgres")
	files, err := r.ListFilesExcludingDefaultBackend(name)
	if err != nil {
		t.Fatalf("listing a NULL-size row: %s", err)
	}
	if len(files) != 1 || files[0].FileInfo.Name != name || files[0].Size != 0 || files[0].Backend != ext {
		t.Fatalf("unexpected listing %+v", files)
	}
}
