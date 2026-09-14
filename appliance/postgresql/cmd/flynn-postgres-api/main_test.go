package main

import "testing"

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
