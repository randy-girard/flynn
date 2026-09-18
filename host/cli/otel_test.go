package cli

import "testing"

func TestParseOTELHeaders(t *testing.T) {
	got, err := parseOTELHeaders([]string{"Authorization: Bearer x", "X-Scope: flynn"})
	if err != nil {
		t.Fatal(err)
	}
	if got["Authorization"] != "Bearer x" || got["X-Scope"] != "flynn" {
		t.Fatalf("%v", got)
	}
	if _, err := parseOTELHeaders([]string{"bad"}); err == nil {
		t.Fatal("expected invalid header error")
	}
}
