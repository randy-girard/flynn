package authz

import (
	"net/http"
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
)

func TestRegionRouteAuth(t *testing.T) {
	tenant := &authorizer.Token{UserID: "u1", AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:write"}}}}
	key := &authorizer.Token{ClusterKey: true}

	if !HTTPAllowed(tenant, http.MethodGet, "/cluster/regions") {
		t.Fatal("a tenant must be able to list regions")
	}
	if HTTPAllowed(tenant, http.MethodPost, "/cluster/regions") {
		t.Fatal("a tenant must not link an arbitrary URL")
	}
	if HTTPAllowed(tenant, http.MethodDelete, "/cluster/regions/EU") {
		t.Fatal("a tenant must not unlink a region")
	}
	if HTTPAllowed(tenant, http.MethodPut, "/cluster/region") {
		t.Fatal("a tenant must not set the cluster region")
	}
	if !HTTPAllowed(key, http.MethodPost, "/cluster/regions") {
		t.Fatal("the cluster key must link with no user account")
	}
	if !HTTPAllowed(key, http.MethodPut, "/cluster/region") {
		t.Fatal("the cluster key must set the region at bootstrap")
	}
	if !ClusterRegionPublic(http.MethodGet, "/cluster/region") {
		t.Fatal("the peer dial reads GET /cluster/region with no cluster key")
	}
	if ClusterRegionPublic(http.MethodPost, "/cluster/region") || ClusterRegionPublic(http.MethodGet, "/cluster/regions") {
		t.Fatal("only GET /cluster/region is public")
	}
}
