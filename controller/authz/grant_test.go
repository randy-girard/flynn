package authz

import (
	"net/http"
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

// A request path may name the app by name or id. The grant is by id, so the
// route check resolves the path segment first.
func TestHTTPAllowedResolvesPathRefToID(t *testing.T) {
	const victim = "11111111-2222-3333-4444-555555555555"
	resolve := func(ref string) string {
		switch ref {
		case "demo":
			return "id-demo"
		case victim:
			return "id-victim"
		}
		return ref
	}
	own := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "id-demo", Permissions: []string{"app:write"}}}}
	if !HTTPAllowedResolved(own, http.MethodGet, "/apps/demo/releases", resolve) {
		t.Error("by name: own app must be allowed")
	}
	if !HTTPAllowedResolved(own, http.MethodGet, "/apps/id-demo/releases", resolve) {
		t.Error("by id: own app must be allowed")
	}
	// A legacy app whose name is the victim's id carries a grant under that
	// name; resolving the path to the victim's real id must not match it.
	legacy := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: victim, Permissions: []string{"app:write"}}}}
	if HTTPAllowedResolved(legacy, http.MethodGet, "/apps/"+victim+"/releases", resolve) {
		t.Error("a grant keyed by another app's id must not cover the victim app")
	}
}
