package authz

import (
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
)

func TestGRPCAllowed(t *testing.T) {
	if GRPCAllowed(nil, "/flynn.api.v1.Controller/Status") {
		t.Fatal("nil token")
	}
	admin := &authorizer.Token{ClusterKey: true}
	if !GRPCAllowed(admin, "/flynn.api.v1.Controller/StreamJobs") {
		t.Fatal("cluster admin")
	}
	scoped := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1"}}}
	if !GRPCAllowed(scoped, "/flynn.api.v1.Controller/Status") {
		t.Fatal("app-scoped status")
	}
	if GRPCAllowed(scoped, "/flynn.api.v1.Controller/StreamJobs") {
		t.Fatal("app-scoped must not stream jobs")
	}
	empty := &authorizer.Token{}
	if GRPCAllowed(empty, "/anything") {
		t.Fatal("empty token must not be cluster admin")
	}
	star := &authorizer.Token{Scopes: []string{"*"}}
	if !GRPCAllowed(star, "/anything") {
		t.Fatal("* scope is cluster admin")
	}
}
