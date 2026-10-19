package main

import (
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
	ct "github.com/randy-girard/flynn/controller/types"
)

func TestUserScalePolicyError(t *testing.T) {
	s := &ct.RuntimeSettings{AllowUserScale: false, AllowUserScaleTags: false}
	cur := &ct.Formation{Processes: map[string]int{"web": 1}, Tags: map[string]map[string]string{"web": {"role": "web"}}}
	user := &authorizer.Token{UserID: "u1"}
	if err := userScalePolicyError(user, s, cur, map[string]int{"web": 2}, nil); err == nil {
		t.Fatal("user scale counts must be rejected")
	}
	if err := userScalePolicyError(user, s, cur, map[string]int{"web": 1}, map[string]map[string]string{"web": {"role": "database"}}); err == nil {
		t.Fatal("user scale tags must be rejected")
	}
	if err := userScalePolicyError(user, s, cur, map[string]int{"web": 1}, map[string]map[string]string{}); err != nil {
		t.Fatalf("empty tags is not a tag update: %v", err)
	}
	admin := &authorizer.Token{Scopes: []string{"cluster:admin"}}
	if err := userScalePolicyError(admin, s, cur, map[string]int{"web": 5}, map[string]map[string]string{"web": {"role": "database"}}); err != nil {
		t.Fatalf("admin: %v", err)
	}
}

func TestScaleTagsRequested(t *testing.T) {
	if scaleTagsRequested(nil) || scaleTagsRequested(map[string]map[string]string{"web": {}}) {
		t.Fatal("empty")
	}
	if !scaleTagsRequested(map[string]map[string]string{"web": {"role": "db"}}) {
		t.Fatal("wanted tags")
	}
}
