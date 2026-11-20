package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/random"
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

func pluginInstallMeta() map[string]string {
	return map[string]string{"flynn-system-app": "true", "flynn-plugin": "true"}
}

func TestCatalogInternalApp(t *testing.T) {
	// Only apps Flynn already installed (flynn-plugin=true) stay on GET /apps.
	installed := []string{
		"dashboard-plugin", "postgres-plugin", "redis-plugin", "mysql-plugin",
		"mongodb-plugin", "kafka-plugin", "clickhouse-plugin", "github-plugin",
		"widget-plugin",
	}
	for _, name := range installed {
		app := &ct.App{Name: name, Meta: pluginInstallMeta()}
		if catalogInternalApp(app) {
			t.Errorf("%s: installed plugin must stay on GET /apps", name)
		}
	}
	for _, name := range []string{"controller", "postgres", "blobstore", "gitreceive", "router"} {
		app := &ct.App{Name: name, Meta: map[string]string{"flynn-system-app": "true"}}
		if !catalogInternalApp(app) {
			t.Errorf("%s: bootstrap system app must stay off GET /apps", name)
		}
	}
	if catalogInternalApp(&ct.App{Name: "myapp"}) {
		t.Fatal("user app is not internal")
	}
	if catalogInternalApp(&ct.App{Name: "widget", Meta: map[string]string{"flynn-plugin": "true"}}) {
		t.Fatal("plugin meta without system meta is still a plugin")
	}
	// A known plugin name with no install meta is not an installed plugin.
	if !catalogInternalApp(&ct.App{Name: "postgres", Meta: map[string]string{"flynn-system-app": "true"}}) {
		t.Fatal("platform postgres without flynn-plugin is still a system app")
	}
}

func TestFilterVisibleApps(t *testing.T) {
	userApp := &ct.App{ID: "user-1", Name: "myapp", OwnerAccount: "user:u1"}
	otherApp := &ct.App{ID: "user-2", Name: "other", OwnerAccount: "user:u2"}
	unowned := &ct.App{ID: "op-1", Name: "ops-app"}
	controllerApp := &ct.App{ID: "sys-1", Name: "controller", Meta: map[string]string{"flynn-system-app": "true"}}
	postgresApp := &ct.App{ID: "sys-2", Name: "postgres", Meta: map[string]string{"flynn-system-app": "true"}}
	pluginApp := &ct.App{ID: "plug-1", Name: "dashboard", Meta: pluginInstallMeta()}
	postgresPlugin := &ct.App{ID: "plug-pg", Name: "postgres-plugin", Meta: pluginInstallMeta()}
	redisPlugin := &ct.App{ID: "plug-redis", Name: "redis-plugin", Meta: pluginInstallMeta()}
	mysqlPlugin := &ct.App{ID: "plug-mysql", Name: "mysql-plugin", Meta: pluginInstallMeta()}
	mongoPlugin := &ct.App{ID: "plug-mongo", Name: "mongodb-plugin", Meta: pluginInstallMeta()}
	kafkaPlugin := &ct.App{ID: "plug-kafka", Name: "kafka-plugin", Meta: pluginInstallMeta()}
	clickhousePlugin := &ct.App{ID: "plug-ch", Name: "clickhouse-plugin", Meta: pluginInstallMeta()}
	githubPlugin := &ct.App{ID: "plug-gh", Name: "github-plugin", Meta: pluginInstallMeta()}
	futurePlugin := &ct.App{ID: "plug-widget", Name: "widget-plugin", Meta: pluginInstallMeta()}
	collabApp := &ct.App{ID: "col-1", Name: "shared", OwnerAccount: "user:u2"}
	all := []*ct.App{
		userApp, otherApp, unowned, controllerApp, postgresApp, pluginApp,
		postgresPlugin, redisPlugin, mysqlPlugin, mongoPlugin, kafkaPlugin,
		clickhousePlugin, githubPlugin, futurePlugin, collabApp,
	}

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

	t.Run("cluster_admin_default_lists_plugins_hides_system", func(t *testing.T) {
		tok := &authorizer.Token{ClusterKey: true}
		ctx := context.WithValue(context.Background(), authz.TokenContextKey, tok)
		got := names(api.filterVisibleApps(ctx, false, all))
		for _, want := range []string{
			"myapp", "other", "ops-app", "shared",
			"dashboard", "postgres-plugin", "redis-plugin", "mysql-plugin",
			"mongodb-plugin", "kafka-plugin", "clickhouse-plugin", "github-plugin",
			"widget-plugin",
		} {
			if !has(got, want) {
				t.Fatalf("admin default list = %v, missing %q", got, want)
			}
		}
		for _, banned := range []string{"controller", "postgres"} {
			if has(got, banned) {
				t.Fatalf("admin default list = %v must hide bootstrap system app %q", got, banned)
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
		t.Fatal("user must not see plugin apps they were not granted")
	}
	pluginGrant := &authorizer.Token{UserID: "u1", AppGrants: []authorizer.AppGrant{{AppID: "p1", Permissions: []string{"app:read"}}}}
	if !appVisible(pluginGrant, plugin) {
		t.Fatal("collaborator grant must list a Flynn-installed plugin")
	}
}

func TestValidUserUUID(t *testing.T) {
	if validUserUUID("11111111-1111-4111-8111-111111111111") != true {
		t.Fatal("want valid uuid")
	}
	if validUserUUID("user:11111111-1111-4111-8111-111111111111") {
		t.Fatal("prefix must be stripped before validUserUUID")
	}
	if validUserUUID("not-a-uuid") {
		t.Fatal("dashboard base64 ids are not uuids")
	}
}

func (s *S) domainCall(c *C, auth, path, body string) (int, []byte) {
	req, err := http.NewRequest(http.MethodPost, s.srv.URL+path, strings.NewReader(body))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/json")
	if auth == authKey {
		req.SetBasicAuth("", auth)
	} else {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	c.Assert(err, IsNil)
	return res.StatusCode, raw
}

func (s *S) claimDomain(c *C, auth, hostname, owner string) ct.DomainVerification {
	code, raw := s.domainCall(c, auth, "/domains/verify", fmt.Sprintf(`{"hostname":%q,"owner_account":%q}`, hostname, owner))
	c.Assert(code, Equals, 200, Commentf("%s", raw))
	var d ct.DomainVerification
	c.Assert(json.Unmarshal(raw, &d), IsNil)
	c.Assert(d.Token, Not(Equals), "")
	return d
}

func (s *S) domainVerifiedAt(c *C, hostname string) *time.Time {
	var at *time.Time
	c.Assert(s.hc.db.QueryRow(`SELECT verified_at FROM verified_domains WHERE hostname = $1`, hostname).Scan(&at), IsNil)
	return at
}

func (s *S) withTXT(fn func(ctx context.Context, name string) ([]string, error)) func() {
	prev := s.api.lookupTXT
	s.api.lookupTXT = fn
	return func() { s.api.lookupTXT = prev }
}

func core20Suffix() string { return strings.ReplaceAll(random.UUID(), "-", "")[:10] }

// TestCheckDomainIgnoresClientRecordsForNonAdmin: CORE-20. A tenant used to
// pass its own token in "records" and the controller marked the domain
// verified with no DNS lookup. Only the cluster key may supply records. A
// tenant gets a validation error and the row stays unverified.
func (s *S) TestCheckDomainIgnoresClientRecordsForNonAdmin(c *C) {
	userID := "core20-" + core20Suffix()
	tok := s.signUser(c, userID, nil)
	host := userID + ".example.com"
	d := s.claimDomain(c, tok, host, "user:"+userID)
	var looked []string
	defer s.withTXT(func(ctx context.Context, name string) ([]string, error) {
		looked = append(looked, name)
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	})()

	code, raw := s.domainCall(c, tok, "/domains/verify/"+host+"/check", fmt.Sprintf(`{"records":[%q]}`, d.Token))
	c.Assert(code, Equals, 400, Commentf("%s", raw))
	var apiErr hh.JSONError
	c.Assert(json.Unmarshal(raw, &apiErr), IsNil)
	c.Assert(apiErr.Code, Equals, hh.ValidationErrorCode)
	c.Assert(s.domainVerifiedAt(c, host), IsNil)

	// Without records the controller does the lookup itself; NXDOMAIN fails.
	code, raw = s.domainCall(c, tok, "/domains/verify/"+host+"/check", `{}`)
	c.Assert(code, Equals, 400, Commentf("%s", raw))
	c.Assert(looked, DeepEquals, []string{"_flynn-verify." + host})
	c.Assert(s.domainVerifiedAt(c, host), IsNil)
}

// TestCheckDomainUsesControllerTXTLookup: the tenant flow still works when the
// TXT record is really published. The lookup has a deadline.
func (s *S) TestCheckDomainUsesControllerTXTLookup(c *C) {
	userID := "core20-" + core20Suffix()
	tok := s.signUser(c, userID, nil)
	host := "shop." + userID + ".example.com"
	d := s.claimDomain(c, tok, host, "user:"+userID)
	c.Assert(d.TXTName, Equals, "_flynn-verify."+host)
	hadDeadline := false
	defer s.withTXT(func(ctx context.Context, name string) ([]string, error) {
		_, hadDeadline = ctx.Deadline()
		if name != "_flynn-verify."+host {
			return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
		}
		return []string{"other", d.Token}, nil
	})()

	code, raw := s.domainCall(c, tok, "/domains/verify/"+host+"/check", `{}`)
	c.Assert(code, Equals, 200, Commentf("%s", raw))
	c.Assert(hadDeadline, Equals, true)
	c.Assert(s.domainVerifiedAt(c, host), NotNil)
}

// TestCheckDomainClusterKeyMaySupplyRecords keeps the operator and test path.
func (s *S) TestCheckDomainClusterKeyMaySupplyRecords(c *C) {
	userID := "core20-" + core20Suffix()
	host := userID + ".example.org"
	d := s.claimDomain(c, authKey, host, "user:"+userID)
	defer s.withTXT(func(ctx context.Context, name string) ([]string, error) {
		c.Errorf("cluster key with records must not look up %s", name)
		return nil, nil
	})()
	code, raw := s.domainCall(c, authKey, "/domains/verify/"+host+"/check", fmt.Sprintf(`{"records":[%q]}`, d.Token))
	c.Assert(code, Equals, 200, Commentf("%s", raw))
	c.Assert(s.domainVerifiedAt(c, host), NotNil)
}

// TestVerifyDomainRejectsPublicSuffix: CORE-20. A claim on "com" (or co.uk,
// github.io, the cluster's own domains) would let HostnameAllowed accept
// every hostname under it. No token is issued and no row is written.
func (s *S) TestVerifyDomainRejectsPublicSuffix(c *C) {
	var prevAppDomain string
	c.Assert(s.hc.db.QueryRow(`SELECT app_domain FROM cluster_settings WHERE id = 1`).Scan(&prevAppDomain), IsNil)
	c.Assert(s.hc.db.Exec(`UPDATE cluster_settings SET app_domain = 'apps.core20-platform.net' WHERE id = 1`), IsNil)
	defer func() {
		c.Assert(s.hc.db.Exec(`UPDATE cluster_settings SET app_domain = $1 WHERE id = 1`, prevAppDomain), IsNil)
	}()
	userID := "core20-" + core20Suffix()
	tok := s.signUser(c, userID, nil)
	for _, host := range []string{
		"com", "co.uk", "github.io", "localhost", "10.0.0.1", "*.example.com",
		"apps.core20-platform.net", "x.apps.core20-platform.net", "core20-platform.net",
	} {
		for _, auth := range []string{tok, authKey} {
			code, raw := s.domainCall(c, auth, "/domains/verify", fmt.Sprintf(`{"hostname":%q,"owner_account":%q}`, host, "user:"+userID))
			c.Assert(code, Equals, 400, Commentf("%s: %s", host, raw))
			var apiErr hh.JSONError
			c.Assert(json.Unmarshal(raw, &apiErr), IsNil)
			c.Assert(apiErr.Code, Equals, hh.ValidationErrorCode)
			var n int
			c.Assert(s.hc.db.QueryRow(`SELECT COUNT(*) FROM verified_domains WHERE hostname = $1`, host).Scan(&n), IsNil)
			c.Assert(n, Equals, 0, Commentf("%s", host))
		}
	}
}

// TestHostedRouteIgnoresStoredPublicSuffixVerification: a "com" row verified
// before CORE-20 must not let a hosted tenant route another customer's name.
func (s *S) TestHostedRouteIgnoresStoredPublicSuffixVerification(c *C) {
	defer s.restoreTenancyDefaults(c)
	userID := "core20-" + core20Suffix()
	owner := "user:" + userID
	c.Assert(s.hc.db.Exec(`INSERT INTO verified_domains (hostname, owner_account, token, verified_at) VALUES ('com', $1, 'x', now()) ON CONFLICT (hostname) DO UPDATE SET owner_account = EXCLUDED.owner_account, verified_at = now()`, owner), IsNil)
	defer s.hc.db.Exec(`DELETE FROM verified_domains WHERE hostname = 'com'`)
	c.Assert(s.api.tenancy.SetSettings(tenancy.ModeHosted, nil), IsNil)
	app := &ct.App{ID: random.UUID(), Name: userID, OwnerAccount: owner}
	err := s.api.checkHostname(context.Background(), app, "victim-"+userID+".com")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*requires a verified domain.*")
}
