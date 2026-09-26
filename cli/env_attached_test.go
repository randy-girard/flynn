package main

import (
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestRejectAttachedURLSet(t *testing.T) {
	attached := map[string]string{"DATABASE_URL": "postgres://leader.pginst.discoverd:5432/db"}
	next := "postgres://elsewhere/db"
	err := rejectAttachedURLSet(attached, map[string]*string{"DATABASE_URL": &next})
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("env set: %v", err)
	}
	foo := "ok"
	if err := rejectAttachedURLSet(attached, map[string]*string{"FOO": &foo}); err != nil {
		t.Fatal(err)
	}
	if err := rejectAttachedURLSet(map[string]string{}, map[string]*string{"DATABASE_URL": &next}); err != nil {
		t.Fatalf("detached: %v", err)
	}
}

func TestAttachedURLKeys(t *testing.T) {
	keys := attachedURLKeys([]*ct.Resource{{
		Env: map[string]string{"ANALYTICS_URL": "postgres://a", "NOT_THIS": "x"},
	}})
	if len(keys) != 1 || keys["ANALYTICS_URL"] == "" {
		t.Fatalf("keys: %#v", keys)
	}
	if _, ok := keys["NOT_THIS"]; ok {
		t.Fatalf("non-URL env must stay writable: %#v", keys)
	}
}
