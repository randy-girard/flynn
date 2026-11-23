package main

import (
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"sync"
	"testing"

	"github.com/randy-girard/flynn/blobstore/backend"
	"github.com/randy-girard/flynn/blobstore/data"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/random"
)

// fakeExternalBackend stands in for S3, GCS, Azure and MinIO: each Put stores
// the data under the fresh external id the repo chose, so the old object and
// the new one live side by side until the old one is deleted.
type fakeExternalBackend struct {
	mtx     sync.Mutex
	objects map[string][]byte
	failPut bool
	// failAfterStore stores the object, then reports an error, like an
	// upload whose last request or commit failed after the data arrived
	failAfterStore bool
	failDel        bool
	deleted        []string
}

func newFakeExternalBackend() *fakeExternalBackend {
	return &fakeExternalBackend{objects: make(map[string][]byte)}
}

func (b *fakeExternalBackend) Name() string { return "fake-external" }

func (b *fakeExternalBackend) Put(_ *postgres.DB, info backend.FileInfo, r io.Reader) error {
	if info.ExternalID == "" {
		return errors.New("put without an external id")
	}
	buf, err := ioutil.ReadAll(r)
	if err != nil {
		return err
	}
	b.mtx.Lock()
	defer b.mtx.Unlock()
	if b.failPut {
		// an interrupted upload: part of the body arrived, nothing committed
		return errors.New("fake upload interrupted")
	}
	b.objects[info.ExternalID] = buf
	if b.failAfterStore {
		return errors.New("fake upload failed after storing")
	}
	return nil
}

func (b *fakeExternalBackend) Open(tx *postgres.DBTx, info backend.FileInfo, txControl bool) (backend.FileStream, error) {
	if txControl {
		tx.Rollback()
	}
	b.mtx.Lock()
	defer b.mtx.Unlock()
	buf, ok := b.objects[info.ExternalID]
	if !ok {
		return nil, backend.ErrNotFound
	}
	return nopCloseReadSeeker{bytes.NewReader(buf)}, nil
}

func (b *fakeExternalBackend) Copy(tx *postgres.DBTx, dst, src backend.FileInfo) error {
	return errors.New("not implemented")
}

func (b *fakeExternalBackend) Delete(tx *postgres.DBTx, info backend.FileInfo) error {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	if b.failDel {
		return errors.New("fake delete failed")
	}
	b.deleted = append(b.deleted, info.ExternalID)
	delete(b.objects, info.ExternalID)
	return nil
}

func (b *fakeExternalBackend) count() int {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	return len(b.objects)
}

type nopCloseReadSeeker struct{ *bytes.Reader }

func (nopCloseReadSeeker) Close() error { return nil }

func newFakeExternalRepo(t *testing.T) (*data.FileRepo, *fakeExternalBackend, func()) {
	db := initDB(t)
	b := newFakeExternalBackend()
	return data.NewFileRepo(db, []backend.Backend{backend.Postgres, b}, b.Name()), b, db.Close
}

func readBlob(t *testing.T, r *data.FileRepo, name string) string {
	t.Helper()
	f, err := r.Get(name, true)
	if err != nil {
		t.Fatalf("get %s: %s", name, err)
	}
	defer f.Close()
	buf, err := ioutil.ReadAll(f)
	if err != nil {
		t.Fatalf("read %s: %s", name, err)
	}
	return string(buf)
}

func TestPutOverwriteKeepsOldObjectWhenUploadFails(t *testing.T) {
	r, b, done := newFakeExternalRepo(t)
	defer done()
	name := "/repos/" + random.String(8) + ".tar"

	if err := r.Put(name, bytes.NewReader([]byte("old")), 0, "application/x-tar"); err != nil {
		t.Fatal(err)
	}

	b.mtx.Lock()
	var oldIDs []string
	for id := range b.objects {
		oldIDs = append(oldIDs, id)
	}
	b.mtx.Unlock()

	b.failPut = true
	if err := r.Put(name, bytes.NewReader([]byte("new")), 0, "application/x-tar"); err == nil {
		t.Fatal("expected the interrupted overwrite to fail")
	}
	b.failPut = false

	// only the failed upload's own object may be deleted (EDGE-32)
	for _, id := range oldIDs {
		for _, d := range b.deleted {
			if d == id {
				t.Fatalf("old object %s was deleted from the backend although the overwrite failed", id)
			}
		}
	}
	if got := readBlob(t, r, name); got != "old" {
		t.Fatalf("after failed overwrite expected old content, got %q", got)
	}
}

func TestPutOverwriteDeletesOldObjectAfterCommit(t *testing.T) {
	r, b, done := newFakeExternalRepo(t)
	defer done()
	name := "/repos/" + random.String(8) + ".tar"

	if err := r.Put(name, bytes.NewReader([]byte("old")), 0, "application/x-tar"); err != nil {
		t.Fatal(err)
	}
	if err := r.Put(name, bytes.NewReader([]byte("new")), 0, "application/x-tar"); err != nil {
		t.Fatal(err)
	}
	if got := readBlob(t, r, name); got != "new" {
		t.Fatalf("after overwrite expected new content, got %q", got)
	}
	if len(b.deleted) != 1 {
		t.Fatalf("expected the old object to be deleted once, got %v", b.deleted)
	}
	if n := b.count(); n != 1 {
		t.Fatalf("expected only the new object to remain in the backend, got %d", n)
	}
}

func TestPutOverwriteDeleteFailureLeavesRowForCleanup(t *testing.T) {
	r, b, done := newFakeExternalRepo(t)
	defer done()
	name := "/repos/" + random.String(8) + ".tar"

	if err := r.Put(name, bytes.NewReader([]byte("old")), 0, "application/x-tar"); err != nil {
		t.Fatal(err)
	}
	b.failDel = true
	if err := r.Put(name, bytes.NewReader([]byte("new")), 0, "application/x-tar"); err != nil {
		t.Fatalf("a failed delete of the replaced object must not fail the overwrite: %s", err)
	}
	if got := readBlob(t, r, name); got != "new" {
		t.Fatalf("after overwrite expected new content, got %q", got)
	}

	files, err := r.ListDeletedFilesForCleanup()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range files {
		if f.FileInfo.Name == name && f.Backend != nil && f.Backend.Name() == b.Name() {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the replaced object of %s to be listed for `flynn-blobstore cleanup`", name)
	}
}
