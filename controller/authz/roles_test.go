package authz

import (
	"net/http"
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
)

func TestDefaultAppRolesGrantMapping(t *testing.T) {
	want := map[string]string{
		"view":   PermAppRead,
		"deploy": PermAppDeploy,
		"manage": PermAppWrite,
		"admin":  PermAppAdmin,
	}
	if len(DefaultAppRoles) != 4 {
		t.Fatalf("DefaultAppRoles len=%d, want 4", len(DefaultAppRoles))
	}
	seen := map[string]bool{}
	for _, r := range DefaultAppRoles {
		if seen[r.ID] {
			t.Fatalf("duplicate role id %q", r.ID)
		}
		seen[r.ID] = true
		perm, ok := want[r.ID]
		if !ok {
			t.Fatalf("unexpected default role %q", r.ID)
		}
		if len(r.Permissions) != 1 || r.Permissions[0] != perm {
			t.Fatalf("role %s permissions=%v, want [%s]", r.ID, r.Permissions, perm)
		}
		if r.Name == "" || r.Description == "" {
			t.Fatalf("role %s missing name or description", r.ID)
		}
		got, ok := DefaultRoleByID(r.ID)
		if !ok || got.Name != r.Name {
			t.Fatalf("DefaultRoleByID(%s) = %+v, ok=%v", r.ID, got, ok)
		}
	}
	if _, ok := DefaultRoleByID("nope"); ok {
		t.Fatal("unknown role id must not resolve")
	}
}

func TestKnownAppPermission(t *testing.T) {
	for _, p := range AppPermissionCatalog {
		if !KnownAppPermission(p) {
			t.Fatalf("%s must be a known app permission", p)
		}
	}
	if KnownAppPermission("cluster:admin") || KnownAppPermission("app:delete") || KnownAppPermission("") {
		t.Fatal("cluster and invented verbs must not count as app permissions")
	}
}

func TestDefaultAppRolesHTTP(t *testing.T) {
	tok := func(roleID string) *authorizer.Token {
		r, ok := DefaultRoleByID(roleID)
		if !ok {
			t.Fatalf("missing role %s", roleID)
		}
		return &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: r.Permissions}}}
	}

	cases := []struct {
		role    string
		method  string
		path    string
		allowed bool
	}{
		{"view", http.MethodGet, "/apps/app-1", true},
		{"view", http.MethodPost, "/apps/app-1/deploy", false},
		{"view", http.MethodPost, "/apps/app-1/releases", false},
		{"deploy", http.MethodGet, "/apps/app-1", true},
		{"deploy", http.MethodPost, "/apps/app-1/deploy", true},
		{"deploy", http.MethodPost, "/apps/app-1/releases", false},
		{"deploy", http.MethodPost, "/apps/app-1/scale", false},
		{"manage", http.MethodGet, "/apps/app-1", true},
		{"manage", http.MethodPost, "/apps/app-1/deploy", true},
		{"manage", http.MethodPost, "/apps/app-1/releases", true},
		{"manage", http.MethodPost, "/apps/app-1/scale", true},
		{"admin", http.MethodGet, "/apps/app-1", true},
		{"admin", http.MethodPost, "/apps/app-1/deploy", true},
		{"admin", http.MethodPost, "/apps/app-1/releases", true},
		{"admin", http.MethodDelete, "/apps/app-1", true},
	}
	for _, tc := range cases {
		t.Run(tc.role+"_"+tc.method+"_"+tc.path, func(t *testing.T) {
			got := HTTPAllowed(tok(tc.role), tc.method, tc.path)
			if got != tc.allowed {
				t.Fatalf("HTTPAllowed(%s, %s %s) = %v, want %v", tc.role, tc.method, tc.path, got, tc.allowed)
			}
		})
	}
}
