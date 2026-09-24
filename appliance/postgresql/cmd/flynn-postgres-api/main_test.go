package main

import (
	"strings"
	"testing"
)

func TestDatabaseURLDisablesSSL(t *testing.T) {
	got := databaseURL("user", "pass", "leader.postgres.discoverd", "db")
	want := "postgres://user:pass@leader.postgres.discoverd:5432/db?sslmode=require"
	if got != want {
		t.Fatalf("got %s", got)
	}
}

func TestQuoteIdent(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"controller", `"controller"`},
		{`weird"name`, `"weird""name"`},
		{"", `""`},
	}
	for _, tc := range cases {
		if got := quoteIdent(tc.in); got != tc.want {
			t.Fatalf("quoteIdent(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestRevokeConnectSQL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"controller", `REVOKE CONNECT ON DATABASE "controller" FROM PUBLIC`},
		{`a"b`, `REVOKE CONNECT ON DATABASE "a""b" FROM PUBLIC`},
		{"postgres", `REVOKE CONNECT ON DATABASE "postgres" FROM PUBLIC`},
		{"template1", `REVOKE CONNECT ON DATABASE "template1" FROM PUBLIC`},
	}
	for _, tc := range cases {
		if got := revokeConnectSQL(tc.in); got != tc.want {
			t.Fatalf("revokeConnectSQL(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestParseDatabaseResourceID(t *testing.T) {
	user := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	db := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	gotUser, gotDB, ok := parseDatabaseResourceID("/databases/" + user + ":" + db)
	if !ok || gotUser != user || gotDB != db {
		t.Fatalf("valid id: %s %s %v", gotUser, gotDB, ok)
	}
	for _, bad := range []string{
		"",
		"/databases/",
		user,
		"/databases/" + user + ":",
		`/databases/` + user + `:"evil"`,
		"/databases/" + user + ":postgres;drop",
		"/databases/" + user + ":template1",
		"/databases/" + user + ":bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbg",
		" /databases/" + user + ":" + db + " ",
	} {
		if _, _, ok := parseDatabaseResourceID(bad); ok {
			t.Fatalf("must reject %q", bad)
		}
	}
}

func TestValidDumpDatabase(t *testing.T) {
	if !validDumpDatabase("appdb") || !validDumpDatabase("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb") {
		t.Fatal("expected valid names")
	}
	for _, bad := range []string{"", "postgres;drop", "db name", "a/b", strings.Repeat("x", 64)} {
		if validDumpDatabase(bad) {
			t.Fatalf("must reject %q", bad)
		}
	}
}

func TestPgDumpRestoreArgv(t *testing.T) {
	dump := pgDumpArgv("appdb")
	if dump[0] != "pg_dump" || !containsStr(dump, "--format=custom") || !containsStr(dump, "--dbname=appdb") {
		t.Fatalf("%v", dump)
	}
	restore := pgRestoreArgv("appdb")
	if restore[0] != "pg_restore" || !containsStr(restore, "--clean") || !containsStr(restore, "--dbname=appdb") {
		t.Fatalf("%v", restore)
	}
}

func containsStr(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}
