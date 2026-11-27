package main

import (
	"encoding/json"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestDefaultResourceOwnerUsesTheLoggedInUser(t *testing.T) {
	got, err := defaultResourceOwner("", "ada", false)
	if err != nil || got != "user:ada" {
		t.Fatalf("personal account %q %v", got, err)
	}
	got, err = defaultResourceOwner("org:acme", "ada", false)
	if err != nil || got != "org:acme" {
		t.Fatalf("organization %q %v", got, err)
	}
	got, err = defaultResourceOwner("  user:ada  ", "", false)
	if err != nil || got != "user:ada" {
		t.Fatalf("trimmed account %q %v", got, err)
	}
	got, err = defaultResourceOwner("", "", true)
	if err != nil || got != "" {
		t.Fatalf("cluster key %q %v", got, err)
	}
	got, err = defaultResourceOwner("", "", false)
	if err != nil || got != "" {
		t.Fatalf("anonymous %q %v", got, err)
	}
	if _, err := defaultResourceOwner("acme", "ada", false); err == nil {
		t.Fatal("a handle must be resolved to user:<id> or org:<id> before provision")
	}
	if _, err := defaultResourceOwner("enterprise_account:ent", "ada", false); err == nil {
		t.Fatal("an enterprise account is not a resource owner")
	}
}

func TestProvisionConfigAppIsOverwrittenFromRequest(t *testing.T) {
	target := &ct.App{ID: "id1", Name: "attacker"}
	get := func(t *testing.T, raw []byte) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	forged := []byte(`{"follow":"redis-harbor-48291","app":"victim","tenant":"victim"}`)
	got := get(t, provisionConfigWithApp(forged, target, false))
	if got["app"] != "attacker" || got["tenant"] != "attacker" || got["follow"] != "redis-harbor-48291" {
		t.Fatalf("caller app/tenant must be replaced by the request app: %v", got)
	}
	// No target app: a non-cluster caller cannot name one either.
	got = get(t, provisionConfigWithApp(forged, nil, false))
	if _, ok := got["app"]; ok {
		t.Fatalf("app kept without a target: %v", got)
	}
	if _, ok := got["tenant"]; ok {
		t.Fatalf("tenant kept without a target: %v", got)
	}
	// A cluster key (bootstrap, migration) may still pass its own app.
	got = get(t, provisionConfigWithApp([]byte(`{"app":"boot"}`), nil, true))
	if got["app"] != "boot" {
		t.Fatalf("cluster key config changed: %v", got)
	}
	got = get(t, provisionConfigWithApp(nil, target, false))
	if got["app"] != "attacker" || got["tenant"] != "attacker" {
		t.Fatalf("empty config: %v", got)
	}
}

// NEW-27: an unattached provision (addons:create --unattached --follow) has
// no app, so the plugin cannot tell whose leader it may follow. The
// controller stamps the resolved owner account, never the caller's value.
func TestProvisionConfigStampsOwnerAccount(t *testing.T) {
	get := func(t *testing.T, raw []byte) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	forged := []byte(`{"follow":"mysql-gorge-85967","owner_account":"user:victim"}`)
	got := get(t, provisionConfigWithOwner(forged, nil, "user:ada", false))
	if got["owner_account"] != "user:ada" || got["follow"] != "mysql-gorge-85967" {
		t.Fatalf("unattached provision must carry the resolved owner: %v", got)
	}
	target := &ct.App{ID: "id1", Name: "shop", OwnerAccount: "org:acme"}
	got = get(t, provisionConfigWithOwner(forged, target, "org:acme", false))
	if got["owner_account"] != "org:acme" || got["app"] != "shop" {
		t.Fatalf("attached provision carries the app's owner: %v", got)
	}
	got = get(t, provisionConfigWithOwner(forged, nil, "", false))
	if _, ok := got["owner_account"]; ok {
		t.Fatalf("a caller-supplied owner_account must not reach the plugin: %v", got)
	}
}
