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
