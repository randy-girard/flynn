package main

import "testing"

func TestDatabaseURLDisablesSSL(t *testing.T) {
	got := databaseURL("user", "pass", "leader.postgres.discoverd", "db")
	want := "postgres://user:pass@leader.postgres.discoverd:5432/db?sslmode=disable"
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
