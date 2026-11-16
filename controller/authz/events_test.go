package authz

import (
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
)

func TestEventsGrantAllowsOwnAppOnly(t *testing.T) {
	own := &authorizer.Token{
		UserID: "acct-a",
		Scopes: []string{ScopeEventsRead},
		AppGrants: []authorizer.AppGrant{{
			AppID: "app-a", Permissions: []string{ScopeEventsRead},
		}},
	}
	if !EventsGrantAllows(own, "app-a", "name-a", "user:acct-a") {
		t.Fatal("own app must be allowed")
	}
	if EventsGrantAllows(own, "app-b", "name-b", "user:acct-b") {
		t.Fatal("other account must be denied")
	}
	copied := &authorizer.Token{
		UserID: "acct-a",
		Scopes: []string{ScopeEventsRead},
		AppGrants: []authorizer.AppGrant{{
			AppID: "app-b", Permissions: []string{ScopeEventsRead},
		}},
	}
	if EventsGrantAllows(copied, "app-b", "name-b", "user:acct-b") {
		t.Fatal("a grant for another account's app must be denied")
	}
	watcher := &authorizer.Token{
		UserID: "events:app-a",
		Scopes: []string{ScopeEventsRead},
		AppGrants: []authorizer.AppGrant{{
			AppID: "app-a", Permissions: []string{ScopeEventsRead},
		}},
	}
	if !EventsGrantAllows(watcher, "app-a", "name-a", "user:someone") {
		t.Fatal("per-app watcher is bound by the grant, not by account")
	}
}

func TestNoteScopedRejectOmitsToken(t *testing.T) {
	const secret = "super-secret-token-value"
	var got ScopedRejection
	prev := OnScopedReject
	OnScopedReject = func(r ScopedRejection) { got = r }
	defer func() { OnScopedReject = prev }()
	NoteScopedReject(&authorizer.Token{
		UserID: "acct-a",
		User:   secret,
		Scopes: []string{ScopeEventsRead},
	}, "GET", "/events", "app_id required")
	if got.Scope != ScopeEventsRead || got.Caller != "acct-a" || got.Reason != "app_id required" {
		t.Fatalf("rejection = %+v", got)
	}
	if got.Scope == secret || got.Caller == secret || got.Path == secret || got.Reason == secret {
		t.Fatal("rejection recorded the token")
	}
}
