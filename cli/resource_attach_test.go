package main

import (
	"strings"
	"testing"
)

func TestNormalizeResourceArgsTreatsOneTokenAsTheName(t *testing.T) {
	provider, resource, err := normalizeResourceArgs("mysql-valley-77924", "")
	if err != nil {
		t.Fatal(err)
	}
	if provider != "" || resource != "mysql-valley-77924" {
		t.Fatalf("provider=%q resource=%q", provider, resource)
	}
	provider, resource, err = normalizeResourceArgs("mysql", "mysql-valley-77924")
	if err != nil || provider != "mysql" || resource != "mysql-valley-77924" {
		t.Fatalf("provider=%q resource=%q err=%v", provider, resource, err)
	}
	if _, _, err := normalizeResourceArgs("", ""); err == nil {
		t.Fatal("expected a missing resource")
	}
}

func TestResourceAttachHelpAllowsNameOnly(t *testing.T) {
	cmd := commands["resource:attach"]
	if cmd == nil {
		t.Fatal("resource:attach is not registered")
	}
	for _, part := range []string{"[<provider>]", "mysql-valley-77924", "postgresql-concave-48291"} {
		if !strings.Contains(cmd.usage, part) {
			t.Fatalf("usage missing %q:\n%s", part, cmd.usage)
		}
	}
}
