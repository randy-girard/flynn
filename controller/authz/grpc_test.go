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
	legacy := &authorizer.Token{}
	if !GRPCAllowed(legacy, "/anything") {
		t.Fatal("legacy unsigned dashboard token is cluster admin")
	}
}
