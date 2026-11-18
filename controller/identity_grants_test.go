package main

import (
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
)

const victimAppUUID = "11111111-2222-3333-4444-555555555555"

// An app's grant is keyed by its id only. A name key would let an app whose
// name is another app's id collect that app's grants.
func TestExpandTokenGrantsByIDOnly(t *testing.T) {
	rows := []data.AppAccess{{
		AppID:        "aaaaaaaa-0000-0000-0000-000000000001",
		Name:         victimAppUUID,
		OwnerAccount: "user:u1",
	}}
	grants := appGrantsFromAccess("u1", rows, nil)
	if len(grants) != 1 {
		t.Fatalf("grants = %+v, want exactly one keyed by app id", grants)
	}
	if grants[0].AppID != rows[0].AppID {
		t.Fatalf("grant keyed %q, want app id %q", grants[0].AppID, rows[0].AppID)
	}
	for _, g := range grants {
		if g.AppID == victimAppUUID {
			t.Fatalf("grant keyed by the app name %q", g.AppID)
		}
	}
}

func TestAppVisibleMatchesGrantByIDOnly(t *testing.T) {
	app := &ct.App{ID: "a1", Name: "demo", OwnerAccount: "user:owner"}
	byName := &authorizer.Token{UserID: "u2", AppGrants: []authorizer.AppGrant{{AppID: "demo", Permissions: []string{"app:read"}}}}
	if appVisible(byName, app) {
		t.Fatal("a grant keyed by app name must not make the app visible")
	}
	byID := &authorizer.Token{UserID: "u2", AppGrants: []authorizer.AppGrant{{AppID: "a1", Permissions: []string{"app:read"}}}}
	if !appVisible(byID, app) {
		t.Fatal("a grant keyed by app id must make the app visible")
	}
}

func TestGrantPermsMatchesIDOnly(t *testing.T) {
	app := &ct.App{ID: "a1", Name: "demo"}
	byName := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "demo", Permissions: []string{"app:admin"}}}}
	if got := grantPerms(byName, app); len(got) != 0 {
		t.Fatalf("grantPerms by name = %v, want none", got)
	}
}
