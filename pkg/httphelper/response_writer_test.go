package httphelper

import (
	"net/http/httptest"
	"testing"

	"golang.org/x/net/context"
)

func TestSetContext(t *testing.T) {
	inner := httptest.NewRecorder()
	rw := NewResponseWriter(inner, context.Background())
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "principal")
	rw.SetContext(ctx)
	got := rw.Context()
	if got == nil {
		t.Fatal("SetContext must store a non-nil context")
	}
	if v, _ := got.Value(key{}).(string); v != "principal" {
		t.Fatalf("Context value = %v, want principal", got.Value(key{}))
	}
}

// http.ResponseController finds the connection through Unwrap, which the
// blobstore needs to put a read deadline on PUT bodies (EDGE-33).
func TestResponseWriterUnwrap(t *testing.T) {
	inner := httptest.NewRecorder()
	rw := NewResponseWriter(inner, context.Background())
	if got := rw.Unwrap(); got != inner {
		t.Fatalf("Unwrap() = %v, want the wrapped writer", got)
	}
}
