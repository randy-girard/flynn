package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// useRepoCache points prepareRepo and uploadRepo at srv for one test.
func useRepoCache(t *testing.T, srv *httptest.Server, client *http.Client) {
	t.Helper()
	base, c := repoCacheBase, repoCacheClient
	repoCacheBase = srv.URL
	if client != nil {
		repoCacheClient = client
	}
	t.Cleanup(func() { repoCacheBase, repoCacheClient = base, c })
}

// newCacheRepo returns a git repo with one commit, as left by a push.
func newCacheRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := initRepo(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/a.txt", []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "a")
	return dir
}

func TestUploadRepoReturnsErrorOnNon200(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			http.Error(w, "nope", code)
		}))
		useRepoCache(t, srv, nil)
		err := uploadRepo(newCacheRepo(t), "app1")
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: uploadRepo returned nil", code)
		}
		if !strings.Contains(err.Error(), http.StatusText(code)) && !strings.Contains(err.Error(), strconv.Itoa(code)) {
			t.Fatalf("status %d: error does not name the status: %v", code, err)
		}
	}
}

func TestUploadRepoTransportErrorDoesNotPanic(t *testing.T) {
	// The server reads the whole tar, so the archive goroutine succeeds,
	// then drops the connection without a response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer srv.Close()
	useRepoCache(t, srv, nil)
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("uploadRepo panicked: %v", p)
			}
		}()
		err = uploadRepo(newCacheRepo(t), "app1")
	}()
	if err == nil {
		t.Fatal("uploadRepo returned nil after the connection was dropped")
	}
}

func TestUploadRepoSucceedsOn200(t *testing.T) {
	var got int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/repos/app1.tar" {
			http.Error(w, "bad request "+r.Method+" "+r.URL.Path, 400)
			return
		}
		n, _ := io.Copy(io.Discard, r.Body)
		atomic.StoreInt64(&got, n)
	}))
	defer srv.Close()
	useRepoCache(t, srv, nil)
	if err := uploadRepo(newCacheRepo(t), "app1"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt64(&got) == 0 {
		t.Fatal("no tar was uploaded")
	}
}

// listTemp returns the entries in dir, which a test uses as TMPDIR.
func listTemp(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestPrepareRepoRemovesTempDirOnError(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"500": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) },
		"401": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 401) },
		// A truncated or corrupt cache leaves a half-extracted repo.
		"corrupt tar": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(strings.Repeat("x", 2048)))
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			srv := httptest.NewServer(h)
			defer srv.Close()
			useRepoCache(t, srv, nil)
			path, err := prepareRepo("app1")
			if err == nil {
				t.Fatalf("prepareRepo succeeded with %q", path)
			}
			if left := listTemp(t, tmp); len(left) != 0 {
				t.Fatalf("prepareRepo left %v in TMPDIR", left)
			}
		})
	}
	t.Run("transport error", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("TMPDIR", tmp)
		srv := httptest.NewServer(http.NotFoundHandler())
		useRepoCache(t, srv, nil)
		srv.Close()
		if _, err := prepareRepo("app1"); err == nil {
			t.Fatal("prepareRepo succeeded against a closed server")
		}
		if left := listTemp(t, tmp); len(left) != 0 {
			t.Fatalf("prepareRepo left %v in TMPDIR", left)
		}
	})
}

func TestPrepareRepoRoundTrip(t *testing.T) {
	// A repo uploaded by one push is what the next push starts from.
	var stored []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			stored, _ = io.ReadAll(r.Body)
		case "GET":
			if stored == nil {
				http.NotFound(w, r)
				return
			}
			w.Write(stored)
		}
	}))
	defer srv.Close()
	useRepoCache(t, srv, nil)

	fresh, err := prepareRepo("app1")
	if err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(fresh)

	src := newCacheRepo(t)
	head := strings.TrimSpace(string(mustGit(t, src, "rev-parse", "HEAD")))
	if err := uploadRepo(src, "app1"); err != nil {
		t.Fatal(err)
	}
	path, err := prepareRepo("app1")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path)
	if got := strings.TrimSpace(string(mustGit(t, path, "rev-parse", "HEAD"))); got != head {
		t.Fatalf("HEAD after round trip = %s, want %s", got, head)
	}
}

func TestRepoCacheClientTimesOut(t *testing.T) {
	if repoCacheClient == http.DefaultClient || repoCacheClient.Timeout <= 0 {
		t.Fatal("repo cache client has no timeout")
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-release
	}))
	defer srv.Close()
	defer close(release)
	useRepoCache(t, srv, newRepoCacheClient(5*time.Second, 200*time.Millisecond))

	start := time.Now()
	if err := uploadRepo(newCacheRepo(t), "app1"); err == nil {
		t.Fatal("upload to a stalled blobstore succeeded")
	}
	if _, err := prepareRepo("app1"); err == nil {
		t.Fatal("download from a stalled blobstore succeeded")
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("stalled blobstore took %s", d)
	}
}

func TestStoreRepoRetriesServerErrors(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "busy", 503)
		}
	}))
	defer srv.Close()
	useRepoCache(t, srv, nil)
	h := &gitHandler{upload: uploadRepo, uploadRetryDelay: time.Millisecond}
	if err := h.storeRepo(newCacheRepo(t), "app1"); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
}

func TestStoreRepoDoesNotRetryRefusals(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		atomic.AddInt32(&calls, 1)
		http.Error(w, "no", 401)
	}))
	defer srv.Close()
	useRepoCache(t, srv, nil)
	h := &gitHandler{upload: uploadRepo, uploadRetryDelay: time.Millisecond}
	if err := h.storeRepo(newCacheRepo(t), "app1"); err == nil {
		t.Fatal("storeRepo returned nil on 401")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}

func TestStoreRepoGivesUpAfterAttempts(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		atomic.AddInt32(&calls, 1)
		http.Error(w, "down", 500)
	}))
	defer srv.Close()
	useRepoCache(t, srv, nil)
	h := &gitHandler{upload: uploadRepo, uploadRetryDelay: time.Millisecond}
	if err := h.storeRepo(newCacheRepo(t), "app1"); err == nil {
		t.Fatal("storeRepo returned nil while the blobstore is down")
	}
	if n := atomic.LoadInt32(&calls); n != repoUploadAttempts {
		t.Fatalf("calls = %d, want %d", n, repoUploadAttempts)
	}
}

func TestUploadRepoArchiveErrorIsNotStored(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	var stored int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err == nil {
			atomic.AddInt32(&stored, 1)
		}
	}))
	defer srv.Close()
	useRepoCache(t, srv, nil)
	repo := newCacheRepo(t)
	bad := repo + "/.git/unreadable"
	if err := os.WriteFile(bad, []byte(strings.Repeat("x", 1<<20)), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(bad, 0644) })
	if err := uploadRepo(repo, "app1"); err == nil {
		t.Fatal("uploadRepo returned nil when the archive failed")
	}
	if n := atomic.LoadInt32(&stored); n != 0 {
		t.Fatalf("blobstore received a complete body %d times from a failed archive", n)
	}
}
