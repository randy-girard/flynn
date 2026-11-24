package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/randy-girard/flynn/blobstore/backend"
	"github.com/randy-girard/flynn/blobstore/data"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/random"
)

// EDGE-33: a slow or stalled uploader must not hold a database transaction,
// a pool connection or the row lock of the object it replaces while the
// blobstore waits for its body, and a body that stops arriving is cut off.

// stallReader is a request body from a client that sends a few bytes and then
// stops. It blocks until end is called, then returns err (io.EOF for a body
// that finishes, anything else for a client that goes away).
type stallReader struct {
	first   []byte
	started chan struct{}
	release chan struct{}
	once    sync.Once
	err     error
	sent    bool
}

func newStallReader(first string) *stallReader {
	return &stallReader{first: []byte(first), started: make(chan struct{}), release: make(chan struct{})}
}

func (s *stallReader) Read(p []byte) (int, error) {
	if !s.sent {
		s.sent = true
		close(s.started)
		return copy(p, s.first), nil
	}
	<-s.release
	return 0, s.err
}

func (s *stallReader) end(err error) {
	s.once.Do(func() { s.err = err; close(s.release) })
}

func waitStarted(t *testing.T, s *stallReader) {
	t.Helper()
	select {
	case <-s.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the upload never started reading its body")
	}
}

// within runs f and fails the test when it does not return within d.
func within(t *testing.T, d time.Duration, what string, f func() error) {
	t.Helper()
	errc := make(chan error, 1)
	go func() { errc <- f() }()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%s: %s", what, err)
		}
	case <-time.After(d):
		t.Fatalf("%s did not finish within %s: it waits behind the stalled upload", what, d)
	}
}

// repoKinds runs a test against the Postgres backend and the fake external
// backend (S3, GCS, Azure, MinIO).
func repoKinds(t *testing.T, db func(t *testing.T) *postgres.DB, f func(t *testing.T, r *data.FileRepo, ext *fakeExternalBackend)) {
	t.Run("postgres", func(t *testing.T) {
		f(t, data.NewFileRepo(db(t), []backend.Backend{backend.Postgres}, "postgres"), nil)
	})
	t.Run("external", func(t *testing.T) {
		b := newFakeExternalBackend()
		f(t, data.NewFileRepo(db(t), []backend.Backend{backend.Postgres, b}, b.Name()), b)
	})
}

func pooledDB(t *testing.T) *postgres.DB {
	db := initDB(t)
	t.Cleanup(db.Close)
	return db
}

func TestOverwriteDoesNotWaitForStalledOverwrite(t *testing.T) {
	repoKinds(t, pooledDB, func(t *testing.T, r *data.FileRepo, _ *fakeExternalBackend) {
		name := "/repos/" + random.String(8) + ".tar"
		if err := r.Put(name, strings.NewReader("v1"), 0, "application/x-tar"); err != nil {
			t.Fatal(err)
		}

		stalled := newStallReader("partial")
		defer stalled.end(errors.New("test over"))
		stalledErr := make(chan error, 1)
		go func() { stalledErr <- r.Put(name, stalled, 0, "application/x-tar") }()
		waitStarted(t, stalled)

		within(t, 5*time.Second, "an overwrite while another overwrite is stalled", func() error {
			return r.Put(name, strings.NewReader("v2"), 0, "application/x-tar")
		})
		if got := readBlob(t, r, name); got != "v2" {
			t.Fatalf("expected v2 after the second overwrite, got %q", got)
		}

		stalled.end(errors.New("client went away"))
		if err := <-stalledErr; err == nil {
			t.Fatal("the stalled overwrite succeeded although its client went away")
		}
		if got := readBlob(t, r, name); got != "v2" {
			t.Fatalf("the failed overwrite changed the object: got %q", got)
		}
	})
}

func TestPutHoldsNoConnectionWhileReadingBody(t *testing.T) {
	repoKinds(t, singleConnDB, func(t *testing.T, r *data.FileRepo, _ *fakeExternalBackend) {
		// three stalled uploads on a pool of one connection: neither a new
		// object, an overwrite of a stalled name, nor a read may wait for them
		var stalls []*stallReader
		var names []string
		var dones []chan error
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			s := newStallReader("partial")
			name := fmt.Sprintf("/stalled-%d-%s", i, random.String(8))
			done := make(chan error, 1)
			stalls, names, dones = append(stalls, s), append(names, name), append(dones, done)
			wg.Add(1)
			go func() { defer wg.Done(); done <- r.Put(name, s, 0, "text/plain") }()
			waitStarted(t, s)
		}
		defer func() {
			for _, s := range stalls {
				s.end(errors.New("test over"))
			}
			wg.Wait()
		}()

		other := "/other-" + random.String(8)
		within(t, 5*time.Second, "a PUT while uploads are stalled", func() error {
			return r.Put(other, strings.NewReader("x"), 0, "text/plain")
		})
		within(t, 5*time.Second, "an overwrite of a stalled name", func() error {
			return r.Put(names[0], strings.NewReader("y"), 0, "text/plain")
		})
		within(t, 5*time.Second, "a GET while uploads are stalled", func() error {
			if got := readBlob(t, r, other); got != "x" {
				return fmt.Errorf("got %q", got)
			}
			return nil
		})

		// a stalled upload that then finishes still stores its body
		stalls[1].end(io.EOF)
		if err := <-dones[1]; err != nil {
			t.Fatalf("a slow upload that finished failed: %s", err)
		}
		if got := readBlob(t, r, names[1]); got != "partial" {
			t.Fatalf("a slow upload that finished stored %q", got)
		}
	})
}

func TestConcurrentOverwritesLeaveOneLiveObject(t *testing.T) {
	repoKinds(t, pooledDB, func(t *testing.T, r *data.FileRepo, ext *fakeExternalBackend) {
		name := "/repos/" + random.String(8) + ".tar"
		const n = 8
		var wg sync.WaitGroup
		errs := make(chan error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				errs <- r.Put(name, strings.NewReader(fmt.Sprintf("body-%d", i)), 0, "application/x-tar")
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent overwrite failed: %s", err)
			}
		}
		if got := readBlob(t, r, name); !strings.HasPrefix(got, "body-") {
			t.Fatalf("unexpected content %q", got)
		}
		paths, err := r.List("/repos/")
		if err != nil {
			t.Fatal(err)
		}
		live := 0
		for _, p := range paths {
			if p == name {
				live++
			}
		}
		if live != 1 {
			t.Fatalf("expected one live %s, listed %d times", name, live)
		}
		if ext != nil {
			if n := ext.count(); n != 1 {
				t.Fatalf("expected only the winning object in the backend, found %d", n)
			}
		}
		assertNoUploads(t, r)
	})
}

// assertNoUploads checks that no upload is left in progress.
func assertNoUploads(t *testing.T, r *data.FileRepo) {
	t.Helper()
	n, err := r.PendingUploads()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d uploads still recorded as in progress", n)
	}
}

// EDGE-32: a failed upload removes the object it created in the bucket.
func TestPutFailureDeletesNewExternalObject(t *testing.T) {
	r, b, done := newFakeExternalRepo(t)
	defer done()
	name := "/repos/" + random.String(8) + ".tar"
	if err := r.Put(name, strings.NewReader("old"), 0, "application/x-tar"); err != nil {
		t.Fatal(err)
	}

	// the upload reached the bucket, then the backend reported an error
	b.failAfterStore = true
	if err := r.Put(name, strings.NewReader("new"), 0, "application/x-tar"); err == nil {
		t.Fatal("expected the failed upload to fail")
	}
	b.failAfterStore = false
	if n := b.count(); n != 1 {
		t.Fatalf("a failed upload left %d objects in the bucket, want only the old one", n)
	}
	if got := readBlob(t, r, name); got != "old" {
		t.Fatalf("got %q after a failed overwrite", got)
	}
	assertNoUploads(t, r)

	// the whole object reached the bucket, then the commit failed: here
	// the upload's record vanished, so the swap transaction rolls back
	b.onStored = func() {
		if err := r.DB().Exec("DELETE FROM uploads"); err != nil {
			t.Error(err)
		}
	}
	if err := r.Put(name, strings.NewReader("new"), 0, "application/x-tar"); err == nil {
		t.Fatal("expected the upload whose commit failed to fail")
	}
	b.onStored = nil
	if n := b.count(); n != 1 {
		t.Fatalf("an upload whose commit failed left %d objects in the bucket", n)
	}
	if got := readBlob(t, r, name); got != "old" {
		t.Fatalf("got %q after an overwrite whose commit failed", got)
	}

	// the client's body breaks off part way
	if err := r.Put(name, io.MultiReader(strings.NewReader("ne"), errReader{errors.New("connection reset")}), 0, "application/x-tar"); err == nil {
		t.Fatal("expected the broken upload to fail")
	}
	if n := b.count(); n != 1 {
		t.Fatalf("a broken upload left %d objects in the bucket", n)
	}
	assertNoUploads(t, r)
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// An upload that makes no progress for long enough is abandoned (its client,
// or the blobstore instance that served it, went away): the sweep deletes its
// object, and the upload can no longer commit if it comes back.
func TestSweepRemovesAbandonedUploads(t *testing.T) {
	repoKinds(t, pooledDB, func(t *testing.T, r *data.FileRepo, ext *fakeExternalBackend) {
		db := r.DB()
		objects := func() int {
			if ext != nil {
				return ext.count()
			}
			var n int
			if err := db.QueryRow("SELECT count(*) FROM pg_largeobject_metadata").Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		before := objects()

		name := "/abandoned-" + random.String(8)
		stalled := newStallReader("partial")
		defer stalled.end(errors.New("test over"))
		putErr := make(chan error, 1)
		go func() { putErr <- r.Put(name, stalled, 0, "text/plain") }()
		waitStarted(t, stalled)

		// a fresh upload is never swept
		if n, err := r.SweepUploads(time.Hour); err != nil || n != 0 {
			t.Fatalf("sweep of a live upload: n=%d err=%v", n, err)
		}
		if err := db.Exec("UPDATE uploads SET updated_at = now() - interval '2 hours'"); err != nil {
			t.Fatal(err)
		}
		if n, err := r.SweepUploads(time.Hour); err != nil || n != 1 {
			t.Fatalf("sweep of an abandoned upload: n=%d err=%v", n, err)
		}

		stalled.end(io.EOF)
		if err := <-putErr; err == nil {
			t.Fatal("an upload that was swept committed")
		}
		if _, err := r.Get(name, false); err != backend.ErrNotFound {
			t.Fatalf("expected %s not to exist, got %v", name, err)
		}
		if after := objects(); after != before {
			t.Fatalf("objects before %d, after %d: the swept upload left its object", before, after)
		}
		assertNoUploads(t, r)
	})
}

// An append to an object in an external backend writes a new object with the
// old data and the new; the old one is deleted after the commit.
func TestAppendExternalDeletesOldObject(t *testing.T) {
	r, b, done := newFakeExternalRepo(t)
	defer done()
	name := "/append-" + random.String(8)
	if err := r.Put(name, strings.NewReader("abc"), 0, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := r.Put(name, strings.NewReader("def"), 3, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if got := readBlob(t, r, name); got != "abcdef" {
		t.Fatalf("got %q after append", got)
	}
	if n := b.count(); n != 1 {
		t.Fatalf("append left %d objects in the bucket, want 1", n)
	}
	if err := r.Put(name, strings.NewReader("x"), 4, "text/plain"); err == nil {
		t.Fatal("an append at the wrong offset succeeded")
	}
	if got := readBlob(t, r, name); got != "abcdef" {
		t.Fatalf("a refused append changed the object: %q", got)
	}
	assertNoUploads(t, r)
}

// stalledServer serves r through the same handler stack as the blobstore
// server, with the given body idle timeout.
func stalledServer(t *testing.T, r *data.FileRepo, idle time.Duration) *httptest.Server {
	srv := httptest.NewUnstartedServer(wrapHandler(handler(r, putLimits{idleTimeout: idle})))
	applyServerTimeouts(srv.Config)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// stalledPut sends the headers of a PUT and part of its body, then stops
// sending. It returns the connection and a reader for the response.
func stalledPut(t *testing.T, srv *httptest.Server, name string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "PUT %s HTTP/1.1\r\nHost: blobstore\r\nContent-Type: text/plain\r\nContent-Length: 100000\r\n\r\npartial", name)
	return conn, bufio.NewReader(conn)
}

func TestPutWithStalledBodyFailsWithinTimeout(t *testing.T) {
	db := singleConnDB(t)
	r := data.NewFileRepo(db, []backend.Backend{backend.Postgres}, "postgres")
	name := "/repos/" + random.String(8) + ".tar"
	if err := r.Put(name, strings.NewReader("old"), 0, "application/x-tar"); err != nil {
		t.Fatal(err)
	}
	srv := stalledServer(t, r, 300*time.Millisecond)

	conn, br := stalledPut(t, srv, name)
	start := time.Now()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("no answer to the stalled PUT after %s: %s", time.Since(start).Round(time.Millisecond), err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("stalled PUT: expected 408, got %d %q", res.StatusCode, body)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the stalled PUT was cut off only after %s", d)
	}
	// the server closes the connection
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("the connection of a timed out PUT is still open")
	}
	if got := readBlob(t, r, name); got != "old" {
		t.Fatalf("expected the old content after the stalled overwrite, got %q", got)
	}
	assertNoUploads(t, r)

	// a slow body that keeps sending is not cut off
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < 5; i++ {
			pw.Write([]byte("chunk"))
			time.Sleep(150 * time.Millisecond)
		}
		pw.Close()
	}()
	req, _ := http.NewRequest("PUT", srv.URL+name, pr)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("a slow but steady PUT got %d", res.StatusCode)
	}
	if got := readBlob(t, r, name); got != strings.Repeat("chunk", 5) {
		t.Fatalf("got %q after the slow PUT", got)
	}
}

func TestPutIdleTimeoutOffWaitsForBody(t *testing.T) {
	r := data.NewFileRepo(pooledDB(t), []backend.Backend{backend.Postgres}, "postgres")
	srv := stalledServer(t, r, 0)
	name := "/off-" + random.String(8)
	conn, br := stalledPut(t, srv, name)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := http.ReadResponse(br, nil); err == nil {
		t.Fatal("with the idle timeout off a stalled PUT was answered")
	}
}

func TestUploadIdleTimeoutSetting(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"", defaultUploadIdleTimeout, false},
		{"0", 0, false},
		{"off", 0, false},
		{"OFF", 0, false},
		{"90s", 90 * time.Second, false},
		{"2m", 2 * time.Minute, false},
		{"45", 45 * time.Second, false},
		{"-1s", 0, true},
		{"soon", 0, true},
	} {
		got, err := parseIdleTimeout(c.in, defaultUploadIdleTimeout)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("parseIdleTimeout(%q) = %s, %v; want %s, err=%v", c.in, got, err, c.want, c.err)
		}
	}
	if defaultUploadIdleTimeout < 30*time.Second {
		t.Fatalf("the default idle timeout %s is not generous", defaultUploadIdleTimeout)
	}
}

func TestServerTimeouts(t *testing.T) {
	s := &http.Server{}
	applyServerTimeouts(s)
	if s.ReadHeaderTimeout <= 0 || s.IdleTimeout <= 0 {
		t.Fatalf("ReadHeaderTimeout=%s IdleTimeout=%s, want both set", s.ReadHeaderTimeout, s.IdleTimeout)
	}
	// a whole-request deadline would cut off large uploads and downloads that
	// are still moving; the PUT body idle timeout bounds stalled ones
	if s.ReadTimeout != 0 || s.WriteTimeout != 0 {
		t.Fatalf("ReadTimeout=%s WriteTimeout=%s, want none", s.ReadTimeout, s.WriteTimeout)
	}
}
