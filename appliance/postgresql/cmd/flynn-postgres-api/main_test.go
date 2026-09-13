package main

import "testing"

func TestQuoteIdent(t *testing.T) {
	if got := quoteIdent("controller"); got != `"controller"` {
		t.Fatalf("quoteIdent(controller) = %s", got)
	}
	if got := quoteIdent(`weird"name`); got != `"weird""name"` {
		t.Fatalf("quoteIdent escaped = %s", got)
	}
}
