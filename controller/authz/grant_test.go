package authz

import (
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
)

func TestGrantForMatchesIDOnly(t *testing.T) {
	tok := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "id-1", Permissions: []string{"app:write"}}}}
	if got := GrantFor(tok, "id-1"); len(got) != 1 || got[0] != "app:write" {
		t.Fatalf("GrantFor by id = %v", got)
	}
	if got := GrantFor(tok, "other"); len(got) != 0 {
		t.Fatalf("GrantFor other = %v", got)
	}
	if got := GrantFor(nil, "id-1"); len(got) != 0 {
		t.Fatalf("GrantFor nil token = %v", got)
	}
}

func TestHelpersIgnoreNameGrants(t *testing.T) {
	byName := &authorizer.Token{
		Scopes:    []string{ScopeGitreceiveBuild, ScopeEventsRead},
		AppGrants: []authorizer.AppGrant{{AppID: "demo", Permissions: []string{"app:write", ScopeEventsRead}}},
	}
	if MayReadAppEnv(byName, "id-1", "demo") {
		t.Error("MayReadAppEnv honored a name grant")
	}
	if GitFetchAllowed(byName, "id-1", "demo") {
		t.Error("GitFetchAllowed honored a name grant")
	}
	if GitreceiveBuildAllowed(byName, "id-1", "demo") {
		t.Error("GitreceiveBuildAllowed honored a name grant")
	}
	if EventsGrantAllows(byName, "id-1", "demo", "") {
		t.Error("EventsGrantAllows honored a name grant")
	}
	byID := &authorizer.Token{
		Scopes:    []string{ScopeGitreceiveBuild, ScopeEventsRead},
		AppGrants: []authorizer.AppGrant{{AppID: "id-1", Permissions: []string{"app:write", ScopeEventsRead}}},
	}
	if !MayReadAppEnv(byID, "id-1", "demo") || !GitFetchAllowed(byID, "id-1", "demo") ||
		!GitreceiveBuildAllowed(byID, "id-1", "demo") || !EventsGrantAllows(byID, "id-1", "demo", "") {
		t.Error("id grant must still be honored")
	}
}

func TestGrantForUnionsDuplicateGrants(t *testing.T) {
	tok := &authorizer.Token{AppGrants: []authorizer.AppGrant{
		{AppID: "id-1", Permissions: []string{"app:read"}},
		{AppID: "id-2", Permissions: []string{"app:admin"}},
		{AppID: "id-1", Permissions: []string{ScopeEventsRead}},
	}}
	got := GrantFor(tok, "id-1")
	if len(got) != 2 || got[0] != "app:read" || got[1] != ScopeEventsRead {
		t.Fatalf("GrantFor = %v, want both id-1 grants", got)
	}
	if !eventsGrantMatches(tok, "id-1", "") {
		t.Fatal("events grant on a later duplicate entry must match")
	}
}
