package main

import (
	"errors"
	"strings"
	"testing"

	hh "github.com/randy-girard/flynn/pkg/httphelper"
)

func TestCollabUserCreateErrorExplainsAdminOnly(t *testing.T) {
	err := collabUserCreateError("ada@example.com", hh.JSONError{Code: hh.ForbiddenErrorCode, Message: "forbidden"})
	if !strings.Contains(err.Error(), "cluster administrator") || !strings.Contains(err.Error(), "ada@example.com") {
		t.Fatalf("want admin-only explanation naming the email, got %q", err)
	}
	other := errors.New("boom")
	if got := collabUserCreateError("a@b.c", other); !errors.Is(got, other) {
		t.Fatalf("non-403 errors must stay wrapped, got %v", got)
	}
}
