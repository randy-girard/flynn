package main

import "testing"

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
