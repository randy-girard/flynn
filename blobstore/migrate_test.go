package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	docopt "github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/blobstore/backend"
	"github.com/randy-girard/flynn/blobstore/data"
	"github.com/randy-girard/flynn/pkg/postgres"
)

type fakeStream struct{ *bytes.Reader }

func (fakeStream) Close() error { return nil }

type fakeBackend struct {
	mtx     sync.Mutex
	name    string
	puts    int
	deletes int
	delErr  error
}

func (b *fakeBackend) Name() string { return b.name }
func (b *fakeBackend) Open(tx *postgres.DBTx, info backend.FileInfo, txControl bool) (backend.FileStream, error) {
	return fakeStream{bytes.NewReader([]byte("data"))}, nil
}
func (b *fakeBackend) Put(db *postgres.DB, info backend.FileInfo, r io.Reader) error {
	b.puts++
	_, err := io.Copy(io.Discard, r)
	return err
}
func (b *fakeBackend) Copy(tx *postgres.DBTx, dst, src backend.FileInfo) error { return nil }
func (b *fakeBackend) Delete(tx *postgres.DBTx, info backend.FileInfo) error {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	b.deletes++
	return b.delErr
}

type fakeMigrateRepo struct {
	dest    backend.Backend
	files   []data.BackendFile
	listErr error
}

func (r *fakeMigrateRepo) DefaultBackend() backend.Backend { return r.dest }
func (r *fakeMigrateRepo) ListFilesExcludingDefaultBackend(prefix string) ([]data.BackendFile, error) {
	return r.files, r.listErr
}
func (r *fakeMigrateRepo) SetBackend(tx *postgres.DBTx, id string, dst backend.Backend, info backend.FileInfo) error {
	return nil
}

func migrateArgs() *docopt.Args {
	return &docopt.Args{
		Bool:   map[string]bool{"--delete": true},
		String: map[string]string{"--concurrency": "1", "--prefix": ""},
	}
}

func stubMigrateOpen(t *testing.T, f func() (*postgres.DB, migrateRepo, error)) {
	old := migrateOpen
	migrateOpen = f
	t.Cleanup(func() { migrateOpen = old })
}

func TestRunMigrateReturnsRepoError(t *testing.T) {
	want := errors.New("unknown default backend")
	stubMigrateOpen(t, func() (*postgres.DB, migrateRepo, error) { return nil, nil, want })
	if err := runMigrate(migrateArgs()); !errors.Is(err, want) {
		t.Fatalf("expected repo error %v, got %v", want, err)
	}
}

func TestRunMigrateReturnsListError(t *testing.T) {
	repo := &fakeMigrateRepo{dest: &fakeBackend{name: "s3"}, listErr: errors.New("connection refused")}
	stubMigrateOpen(t, func() (*postgres.DB, migrateRepo, error) { return nil, repo, nil })
	err := runMigrate(migrateArgs())
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected list error, got %v", err)
	}
}

func TestMoveFileDoesNotDeleteSourceWhenCommitFails(t *testing.T) {
	db := createDB(t, "blobstore_migrate")
	defer db.Close()
	src := &fakeBackend{name: "postgres"}
	dest := &fakeBackend{name: "s3"}
	repo := &fakeMigrateRepo{dest: dest}
	f := data.BackendFile{Backend: src, FileInfo: backend.FileInfo{ID: "1", Name: "/a"}}

	want := errors.New("commit failed")
	old := commitMove
	commitMove = func(tx *postgres.DBTx) error { tx.Rollback(); return want }
	defer func() { commitMove = old }()

	if err := moveFile(db, repo, f, true); !errors.Is(err, want) {
		t.Fatalf("expected commit error, got %v", err)
	}
	if src.deletes != 0 {
		t.Fatalf("source deleted %d times although the commit failed", src.deletes)
	}
}

func TestMoveFileDeletesSourceAfterCommit(t *testing.T) {
	db := createDB(t, "blobstore_migrate")
	defer db.Close()
	src := &fakeBackend{name: "postgres"}
	dest := &fakeBackend{name: "s3"}
	repo := &fakeMigrateRepo{dest: dest}
	f := data.BackendFile{Backend: src, FileInfo: backend.FileInfo{ID: "1", Name: "/a"}}

	committed := false
	old := commitMove
	commitMove = func(tx *postgres.DBTx) error {
		if src.deletes != 0 {
			t.Error("source deleted before commit")
		}
		committed = true
		return tx.Commit()
	}
	defer func() { commitMove = old }()

	if err := moveFile(db, repo, f, true); err != nil {
		t.Fatal(err)
	}
	if !committed || src.deletes != 1 || dest.puts != 1 {
		t.Fatalf("committed=%v deletes=%d puts=%d", committed, src.deletes, dest.puts)
	}

	// without --delete the source is kept
	src.deletes = 0
	if err := moveFile(db, repo, f, false); err != nil {
		t.Fatal(err)
	}
	if src.deletes != 0 {
		t.Fatalf("source deleted without --delete")
	}
}
