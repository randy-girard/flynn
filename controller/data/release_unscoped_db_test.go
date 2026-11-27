package data

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

// A release must belong to an app: migration 27 soft-deleted the old
// unscoped releases and added CHECK (app_id IS NOT NULL OR deleted_at IS NOT
// NULL). ReleaseRepo.Add refuses an empty app_id up front so every caller
// (HTTP, gRPC) gets a validation error instead of a database error (NEW-60).
func TestReleaseAddRequiresAppID(t *testing.T) {
	db := smallPoolDB(t, "controller_release_unscoped_test")
	releases := NewReleaseRepo(db, NewArtifactRepo(db), nil)
	err := releases.Add(&ct.Release{})
	verr, ok := err.(ct.ValidationError)
	if !ok {
		t.Fatalf("Add without app_id = %#v, want ct.ValidationError", err)
	}
	if verr.Field != "app_id" {
		t.Fatalf("field = %q, want app_id", verr.Field)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM releases").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d releases stored, want 0", n)
	}
}
