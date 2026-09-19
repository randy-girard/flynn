package logaggregator

import "testing"

func TestDefaultStreamTypesIncludeSystem(t *testing.T) {
	if got := DefaultStreamTypesQuery(); got != "stdout,stderr,system" {
		t.Fatalf("query=%q", got)
	}
	q := (&LogOpts{}).EncodedQuery()
	if q != "stream_types=stdout%2Cstderr%2Csystem" {
		t.Fatalf("EncodedQuery=%q", q)
	}
}
