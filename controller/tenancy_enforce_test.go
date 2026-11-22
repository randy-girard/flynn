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
	return s.domainReq(c, http.MethodPost, auth, path, body)
}

func (s *S) domainReq(c *C, method, auth, path, body string) (int, []byte) {
	req, err := http.NewRequest(method, s.srv.URL+path, strings.NewReader(body))
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

func (s *S) domainRow(c *C, hostname string) (owner, token string, verified bool) {
	var at *time.Time
	c.Assert(s.hc.db.QueryRow(`SELECT owner_account, token, verified_at FROM verified_domains WHERE hostname = $1`, hostname).Scan(&owner, &token, &at), IsNil)
	return owner, token, at != nil
}

func (s *S) countDomainRows(c *C, hostname string) int {
	var n int
	c.Assert(s.hc.db.QueryRow(`SELECT COUNT(*) FROM verified_domains WHERE hostname = $1`, hostname).Scan(&n), IsNil)
	return n
}

// TestVerifyDomainDoesNotReplaceOtherAccountClaim: CORE-55. Another account
// used to overwrite an unverified claim (owner and token) and could keep
// replacing the token so the real owner never verified. A claim now holds
// for domainClaimTTL. After that an unverified claim may be taken over; a
// verified one never.
func (s *S) TestVerifyDomainDoesNotReplaceOtherAccountClaim(c *C) {
	a := "core55-a-" + core20Suffix()
	b := "core55-b-" + core20Suffix()
	tokA, tokB := s.signUser(c, a, nil), s.signUser(c, b, nil)
	host := "shop." + a + ".example.com"
	defer s.hc.db.Exec(`DELETE FROM verified_domains WHERE hostname = $1`, host)

	first := s.claimDomain(c, tokA, host, "user:"+a)
	for _, auth := range []string{tokB, authKey} {
		code, raw := s.domainCall(c, auth, "/domains/verify", fmt.Sprintf(`{"hostname":%q,"owner_account":%q}`, host, "user:"+b))
		c.Assert(code, Equals, 409, Commentf("%s", raw))
		c.Assert(strings.Contains(string(raw), "token"), Equals, false, Commentf("%s", raw))
	}
	owner, token, _ := s.domainRow(c, host)
	c.Assert(owner, Equals, "user:"+a)
	c.Assert(token, Equals, first.Token)

	// The owner may re-claim (new token) without extending the hold.
	var claimedBefore, claimedAfter time.Time
	c.Assert(s.hc.db.QueryRow(`SELECT claimed_at FROM verified_domains WHERE hostname = $1`, host).Scan(&claimedBefore), IsNil)
	again := s.claimDomain(c, tokA, host, "user:"+a)
	c.Assert(again.Token, Not(Equals), first.Token)
	c.Assert(s.hc.db.QueryRow(`SELECT claimed_at FROM verified_domains WHERE hostname = $1`, host).Scan(&claimedAfter), IsNil)
	c.Assert(claimedAfter.Equal(claimedBefore), Equals, true)

	// An unverified claim older than the TTL may be replaced.
	c.Assert(s.hc.db.Exec(`UPDATE verified_domains SET claimed_at = now() - $2::bigint * interval '1 second' WHERE hostname = $1`,
		host, int64((domainClaimTTL+time.Hour)/time.Second)), IsNil)
	taken := s.claimDomain(c, tokB, host, "user:"+b)
	owner, token, _ = s.domainRow(c, host)
	c.Assert(owner, Equals, "user:"+b)
	c.Assert(token, Equals, taken.Token)

	// A verified claim is never replaced by another account, however old.
	c.Assert(s.hc.db.Exec(`UPDATE verified_domains SET verified_at = now(), claimed_at = now() - interval '365 days' WHERE hostname = $1`, host), IsNil)
	code, raw := s.domainCall(c, tokA, "/domains/verify", fmt.Sprintf(`{"hostname":%q,"owner_account":%q}`, host, "user:"+a))
	c.Assert(code, Equals, 409, Commentf("%s", raw))
	owner, token, verified := s.domainRow(c, host)
	c.Assert(owner, Equals, "user:"+b)
	c.Assert(token, Equals, taken.Token)
	c.Assert(verified, Equals, true)
}

// TestDeleteDomainClaimOwnerOnly: CORE-55. An admin of the owning account or
// the cluster key may delete a claim; nobody else may.
func (s *S) TestDeleteDomainClaimOwnerOnly(c *C) {
	a := "core55-a-" + core20Suffix()
	b := "core55-b-" + core20Suffix()
	tokA, tokB := s.signUser(c, a, nil), s.signUser(c, b, nil)
	host := a + ".example.net"
	other := "www." + a + ".example.net"
	defer s.hc.db.Exec(`DELETE FROM verified_domains WHERE hostname IN ($1, $2)`, host, other)
	s.claimDomain(c, tokA, host, "user:"+a)
	s.claimDomain(c, tokA, other, "user:"+a)

	code, raw := s.domainReq(c, http.MethodDelete, tokB, "/domains/"+host, "")
	c.Assert(code, Equals, 403, Commentf("%s", raw))
	c.Assert(s.countDomainRows(c, host), Equals, 1)

	code, raw = s.domainReq(c, http.MethodDelete, tokA, "/domains/"+strings.ToUpper(host)+".", "")
	c.Assert(code, Equals, 200, Commentf("%s", raw))
	c.Assert(s.countDomainRows(c, host), Equals, 0)

	code, _ = s.domainReq(c, http.MethodDelete, tokA, "/domains/"+host, "")
	c.Assert(code, Equals, 404)

	// Operators clear claims with the cluster key; the name is free again.
	code, raw = s.domainReq(c, http.MethodDelete, authKey, "/domains/"+other, "")
	c.Assert(code, Equals, 200, Commentf("%s", raw))
	c.Assert(s.countDomainRows(c, other), Equals, 0)
	s.claimDomain(c, tokB, other, "user:"+b)
	var n int
	c.Assert(s.hc.db.QueryRow(`SELECT COUNT(*) FROM core_audit WHERE action = 'domain.delete' AND data->>'hostname' IN ($1, $2)`, host, other).Scan(&n), IsNil)
	c.Assert(n, Equals, 2)
}

// TestListDomainsOwnerOnly: CORE-55. GET /domains?owner_account= lists an
// account's claims for its admins. With no owner only the cluster key or a
// cluster admin may list every claim.
func (s *S) TestListDomainsOwnerOnly(c *C) {
	a := "core55-a-" + core20Suffix()
	b := "core55-b-" + core20Suffix()
	tokA, tokB := s.signUser(c, a, nil), s.signUser(c, b, nil)
	hostsA := []string{"b." + a + ".example.com", "a." + a + ".example.com"}
	hostB := b + ".example.com"
	defer s.hc.db.Exec(`DELETE FROM verified_domains WHERE hostname IN ($1, $2, $3)`, hostsA[0], hostsA[1], hostB)
	claimA := map[string]string{}
	for _, h := range hostsA {
		claimA[h] = s.claimDomain(c, tokA, h, "user:"+a).Token
	}
	s.claimDomain(c, tokB, hostB, "user:"+b)

	list := func(auth, query string) (int, []ct.DomainVerification, []byte) {
		code, raw := s.domainReq(c, http.MethodGet, auth, "/domains"+query, "")
		var out []ct.DomainVerification
		if code == 200 {
			c.Assert(json.Unmarshal(raw, &out), IsNil, Commentf("%s", raw))
		}
		return code, out, raw
	}

	code, got, raw := list(tokA, "?owner_account="+url.QueryEscape("user:"+a))
	c.Assert(code, Equals, 200, Commentf("%s", raw))
	c.Assert(got, HasLen, 2)
	c.Assert(got[0].Hostname, Equals, hostsA[1]) // sorted by hostname
	c.Assert(got[1].Hostname, Equals, hostsA[0])
	for _, d := range got {
		c.Assert(d.OwnerAccount, Equals, "user:"+a)
		c.Assert(d.Token, Equals, claimA[d.Hostname])
		c.Assert(d.TXTName, Equals, "_flynn-verify."+d.Hostname)
		c.Assert(d.ClaimedAt, NotNil)
		c.Assert(d.VerifiedAt, IsNil)
	}

	code, _, raw = list(tokA, "?owner_account="+url.QueryEscape("user:"+b))
	c.Assert(code, Equals, 403, Commentf("%s", raw))
	code, _, raw = list(tokA, "")
	c.Assert(code, Equals, 400, Commentf("%s", raw))

	code, got, raw = list(authKey, "")
	c.Assert(code, Equals, 200, Commentf("%s", raw))
	seen := map[string]bool{}
	for _, d := range got {
		seen[d.Hostname] = true
	}
	for _, h := range append(hostsA, hostB) {
		c.Assert(seen[h], Equals, true, Commentf("%s", h))
	}
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

func core30User(c *C, s *S, tag string) *ct.User {
	r := core20Suffix()
	return s.createLoginUser(c, "core30-"+tag+"-"+r+"@flynn.test", "c30"+tag+r, "pw-"+r, false)
}

func (s *S) core30CreateApp(c *C, auth string, body map[string]string) (int, *ct.App, []byte) {
	raw, err := json.Marshal(body)
	c.Assert(err, IsNil)
	code, out := s.domainCall(c, auth, "/apps", string(raw))
	if code != 200 {
		return code, nil, out
	}
	app := &ct.App{}
	c.Assert(json.Unmarshal(out, app), IsNil)
	return code, app, out
}

func (s *S) core30AppCount(c *C, name string) int {
	var n int
	c.Assert(s.hc.db.QueryRow(`SELECT COUNT(*) FROM apps WHERE name = $1 AND deleted_at IS NULL`, name).Scan(&n), IsNil)
	return n
}

// TestPrepareNewAppRejectsForeignOwner: CORE-30. POST /apps kept any
// owner_account the caller sent, so a tenant could create apps in another
// user's or org's account (their quota, their app list, their hostnames, and
// the CORE-37 datastore guard's "same account" rule). A non-admin may name
// only an account it administers. created_by always comes from the token.
func (s *S) TestPrepareNewAppRejectsForeignOwner(c *C) {
	a := core30User(c, s, "a")
	victim := core30User(c, s, "victim")
	tokA := s.signUser(c, a.ID, nil)

	for _, owner := range []string{"user:" + victim.ID, "org:core30-" + core20Suffix(), "user:" + random.UUID()} {
		name := "core30-" + core20Suffix()
		code, _, raw := s.core30CreateApp(c, tokA, map[string]string{"name": name, "owner_account": owner})
		c.Assert(code, Equals, 403, Commentf("%s: %s", owner, raw))
		c.Assert(s.core30AppCount(c, name), Equals, 0, Commentf("%s", owner))
	}

	// An org member that is not owner/admin cannot create apps in the org.
	member := "org:core30-" + core20Suffix()
	c.Assert(s.api.tenancy.PutMembership(ct.Membership{UserID: a.ID, Subject: member, Role: "member"}), IsNil)
	code, _, raw := s.core30CreateApp(c, tokA, map[string]string{"name": "core30-" + core20Suffix(), "owner_account": member})
	c.Assert(code, Equals, 403, Commentf("%s", raw))

	// Accounts the caller administers still work: its own, an org it admins,
	// and a user account where it is an admin collaborator.
	org := "org:core30-" + core20Suffix()
	c.Assert(s.api.tenancy.PutMembership(ct.Membership{UserID: a.ID, Subject: org, Role: "admin"}), IsNil)
	shared := core30User(c, s, "shared")
	c.Assert(s.api.tenancy.UpsertAccountCollaborator("user:"+shared.ID, a.ID, "admin"), IsNil)
	for _, owner := range []string{"", "user:" + a.ID, org, "user:" + shared.ID} {
		code, app, raw := s.core30CreateApp(c, tokA, map[string]string{"name": "core30-" + core20Suffix(), "owner_account": owner})
		c.Assert(code, Equals, 200, Commentf("%q: %s", owner, raw))
		want := owner
		if want == "" {
			want = "user:" + a.ID
		}
		c.Assert(app.OwnerAccount, Equals, want)
		c.Assert(app.CreatedBy, Equals, a.ID)
	}
}

// TestPrepareNewAppCreatedByFromToken: CORE-30. A tenant could name any
// existing user in created_by. The token's user wins for non-admins.
func (s *S) TestPrepareNewAppCreatedByFromToken(c *C) {
	a := core30User(c, s, "a")
	victim := core30User(c, s, "victim")
	tokA := s.signUser(c, a.ID, nil)
	for _, by := range []string{victim.ID, "user:" + victim.ID} {
		code, app, raw := s.core30CreateApp(c, tokA, map[string]string{"name": "core30-" + core20Suffix(), "created_by": by})
		c.Assert(code, Equals, 200, Commentf("%s", raw))
		c.Assert(app.CreatedBy, Equals, a.ID)
		got, err := s.api.appRepo.Get(app.ID)
		c.Assert(err, IsNil)
		c.Assert(got.(*ct.App).CreatedBy, Equals, a.ID)
	}
}

// TestPrepareNewAppClusterKeyKeepsOwner: bootstrap, plugins on the controller
// key (pipeline review apps, datastore instance apps) and `flynn import` as
// an operator still create apps for any account and keep created_by.
func (s *S) TestPrepareNewAppClusterKeyKeepsOwner(c *C) {
	victim := core30User(c, s, "victim")
	creator := core30User(c, s, "creator")
	code, app, raw := s.core30CreateApp(c, authKey, map[string]string{
		"name": "core30-" + core20Suffix(), "owner_account": "user:" + victim.ID, "created_by": creator.ID,
	})
	c.Assert(code, Equals, 200, Commentf("%s", raw))
	c.Assert(app.OwnerAccount, Equals, "user:"+victim.ID)
	c.Assert(app.CreatedBy, Equals, creator.ID)
}

// TestPrepareNewAppHostedForeignOwnerNotCounted: CORE-30 in hosted mode. The
// victim's max_apps must not be used up by another tenant's creates.
func (s *S) TestPrepareNewAppHostedForeignOwnerNotCounted(c *C) {
	defer s.restoreTenancyDefaults(c)
	c.Assert(s.api.tenancy.SetSettings(tenancy.ModeHosted, nil), IsNil)
	a := core30User(c, s, "a")
	victim := core30User(c, s, "victim")
	tokA := s.signUser(c, a.ID, nil)
	code, _, raw := s.core30CreateApp(c, tokA, map[string]string{"name": "core30-" + core20Suffix(), "owner_account": "user:" + victim.ID})
	c.Assert(code, Equals, 403, Commentf("%s", raw))
	n, err := s.api.tenancy.CountOwnedApps("user:" + victim.ID)
	c.Assert(err, IsNil)
	c.Assert(n, Equals, 0)
	code, app, raw := s.core30CreateApp(c, tokA, map[string]string{"name": "core30-" + core20Suffix()})
	c.Assert(code, Equals, 200, Commentf("%s", raw))
	c.Assert(app.OwnerAccount, Equals, "user:"+a.ID)
}

// injectRowFailure makes the next writes to one row of table fail inside
// postgres: a BEFORE trigger raises for rows whose column equals value. It
// stands in for a repo error (lost connection, constraint, timeout) at a
// precise point of a handler. The returned func removes the trigger.
func (s *S) injectRowFailure(c *C, table, op, column, value string) func() {
	name := "core45_fail_" + strings.ReplaceAll(random.UUID(), "-", "")
	c.Assert(s.hc.db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger AS $f$
BEGIN
	IF NEW.%s::text = '%s' THEN
		RAISE EXCEPTION 'injected %s failure';
	END IF;
	RETURN NEW;
END
$f$ LANGUAGE plpgsql`, name, column, value, table)), IsNil)
	c.Assert(s.hc.db.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE %s ON %s FOR EACH ROW EXECUTE FUNCTION %s()`, name, op, table, name)), IsNil)
	return func() {
		c.Assert(s.hc.db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s`, name, table)), IsNil)
		c.Assert(s.hc.db.Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, name)), IsNil)
	}
}

// keyStatus sends one request with the cluster key.
func (s *S) keyStatus(c *C, method, path, body string) (int, string) {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.srv.URL+path, r)
	c.Assert(err, IsNil)
	req.Header.Set("Accept", "application/json")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetBasicAuth("", authKey)
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	got, err := io.ReadAll(res.Body)
	c.Assert(err, IsNil)
	return res.StatusCode, string(got)
}

// suspendFixture is an account with one app running web=2.
func (s *S) suspendFixture(c *C, account string) (*ct.App, *ct.Release) {
	app := s.createTestApp(c, &ct.App{Name: "core45-" + random.String(8), OwnerAccount: account})
	release := s.createTestRelease(c, app.ID, &ct.Release{Processes: map[string]ct.ProcessType{"web": {}}})
	s.createTestFormation(c, &ct.Formation{AppID: app.ID, ReleaseID: release.ID, Processes: map[string]int{"web": 2}})
	return app, release
}

// POST /accounts/:id/suspend answered 200 when the scale-to-zero of the
// account's apps failed, so billing believed the tenant was stopped while
// its processes kept running. The failure is reported, and a retry after
// the fault clears scales the app down.
func (s *S) TestSuspendReportsScaleFailure(c *C) {
	account := "user:" + random.UUID()
	app, release := s.suspendFixture(c, account)
	undo := s.injectRowFailure(c, "scale_requests", "INSERT", "app_id", app.ID)
	code, body := s.keyStatus(c, http.MethodPost, "/accounts/"+account+"/suspend", `{}`)
	undo()
	c.Assert(code, Equals, http.StatusInternalServerError, Commentf("body=%s", body))
	f, err := s.c.GetFormation(app.ID, release.ID)
	c.Assert(err, IsNil)
	c.Assert(f.Processes["web"], Equals, 2)

	code, body = s.keyStatus(c, http.MethodPost, "/accounts/"+account+"/suspend", `{}`)
	c.Assert(code, Equals, http.StatusOK, Commentf("body=%s", body))
	f, err = s.c.GetFormation(app.ID, release.ID)
	c.Assert(err, IsNil)
	c.Assert(f.Processes["web"], Equals, 0)
}

// Suspending a user account must also mark the user row suspended (that is
// what blocks its logins); a failed write was ignored.
func (s *S) TestSuspendReportsUserUpdateFailure(c *C) {
	tag := random.String(8)
	u := s.createLoginUser(c, "core45-"+tag+"@flynn.test", "core45-"+tag, "pw-"+tag, false)
	undo := s.injectRowFailure(c, "users", "UPDATE", "id", u.ID)
	code, body := s.keyStatus(c, http.MethodPost, "/accounts/user:"+u.ID+"/suspend", `{}`)
	undo()
	c.Assert(code, Equals, http.StatusInternalServerError, Commentf("body=%s", body))
	got, err := s.api.tenancy.GetUser(u.ID)
	c.Assert(err, IsNil)
	c.Assert(got.Suspended, Equals, false)
}

// Unsuspend dropped the same user-row error, leaving logins blocked while
// answering 200.
func (s *S) TestUnsuspendReportsUserUpdateFailure(c *C) {
	tag := random.String(8)
	u := s.createLoginUser(c, "core45u-"+tag+"@flynn.test", "core45u-"+tag, "pw-"+tag, false)
	code, body := s.keyStatus(c, http.MethodPost, "/accounts/user:"+u.ID+"/suspend", `{}`)
	c.Assert(code, Equals, http.StatusOK, Commentf("body=%s", body))
	undo := s.injectRowFailure(c, "users", "UPDATE", "id", u.ID)
	code, body = s.keyStatus(c, http.MethodPost, "/accounts/user:"+u.ID+"/unsuspend", `{}`)
	undo()
	c.Assert(code, Equals, http.StatusInternalServerError, Commentf("body=%s", body))
	got, err := s.api.tenancy.GetUser(u.ID)
	c.Assert(err, IsNil)
	c.Assert(got.Suspended, Equals, true)
}

// PATCH /users/:id {"suspended":true} runs the same scale-to-zero and
// ignored both its error and the account_state write error.
func (s *S) TestPatchUserSuspendReportsScaleFailure(c *C) {
	tag := random.String(8)
	u := s.createLoginUser(c, "core45p-"+tag+"@flynn.test", "core45p-"+tag, "pw-"+tag, false)
	app, release := s.suspendFixture(c, "user:"+u.ID)
	undo := s.injectRowFailure(c, "scale_requests", "INSERT", "app_id", app.ID)
	code, body := s.keyStatus(c, http.MethodPatch, "/users/"+u.ID, `{"suspended":true}`)
	undo()
	c.Assert(code, Equals, http.StatusInternalServerError, Commentf("body=%s", body))
	f, err := s.c.GetFormation(app.ID, release.ID)
	c.Assert(err, IsNil)
	c.Assert(f.Processes["web"], Equals, 2)

	code, body = s.keyStatus(c, http.MethodPatch, "/users/"+u.ID, `{"suspended":true}`)
	c.Assert(code, Equals, http.StatusOK, Commentf("body=%s", body))
	f, err = s.c.GetFormation(app.ID, release.ID)
	c.Assert(err, IsNil)
	c.Assert(f.Processes["web"], Equals, 0)
	got, err := s.api.tenancy.GetUser(u.ID)
	c.Assert(err, IsNil)
	c.Assert(got.Suspended, Equals, true)
}

// PATCH /users/:id {"suspended":true} ignored a failed account_state write,
// so the account was not suspended for quota and authz checks.
func (s *S) TestPatchUserSuspendReportsAccountStateFailure(c *C) {
	tag := random.String(8)
	u := s.createLoginUser(c, "core45s-"+tag+"@flynn.test", "core45s-"+tag, "pw-"+tag, false)
	undo := s.injectRowFailure(c, "account_state", "INSERT OR UPDATE", "account", "user:"+u.ID)
	code, body := s.keyStatus(c, http.MethodPatch, "/users/"+u.ID, `{"suspended":true}`)
	undo()
	c.Assert(code, Equals, http.StatusInternalServerError, Commentf("body=%s", body))
	got, err := s.api.tenancy.GetUser(u.ID)
	c.Assert(err, IsNil)
	c.Assert(got.Suspended, Equals, false)
}

// ageDomainCheck makes a verified row look last checked longer ago than
// domainRecheckAge, the way it would after a day without a re-check.
func (s *S) ageDomainCheck(c *C, hostname string) {
	c.Assert(s.hc.db.Exec(`UPDATE verified_domains SET verified_at = now() - $2::bigint * interval '1 second', checked_at = now() - $2::bigint * interval '1 second' WHERE hostname = $1`,
		hostname, int64((domainRecheckAge+time.Hour)/time.Second)), IsNil)
}

func (s *S) domainCheckedAt(c *C, hostname string) *time.Time {
	var at *time.Time
	c.Assert(s.hc.db.QueryRow(`SELECT checked_at FROM verified_domains WHERE hostname = $1`, hostname).Scan(&at), IsNil)
	return at
}

// TestHostedRouteNeedsFreshVerification: CORE-56. verified_at used to stay
// set forever, so after a domain expired or changed hands the old account
// could still add routes under it. A verification last checked more than
// domainRecheckAge ago is no longer trusted on its own: checkHostname looks
// the TXT record up again. A match refreshes checked_at; NXDOMAIN or a
// wrong value clears verification; a failed lookup refuses the route and
// keeps the row as it is.
func (s *S) TestHostedRouteNeedsFreshVerification(c *C) {
	defer s.restoreTenancyDefaults(c)
	userID := "core56-" + core20Suffix()
	owner := "user:" + userID
	host := userID + ".example.com"
	route := "www." + host
	defer s.hc.db.Exec(`DELETE FROM verified_domains WHERE hostname = $1`, host)
	d := s.claimDomain(c, authKey, host, owner)
	code, raw := s.domainCall(c, authKey, "/domains/verify/"+host+"/check", fmt.Sprintf(`{"records":[%q]}`, d.Token))
	c.Assert(code, Equals, 200, Commentf("%s", raw))
	c.Assert(s.domainCheckedAt(c, host), NotNil)
	c.Assert(s.api.tenancy.SetSettings(tenancy.ModeHosted, nil), IsNil)
	app := &ct.App{ID: random.UUID(), Name: userID, OwnerAccount: owner}

	var looked []string
	answer := func(ctx context.Context, name string) ([]string, error) { return nil, fmt.Errorf("unexpected lookup") }
	defer s.withTXT(func(ctx context.Context, name string) ([]string, error) {
		looked = append(looked, name)
		return answer(ctx, name)
	})()

	// Fresh: no lookup.
	c.Assert(s.api.checkHostname(context.Background(), app, route), IsNil)
	c.Assert(looked, HasLen, 0)

	// Stale and the record is still published: allowed, checked_at moves.
	s.ageDomainCheck(c, host)
	answer = func(ctx context.Context, name string) ([]string, error) { return []string{d.Token}, nil }
	c.Assert(s.api.checkHostname(context.Background(), app, route), IsNil)
	c.Assert(looked, DeepEquals, []string{"_flynn-verify." + host})
	checked := s.domainCheckedAt(c, host)
	c.Assert(checked, NotNil)
	c.Assert(time.Since(*checked) < time.Minute, Equals, true, Commentf("checked_at=%v", checked))
	c.Assert(s.api.checkHostname(context.Background(), app, route), IsNil)
	c.Assert(looked, HasLen, 1)

	// Stale and the lookup fails: refused, verification kept.
	s.ageDomainCheck(c, host)
	answer = func(ctx context.Context, name string) ([]string, error) {
		return nil, &net.DNSError{Err: "server misbehaving", Name: name, IsTemporary: true}
	}
	err := s.api.checkHostname(context.Background(), app, route)
	c.Assert(err, ErrorMatches, ".*could not re-check.*")
	c.Assert(s.domainVerifiedAt(c, host), NotNil)

	// Stale and the record is gone (the domain expired or changed hands):
	// refused and the row is unverified.
	answer = func(ctx context.Context, name string) ([]string, error) {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	err = s.api.checkHostname(context.Background(), app, route)
	c.Assert(err, ErrorMatches, ".*requires a verified domain.*")
	c.Assert(s.domainVerifiedAt(c, host), IsNil)
	c.Assert(s.domainCheckedAt(c, host), IsNil)
	var n int
	c.Assert(s.hc.db.QueryRow(`SELECT COUNT(*) FROM core_audit WHERE action = 'domain.unverify' AND data->>'hostname' = $1`, host).Scan(&n), IsNil)
	c.Assert(n, Equals, 1)

	// A wrong value clears it the same way.
	code, raw = s.domainCall(c, authKey, "/domains/verify/"+host+"/check", fmt.Sprintf(`{"records":[%q]}`, d.Token))
	c.Assert(code, Equals, 200, Commentf("%s", raw))
	s.ageDomainCheck(c, host)
	answer = func(ctx context.Context, name string) ([]string, error) { return []string{"someone-else"}, nil }
	c.Assert(s.api.checkHostname(context.Background(), app, route), ErrorMatches, ".*requires a verified domain.*")
	c.Assert(s.domainVerifiedAt(c, host), IsNil)
}

// TestVerifyDomainRechecksStaleVerifiedClaim: CORE-56. A verified claim
// blocked every other account forever, even after the domain changed hands.
// A claim by another account on a stale verified name re-checks the TXT
// record first: still published keeps the 409; gone clears verification so
// the hold rules apply.
func (s *S) TestVerifyDomainRechecksStaleVerifiedClaim(c *C) {
	a := "core56-a-" + core20Suffix()
	b := "core56-b-" + core20Suffix()
	tokA, tokB := s.signUser(c, a, nil), s.signUser(c, b, nil)
	host := "shop." + a + ".example.com"
	defer s.hc.db.Exec(`DELETE FROM verified_domains WHERE hostname = $1`, host)
	first := s.claimDomain(c, tokA, host, "user:"+a)
	c.Assert(s.hc.db.Exec(`UPDATE verified_domains SET claimed_at = now() - interval '365 days' WHERE hostname = $1`, host), IsNil)
	code, raw := s.domainCall(c, authKey, "/domains/verify/"+host+"/check", fmt.Sprintf(`{"records":[%q]}`, first.Token))
	c.Assert(code, Equals, 200, Commentf("%s", raw))

	var looked int
	answer := func(name string) ([]string, error) { return nil, fmt.Errorf("unexpected lookup") }
	defer s.withTXT(func(ctx context.Context, name string) ([]string, error) {
		looked++
		return answer(name)
	})()
	claimB := func() (int, []byte) {
		return s.domainCall(c, tokB, "/domains/verify", fmt.Sprintf(`{"hostname":%q,"owner_account":%q}`, host, "user:"+b))
	}

	// Fresh verified claim: 409 with no lookup.
	code, raw = claimB()
	c.Assert(code, Equals, 409, Commentf("%s", raw))
	c.Assert(looked, Equals, 0)

	// Stale, still published: 409 and checked_at refreshed.
	s.ageDomainCheck(c, host)
	answer = func(name string) ([]string, error) { return []string{first.Token}, nil }
	code, raw = claimB()
	c.Assert(code, Equals, 409, Commentf("%s", raw))
	c.Assert(looked, Equals, 1)
	checked := s.domainCheckedAt(c, host)
	c.Assert(checked, NotNil)
	c.Assert(time.Since(*checked) < time.Minute, Equals, true)

	// Stale, record gone: B takes the name.
	s.ageDomainCheck(c, host)
	answer = func(name string) ([]string, error) {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	code, raw = claimB()
	c.Assert(code, Equals, 200, Commentf("%s", raw))
	owner, _, verified := s.domainRow(c, host)
	c.Assert(owner, Equals, "user:"+b)
	c.Assert(verified, Equals, false)
}

// TestReclaimClearsCheckedAt: an owner re-claim (new token) clears
// checked_at with verified_at, so a later check starts from scratch.
func (s *S) TestReclaimClearsCheckedAt(c *C) {
	a := "core56-" + core20Suffix()
	tokA := s.signUser(c, a, nil)
	host := a + ".example.org"
	defer s.hc.db.Exec(`DELETE FROM verified_domains WHERE hostname = $1`, host)
	d := s.claimDomain(c, tokA, host, "user:"+a)
	code, raw := s.domainCall(c, authKey, "/domains/verify/"+host+"/check", fmt.Sprintf(`{"records":[%q]}`, d.Token))
	c.Assert(code, Equals, 200, Commentf("%s", raw))
	var got ct.DomainVerification
	c.Assert(json.Unmarshal(raw, &got), IsNil)
	c.Assert(got.CheckedAt, NotNil)
	s.claimDomain(c, tokA, host, "user:"+a)
	c.Assert(s.domainVerifiedAt(c, host), IsNil)
	c.Assert(s.domainCheckedAt(c, host), IsNil)
}
