package main

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
	"golang.org/x/net/context"
)

func TestWantCatalogAll(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", false},
		{"/apps", false},
		{"/apps?all=1", true},
		{"/apps?all=true", true},
		{"/apps?all=TRUE", true},
		{"/apps?all=yes", true},
		{"/apps?all=0", false},
		{"/apps?all=false", false},
		{"/apps?system=1", true},
		{"/apps?system=true", true},
		{"/apps?foo=1", false},
	}
	for _, tc := range cases {
		req, err := http.NewRequest(http.MethodGet, tc.raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := wantCatalogAll(req); got != tc.want {
			t.Errorf("wantCatalogAll(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
	if wantCatalogAll(nil) {
		t.Fatal("nil request must not request the operator catalog")
	}
	if wantCatalogAll(&http.Request{URL: &url.URL{}}) {
		t.Fatal("empty query must not request the operator catalog")
	}
}

func TestFilterVisibleApps(t *testing.T) {
	userApp := &ct.App{ID: "user-1", Name: "myapp", OwnerAccount: "user:u1"}
	otherApp := &ct.App{ID: "user-2", Name: "other", OwnerAccount: "user:u2"}
	unowned := &ct.App{ID: "op-1", Name: "ops-app"}
	controllerApp := &ct.App{ID: "sys-1", Name: "controller", Meta: map[string]string{"flynn-system-app": "true"}}
	postgresApp := &ct.App{ID: "sys-2", Name: "postgres", Meta: map[string]string{"flynn-system-app": "true"}}
	pluginApp := &ct.App{ID: "plug-1", Name: "dashboard", Meta: map[string]string{"flynn-plugin": "true"}}
	collabApp := &ct.App{ID: "col-1", Name: "shared", OwnerAccount: "user:u2"}
	all := []*ct.App{userApp, otherApp, unowned, controllerApp, postgresApp, pluginApp, collabApp}

	api := &controllerAPI{}
	names := func(list interface{}) []string {
		apps, ok := list.([]*ct.App)
		if !ok {
			t.Fatalf("list type %T", list)
		}
		out := make([]string, 0, len(apps))
		for _, a := range apps {
			out = append(out, a.Name)
		}
		return out
	}
	has := func(got []string, name string) bool {
		for _, n := range got {
			if n == name {
				return true
			}
		}
		return false
	}

	t.Run("user_token_owned_and_collaborator_only", func(t *testing.T) {
		tok := &authorizer.Token{
			UserID:    "u1",
			AppGrants: []authorizer.AppGrant{{AppID: collabApp.ID, Permissions: []string{"app:read"}}},
		}
		ctx := context.WithValue(context.Background(), authz.TokenContextKey, tok)
		got := names(api.filterVisibleApps(ctx, false, all))
		if !has(got, "myapp") || !has(got, "shared") {
			t.Fatalf("user list = %v, want owned myapp and collaborator shared", got)
		}
		for _, banned := range []string{"other", "ops-app", "controller", "postgres", "dashboard"} {
			if has(got, banned) {
				t.Fatalf("user list = %v must not include %q", got, banned)
			}
		}
	})

	t.Run("user_token_all_query_still_filtered", func(t *testing.T) {
		tok := &authorizer.Token{UserID: "u1"}
		ctx := context.WithValue(context.Background(), authz.TokenContextKey, tok)
		got := names(api.filterVisibleApps(ctx, true, all))
		if !has(got, "myapp") {
			t.Fatalf("user list = %v, want owned myapp", got)
		}
		if has(got, "controller") || has(got, "other") || has(got, "dashboard") {
			t.Fatalf("?all=1 must not dump the catalog for a user token: %v", got)
		}
	})

	t.Run("user_token_without_grants_does_not_dump_catalog", func(t *testing.T) {
		tok := &authorizer.Token{UserID: "nobody"}
		ctx := context.WithValue(context.Background(), authz.TokenContextKey, tok)
		got := names(api.filterVisibleApps(ctx, false, all))
		if len(got) != 0 {
			t.Fatalf("stranger user list = %v, want empty", got)
		}
	})

	t.Run("cluster_admin_default_hides_system_and_plugin", func(t *testing.T) {
		tok := &authorizer.Token{ClusterKey: true}
		ctx := context.WithValue(context.Background(), authz.TokenContextKey, tok)
		got := names(api.filterVisibleApps(ctx, false, all))
		for _, want := range []string{"myapp", "other", "ops-app", "shared"} {
			if !has(got, want) {
				t.Fatalf("admin default list = %v, missing user app %q", got, want)
			}
		}
		for _, banned := range []string{"controller", "postgres", "dashboard"} {
			if has(got, banned) {
				t.Fatalf("admin default list = %v must hide %q", got, banned)
			}
		}
	})

	t.Run("cluster_admin_all_includes_system_and_plugin", func(t *testing.T) {
		tok := &authorizer.Token{Scopes: []string{"cluster:admin"}}
		ctx := context.WithValue(context.Background(), authz.TokenContextKey, tok)
		got := names(api.filterVisibleApps(ctx, true, all))
		if len(got) != len(all) {
			t.Fatalf("admin --all list = %v, want every app", got)
		}
		for _, want := range []string{"controller", "postgres", "dashboard", "myapp"} {
			if !has(got, want) {
				t.Fatalf("admin --all list = %v, missing %q", got, want)
			}
		}
	})

	t.Run("nil_token_unchanged", func(t *testing.T) {
		got := api.filterVisibleApps(context.Background(), false, all)
		if len(got.([]*ct.App)) != len(all) {
			t.Fatalf("nil token must not rewrite the list")
		}
	})
}

func TestAppVisible(t *testing.T) {
	app := &ct.App{ID: "a1", Name: "myapp", OwnerAccount: "user:u1"}
	sys := &ct.App{ID: "s1", Name: "controller", Meta: map[string]string{"flynn-system-app": "true"}}
	plugin := &ct.App{ID: "p1", Name: "www", Meta: map[string]string{"flynn-plugin": "true"}}

	if appVisible(nil, app) {
		t.Fatal("nil token")
	}
	if !appVisible(&authorizer.Token{UserID: "u1"}, app) {
		t.Fatal("owner must see their app even without grants")
	}
	if appVisible(&authorizer.Token{UserID: "u2"}, app) {
		t.Fatal("other user must not see the app")
	}
	collab := &authorizer.Token{UserID: "u2", AppGrants: []authorizer.AppGrant{{AppID: "a1", Permissions: []string{"app:deploy"}}}}
	if !appVisible(collab, app) {
		t.Fatal("collaborator grant must make the app visible")
	}
	if appVisible(&authorizer.Token{UserID: "u1"}, sys) {
		t.Fatal("user must not see system apps")
	}
	if appVisible(&authorizer.Token{UserID: "u1", AppGrants: []authorizer.AppGrant{{AppID: "s1", Permissions: []string{"app:read"}}}}, sys) {
		t.Fatal("grants must not expose system apps on the catalog")
	}
	if appVisible(&authorizer.Token{UserID: "u1"}, plugin) {
		t.Fatal("user must not see plugin apps")
	}
}
