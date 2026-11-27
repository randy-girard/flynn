package main

import (
	"bytes"
	"reflect"
	"sort"
	"testing"

	"github.com/randy-girard/flynn/blobstore/backend"
	"github.com/randy-girard/flynn/blobstore/data"
)

// regexNames hold regular expression and LIKE metacharacters. Delete, List
// and the migrate prefix must treat a name as a literal path (NEW-48).
var regexNames = []string{
	"/lit/a.b", "/lit/a+*", "/lit/a(b", "/lit/[ab]", "/lit/a%b", "/lit/a_b", `/lit/a\b`, "/lit/a|b", "/lit/a$", "/lit/^a", "/lit/a{2}",
}

func putLiteral(t *testing.T, r *data.FileRepo, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := r.Put(name, bytes.NewReader([]byte("data")), 0, "text/plain"); err != nil {
			t.Fatalf("put %s: %s", name, err)
		}
	}
}

func exists(t *testing.T, r *data.FileRepo, name string) bool {
	t.Helper()
	_, err := r.Get(name, false)
	if err == backend.ErrNotFound {
		return false
	}
	if err != nil {
		t.Fatalf("get %s: %s", name, err)
	}
	return true
}

func TestDeleteMatchesNameLiterally(t *testing.T) {
	db := initDB(t)
	defer db.Close()
	r := data.NewFileRepo(db, []backend.Backend{backend.Postgres}, "postgres")

	// Names a regex built from the name would also match.
	bystanders := []string{"/lit/axb", "/lit/ab", "/lit/aab", "/lit/a", "/lit/b", "/lit/aa", "/lit/a%xb", "/lit/aXb", "/lit/a{2}x"}
	putLiteral(t, r, bystanders...)
	putLiteral(t, r, regexNames...)
	putLiteral(t, r, "/lit/d.r/x", "/lit/dxr/x", "/lit/d.rx/y")

	for _, name := range regexNames {
		if err := r.Delete(name); err != nil {
			t.Fatalf("delete %q: %s", name, err)
		}
		if exists(t, r, name) {
			t.Fatalf("%q still exists after deleting it", name)
		}
	}
	for _, name := range bystanders {
		if !exists(t, r, name) {
			t.Fatalf("deleting the metacharacter names also deleted %q", name)
		}
	}

	// A directory delete removes the children and nothing else.
	if err := r.Delete("/lit/d.r"); err != nil {
		t.Fatal(err)
	}
	if exists(t, r, "/lit/d.r/x") {
		t.Fatal("/lit/d.r/x survived deleting /lit/d.r")
	}
	for _, name := range []string{"/lit/dxr/x", "/lit/d.rx/y"} {
		if !exists(t, r, name) {
			t.Fatalf("deleting /lit/d.r also deleted %q", name)
		}
	}
}

func TestListMatchesDirLiterally(t *testing.T) {
	db := initDB(t)
	defer db.Close()
	r := data.NewFileRepo(db, []backend.Backend{backend.Postgres}, "postgres")

	putLiteral(t, r, "/a.b/one", "/a.b/sub/two", "/axb/three", "/a.bc/four", "/a%/five", "/a%x/six", "/a_/seven", "/ab/eight", "/a+/x", "/a(/y", "/a$/z", "/a[/w")

	for dir, want := range map[string][]string{
		"/a.b": {"/a.b/one", "/a.b/sub/"},
		"/a%":  {"/a%/five"},
		"/a_":  {"/a_/seven"},
		"/a+":  {"/a+/x"},
		"/a(":  {"/a(/y"},
		"/a$":  {"/a$/z"},
		"/a[":  {"/a[/w"},
		"/a+*": nil,
	} {
		got, err := r.List(dir)
		if err != nil {
			t.Fatalf("list %q: %s", dir, err)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("list %q = %v, want %v", dir, got, want)
		}
	}
}

func TestListFilesExcludingDefaultBackendMatchesPrefixLiterally(t *testing.T) {
	db := initDB(t)
	defer db.Close()
	// Store in postgres, then list as if another backend were the default.
	pg := data.NewFileRepo(db, []backend.Backend{backend.Postgres}, "postgres")
	putLiteral(t, pg, "/p%/one", "/px/two", "/p_/three", "/py/four")
	r := data.NewFileRepo(db, []backend.Backend{backend.Postgres, newFakeExternalBackend()}, "fake-external")

	for prefix, want := range map[string][]string{
		"/p%": {"/p%/one"},
		"/p_": {"/p_/three"},
		"/p":  {"/p%/one", "/p_/three", "/px/two", "/py/four"},
	} {
		files, err := r.ListFilesExcludingDefaultBackend(prefix)
		if err != nil {
			t.Fatalf("list %q: %s", prefix, err)
		}
		var got []string
		for _, f := range files {
			got = append(got, f.FileInfo.Name)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("prefix %q = %v, want %v", prefix, got, want)
		}
	}
}
