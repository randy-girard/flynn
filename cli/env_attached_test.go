package main

import (
	"strings"
	"testing"

	"github.com/randy-girard/flynn/pkg/resname"
)

func TestRejectLockedSet(t *testing.T) {
	locked := map[string]string{
		"DATABASE_URL":                 "postgres://leader.pginst.discoverd:5432/db",
		"PG_DELTA_PCLPEZ_DATABASE_URL": "postgres://leader.pginst.discoverd:5432/db",
	}
	next := "postgres://elsewhere/db"
	err := rejectLockedSet(locked, map[string]*string{"DATABASE_URL": &next})
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("env set: %v", err)
	}
	if err := rejectLockedSet(locked, map[string]*string{"PG_DELTA_PCLPEZ_DATABASE_URL": &next}); err == nil || !strings.Contains(err.Error(), "PG_DELTA_PCLPEZ_DATABASE_URL") {
		t.Fatalf("scoped url: %v", err)
	}
	if err := rejectLockedSet(locked, map[string]*string{"DATABASE_URL": nil}); err == nil {
		t.Fatal("unset of a locked key must fail")
	}
	foo := "ok"
	if err := rejectLockedSet(locked, map[string]*string{"FOO": &foo}); err != nil {
		t.Fatal(err)
	}
	if err := rejectLockedSet(map[string]string{}, map[string]*string{"DATABASE_URL": &next}); err != nil {
		t.Fatalf("detached: %v", err)
	}
}

func TestLockedKeysFromResourceEnv(t *testing.T) {
	release := map[string]string{"DATABASE_URL": "postgres://a", "NOTES": "x"}
	keys := resname.LockedKeys(release, map[string]string{"ANALYTICS_URL": "postgres://a", "DATABASE_URL": "postgres://a", "NOT_THIS": "x"})
	if keys["DATABASE_URL"] == "" {
		t.Fatalf("keys: %#v", keys)
	}
	if _, ok := keys["NOT_THIS"]; ok {
		t.Fatalf("keys not on the release must stay writable: %#v", keys)
	}
	if _, ok := keys["NOTES"]; ok {
		t.Fatalf("unrelated release env: %#v", keys)
	}
}
