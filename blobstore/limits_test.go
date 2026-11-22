package main

import (
	"bytes"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/blobstore/backend"
	"github.com/randy-girard/flynn/blobstore/data"
	"github.com/randy-girard/flynn/pkg/random"
)

// limitServer serves a Postgres-backed repo with the given PUT limits.
func limitServer(t *testing.T, lim putLimits) (*httptest.Server, *data.FileRepo, func()) {
	t.Helper()
	db := initDB(t)
	r := data.NewFileRepo(db, []backend.Backend{backend.Postgres}, "postgres")
	lim.usage = r.AppUsage
	srv := httptest.NewServer(handler(r, lim))
	return srv, r, func() { srv.Close(); db.Close() }
}

// hideLength hides the length of a body so the request is sent chunked.
type hideLength struct{ io.Reader }

func putBlob(t *testing.T, srv *httptest.Server, name string, body []byte, offset int, chunked bool) (int, string) {
	t.Helper()
	var r io.Reader = bytes.NewReader(body)
	if chunked {
		r = hideLength{r}
	}
	req, err := http.NewRequest("PUT", srv.URL+name, r)
	if err != nil {
		t.Fatal(err)
	}
	if offset > 0 {
		req.Header.Set("Blobstore-Offset", strconv.Itoa(offset))
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	msg, _ := ioutil.ReadAll(res.Body)
	return res.StatusCode, string(msg)
}

// EDGE-08: a PUT over MAX_OBJECT_SIZE is refused with 413, with or without a
// Content-Length, an append cannot grow an object past it, and a refused
// overwrite leaves the old content in place.
func TestPutRejectsOversizeObject(t *testing.T) {
	srv, r, done := limitServer(t, putLimits{maxObject: 1024})
	defer done()
	name := "/repos/" + random.String(8) + ".tar"

	if code, msg := putBlob(t, srv, name, []byte("old"), 0, false); code != 200 {
		t.Fatalf("small PUT = %d %s", code, msg)
	}
	big := bytes.Repeat([]byte("x"), 2048)
	for _, chunked := range []bool{false, true} {
		code, msg := putBlob(t, srv, name, big, 0, chunked)
		if code != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversize PUT (chunked=%v) = %d %s, want 413", chunked, code, msg)
		}
		if !strings.Contains(msg, "MAX_OBJECT_SIZE") {
			t.Fatalf("error does not name the setting: %s", msg)
		}
		if got := readBlob(t, r, name); got != "old" {
			t.Fatalf("after a refused overwrite (chunked=%v) got %q, want the old content", chunked, got)
		}
	}

	// exactly the limit is allowed
	exact := bytes.Repeat([]byte("y"), 1024)
	if code, msg := putBlob(t, srv, name, exact, 0, true); code != 200 {
		t.Fatalf("PUT of exactly the limit = %d %s", code, msg)
	}

	// appends count the bytes already stored
	appendName := "/repos/" + random.String(8) + ".tar"
	part := bytes.Repeat([]byte("z"), 600)
	if code, msg := putBlob(t, srv, appendName, part, 0, false); code != 200 {
		t.Fatalf("first part = %d %s", code, msg)
	}
	if code, msg := putBlob(t, srv, appendName, part, 600, true); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("append past the limit = %d %s, want 413", code, msg)
	}
	if got := readBlob(t, r, appendName); got != string(part) {
		t.Fatalf("after a refused append got %d bytes, want %d", len(got), len(part))
	}
}

// EDGE-08: APP_QUOTA caps the live bytes stored under one app's names (its
// slug and image prefixes, build caches and repo tarball). A PUT that would
// pass it is refused with 507; other apps and an overwrite that stays under
// the quota are not affected.
func TestPutEnforcesAppQuota(t *testing.T) {
	srv, r, done := limitServer(t, putLimits{appQuota: 4096})
	defer done()
	app := random.UUID()
	other := random.UUID()
	blob := func(n int) []byte { return bytes.Repeat([]byte("q"), n) }

	slug := "/slugs/apps/" + app + "/layers/" + random.String(8) + ".squashfs"
	if code, msg := putBlob(t, srv, slug, blob(3000), 0, false); code != 200 {
		t.Fatalf("first slug = %d %s", code, msg)
	}
	for _, name := range []string{
		"/slugs/apps/" + app + "/layers/" + random.String(8) + ".squashfs",
		"/tarreceive/apps/" + app + "/images/" + random.String(8) + ".json",
		"/" + app + "-cache.tgz",
		"/" + app + "-docker-cache.tgz",
		"/repos/" + app + ".tar",
	} {
		for _, chunked := range []bool{false, true} {
			code, msg := putBlob(t, srv, name, blob(2000), 0, chunked)
			if code != http.StatusInsufficientStorage {
				t.Fatalf("PUT %s over the app quota (chunked=%v) = %d %s, want 507", name, chunked, code, msg)
			}
			if !strings.Contains(msg, "APP_QUOTA") {
				t.Fatalf("error does not name the setting: %s", msg)
			}
			if _, err := r.Get(name, false); err != backend.ErrNotFound {
				t.Fatalf("refused PUT %s left an object (err=%v)", name, err)
			}
		}
	}

	// replacing the app's own object counts only the new size
	if code, msg := putBlob(t, srv, slug, blob(4000), 0, true); code != 200 {
		t.Fatalf("overwrite under the quota = %d %s", code, msg)
	}
	// another app has its own quota
	if code, msg := putBlob(t, srv, "/slugs/apps/"+other+"/layers/x.squashfs", blob(3000), 0, false); code != 200 {
		t.Fatalf("other app = %d %s", code, msg)
	}
	// names that belong to no app are not counted
	if code, msg := putBlob(t, srv, "/tarreceive/layers/"+random.String(8)+".squashfs", blob(5000), 0, false); code != 200 {
		t.Fatalf("shared layer = %d %s", code, msg)
	}
}

func TestAppForPath(t *testing.T) {
	for p, want := range map[string]string{
		"/slugs/apps/a1/layers/x.squashfs": "a1",
		"/slugs/apps/a1/images/x.json":     "a1",
		"/tarreceive/apps/a1/images/x":     "a1",
		"/a1-cache.tgz":                    "a1",
		"/a1-docker-cache.tgz":             "a1",
		"/repos/a1.tar":                    "a1",
		"/slugs/x.squashfs":                "",
		"/tarreceive/layers/x.squashfs":    "",
		"/slugs/apps//layers/x":            "",
		"/repos/.tar":                      "",
		"/repos/a/b.tar":                   "",
		"/other.txt":                       "",
	} {
		got, ok := appForPath(p)
		if got != want || ok != (want != "") {
			t.Errorf("appForPath(%q) = %q, %v; want %q", p, got, ok, want)
		}
	}
}
