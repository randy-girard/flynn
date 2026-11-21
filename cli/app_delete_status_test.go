package main

import (
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestDeletionStatusShowsFailedDelete(t *testing.T) {
	if got := deletionStatus(&ct.App{}); got != "" {
		t.Fatalf("expected no status for a live app, got %q", got)
	}
	if got := deletionStatus(nil); got != "" {
		t.Fatalf("expected no status for nil, got %q", got)
	}
	app := &ct.App{Meta: map[string]string{ct.AppMetaDeletionError: "2026-11-21T00:00:00Z: provider down"}}
	got := deletionStatus(app)
	if !strings.Contains(got, "provider down") || !strings.Contains(got, "retrying") {
		t.Fatalf("expected the failure in the status, got %q", got)
	}
}
