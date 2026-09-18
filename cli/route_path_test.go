package main

import (
	"net/url"
	"testing"

	router "github.com/randy-girard/flynn/router/types"
)

func TestFlynnCLIRejectsPathHTTPRoutes(t *testing.T) {
	u, err := url.Parse("http://example.com/admin")
	if err != nil {
		t.Fatal(err)
	}
	if !router.HTTPPathRequiresClusterAdmin(u.Path) {
		t.Fatal("example.com/admin must be treated as a path route")
	}
}

func TestFlynnCLIAllowsApexHTTPRoutes(t *testing.T) {
	u, err := url.Parse("http://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if router.HTTPPathRequiresClusterAdmin(u.Path) {
		t.Fatalf("apex path %q must not require cluster admin", u.Path)
	}
}
