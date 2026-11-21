package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/flynn/go-check"
	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/random"
	pgtestutils "github.com/randy-girard/flynn/pkg/testutils/postgres"
	router "github.com/randy-girard/flynn/router/types"
)

// queryCounter is a pgx logger that records every statement a pool runs.
// pgx logs one "Query" (with its row count) or "Exec" line per statement.
type queryCounter struct {
	mu      sync.Mutex
	queries []countedQuery
}

type countedQuery struct {
	sql  string
	rows int
}

func (q *queryCounter) Info(msg string, kv ...interface{}) {
	if msg != "Query" && msg != "Exec" {
		return
	}
	var cq countedQuery
	for i := 0; i+1 < len(kv); i += 2 {
		switch kv[i] {
		case "sql":
			cq.sql, _ = kv[i+1].(string)
		case "rowCount":
			// pgx counts Next calls: a result read to the end reports
			// one more than its rows.
			if n, _ := kv[i+1].(int); n > 0 {
				cq.rows = n - 1
			}
		}
	}
	q.mu.Lock()
	q.queries = append(q.queries, cq)
	q.mu.Unlock()
}
func (q *queryCounter) Debug(string, ...interface{}) {}
func (q *queryCounter) Warn(string, ...interface{})  {}
func (q *queryCounter) Error(string, ...interface{}) {}

func (q *queryCounter) reset() {
	q.mu.Lock()
	q.queries = nil
	q.mu.Unlock()
}

func (q *queryCounter) snapshot() []countedQuery {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]countedQuery(nil), q.queries...)
}

func (q *queryCounter) count() int { return len(q.snapshot()) }

// rowsFor is the row count of every run of the named prepared statement.
func (q *queryCounter) rowsFor(name string) []int {
	sql := data.PreparedSQL(name)
	var out []int
	for _, cq := range q.snapshot() {
		if sql != "" && cq.sql == sql {
			out = append(out, cq.rows)
		}
	}
	return out
}

type countingServer struct {
	url     string
	api     *controllerAPI
	pool    *pgx.ConnPool
	counter *queryCounter
	close   func()
}

// newCountingServer is a second controller on the test database whose pool
// counts statements. maxConns is the pool size (pgx needs at least 2).
func (s *S) newCountingServer(c *C, maxConns int) *countingServer {
	qc := &queryCounter{}
	cfg, err := pgtestutils.ConnConfigForDatabase("controllertest")
	c.Assert(err, IsNil)
	cfg.Logger = qc
	cfg.LogLevel = pgx.LogLevelInfo
	pool, err := pgx.NewConnPool(pgx.ConnPoolConfig{
		ConnConfig:     cfg,
		MaxConnections: maxConns,
		AcquireTimeout: 5 * time.Second,
		AfterConnect:   data.PrepareStatements,
	})
	c.Assert(err, IsNil)
	hc := s.hc
	hc.db = postgres.New(pool, nil)
	handler, _, capi := appHandler(hc)
	srv := httptest.NewServer(handler)
	return &countingServer{url: srv.URL, api: capi, pool: pool, counter: qc, close: func() {
		srv.Close()
		pool.Close()
	}}
}

// get runs one request and returns the status, body and statement count.
func (cs *countingServer) do(c *C, method, path, bearer, body string) (int, string, int) {
	req, err := http.NewRequest(method, cs.url+path, strings.NewReader(body))
	c.Assert(err, IsNil)
	if bearer == authKey {
		req.SetBasicAuth("", authKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	cs.counter.reset()
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	got, err := io.ReadAll(res.Body)
	c.Assert(err, IsNil)
	return res.StatusCode, string(got), cs.counter.count()
}

func resourceIDSet(c *C, body string) []string {
	var list []*ct.Resource
	c.Assert(json.Unmarshal([]byte(body), &list), IsNil, Commentf("body=%s", body))
	ids := make([]string, 0, len(list))
	for _, r := range list {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	return ids
}

// userCtx is a request context for a user token after expandToken, the way
// the HTTP middleware builds it.
func (s *S) userCtx(c *C, userID string) context.Context {
	tok := &authorizer.Token{UserID: userID}
	c.Assert(s.api.expandToken(tok), IsNil)
	return context.WithValue(context.Background(), authz.TokenContextKey, tok)
}

// perRowVisibleResources is the pre-CORE-34 filter: one accessForAccount
// (three queries) per listed resource. The batched list must return exactly
// these rows.
func (s *S) perRowVisibleResources(ctx context.Context, res []*ct.Resource) []string {
	ids := []string{}
	for _, item := range res {
		if item.OwnerAccount == "" {
			continue
		}
		if s.api.canAdminOwner(ctx, item.OwnerAccount) {
			ids = append(ids, item.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// A tenant's GET /resources and GET /providers/:id/resources used to cost
// three queries for every resource in the cluster (accessForAccount per row)
// on a five-connection pool. The count must not depend on how many
// resources or accounts the cluster holds, and the rows must stay the same:
// own account, org owner/admin, admin account collaborator; not org member,
// non-admin collaborator, a suspended org, another tenant, or unowned.
func (s *S) TestFilterResourcesQueriesOncePerAccount(c *C) {
	tag := random.String(6)
	viewer := s.createLoginUser(c, "c34-res-v-"+tag+"@flynn.test", "c34-res-v-"+tag, "pw-c34-"+tag, false)
	sharer := s.createLoginUser(c, "c34-res-s-"+tag+"@flynn.test", "c34-res-s-"+tag, "pw-c34-"+tag, false)
	manager := s.createLoginUser(c, "c34-res-m-"+tag+"@flynn.test", "c34-res-m-"+tag, "pw-c34-"+tag, false)
	stranger := s.createLoginUser(c, "c34-res-x-"+tag+"@flynn.test", "c34-res-x-"+tag, "pw-c34-"+tag, false)
	p := s.createTestProvider(c, &ct.Provider{URL: "https://c34-" + tag + ".example.com", Name: "c34-res-" + tag})

	orgAdmin, orgOwner, orgMember, orgSusp := "org:c34a-"+tag, "org:c34o-"+tag, "org:c34m-"+tag, "org:c34s-"+tag
	t := s.api.tenancy
	c.Assert(t.PutMembership(ct.Membership{UserID: viewer.ID, Subject: orgAdmin, Role: "admin"}), IsNil)
	c.Assert(t.PutMembership(ct.Membership{UserID: viewer.ID, Subject: orgOwner, Role: "owner"}), IsNil)
	c.Assert(t.PutMembership(ct.Membership{UserID: viewer.ID, Subject: orgMember, Role: "member"}), IsNil)
	c.Assert(t.PutMembership(ct.Membership{UserID: viewer.ID, Subject: orgSusp, Role: "admin"}), IsNil)
	c.Assert(t.SetAccountSuspended(orgSusp, true), IsNil)
	c.Assert(t.UpsertAccountCollaborator("user:"+sharer.ID, viewer.ID, "admin"), IsNil)
	c.Assert(t.UpsertAccountCollaborator("user:"+manager.ID, viewer.ID, "manage"), IsNil)

	add := func(owner string) *ct.Resource {
		r := &ct.Resource{ProviderID: p.ID, ExternalID: "/things/" + random.UUID(), Env: map[string]string{"S": random.UUID()}, OwnerAccount: owner}
		c.Assert(s.api.resourceRepo.Add(r), IsNil)
		return r
	}
	want := []string{
		add("user:" + viewer.ID).ID,
		add(orgAdmin).ID,
		add(orgOwner).ID,
		add("user:" + sharer.ID).ID,
	}
	sort.Strings(want)
	for _, owner := range []string{orgMember, orgSusp, "user:" + manager.ID, "user:" + stranger.ID, ""} {
		add(owner)
	}

	cs := s.newCountingServer(c, 5)
	defer cs.close()
	tok := s.signUser(c, viewer.ID, nil)
	ctx := s.userCtx(c, viewer.ID)
	paths := []string{"/resources", "/providers/" + p.ID + "/resources"}

	measure := func() []int {
		var counts []int
		for _, path := range paths {
			code, body, n := cs.do(c, "GET", path, tok, "")
			c.Assert(code, Equals, 200, Commentf("%s body=%s", path, body))
			got := resourceIDSet(c, body)
			var all []*ct.Resource
			var err error
			if strings.HasPrefix(path, "/providers/") {
				all, err = s.api.resourceRepo.ProviderList(p.ID)
				c.Assert(got, DeepEquals, want, Commentf(path))
			} else {
				all, err = s.api.resourceRepo.List()
				for _, id := range want {
					c.Assert(contains(got, id), Equals, true, Commentf("%s missing %s", path, id))
				}
			}
			c.Assert(err, IsNil)
			c.Assert(got, DeepEquals, s.perRowVisibleResources(ctx, all), Commentf("%s differs from the per-row filter", path))
			counts = append(counts, n)
		}
		return counts
	}

	before := measure()
	// Twenty more resources, each in its own foreign account.
	for i := 0; i < 20; i++ {
		add(fmt.Sprintf("user:%s", random.UUID()))
	}
	after := measure()
	c.Logf("GET /resources, /providers/:id/resources statements: %v -> %v after 20 more resources", before, after)
	c.Assert(after, DeepEquals, before, Commentf("statements per request grew with the resource count: %v -> %v", before, after))
	for i, n := range after {
		c.Assert(n <= 12, Equals, true, Commentf("%s ran %d statements", paths[i], n))
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// perRowVisibleRoutes is the pre-CORE-34 route filter: one appRepo.Get per
// route.
func (s *S) perRowVisibleRoutes(ctx context.Context, routes []*router.Route) []string {
	tok := authz.TokenFromContext(ctx)
	acme := authz.HasACMEIssue(tok)
	ids := []string{}
	for _, route := range routes {
		appID := strings.TrimPrefix(route.ParentRef, ct.RouteParentRefPrefix)
		if appID == "" || appID == route.ParentRef {
			continue
		}
		raw, err := s.api.appRepo.Get(appID)
		if err != nil {
			continue
		}
		app := raw.(*ct.App)
		if (acme && !catalogInternalApp(app)) || appVisible(tok, app) {
			ids = append(ids, route.Type+"/"+route.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func sortedRouteIDs(routes []*router.Route) []string {
	ids := routeIDs(routes)
	sort.Strings(ids)
	return ids
}

// Unpaged GET /routes for a tenant ran one app lookup per route in the
// cluster. The count must not depend on the route count, and the rows must
// match the per-row filter: own apps, apps shared by app collaboration or an
// org, no other tenant's apps and no catalog-internal app.
func (s *S) TestFilterRoutesQueriesIndependentOfRouteCount(c *C) {
	tag := random.String(6)
	viewer := s.createLoginUser(c, "c34-rt-v-"+tag+"@flynn.test", "c34-rt-v-"+tag, "pw-c34-"+tag, false)
	other := s.createLoginUser(c, "c34-rt-o-"+tag+"@flynn.test", "c34-rt-o-"+tag, "pw-c34-"+tag, false)
	org := "org:c34rt-" + tag
	c.Assert(s.api.tenancy.PutMembership(ct.Membership{UserID: viewer.ID, Subject: org, Role: "member"}), IsNil)

	own := s.createTestApp(c, &ct.App{Name: "c34-rt-own-" + tag, OwnerAccount: "user:" + viewer.ID})
	shared := s.createTestApp(c, &ct.App{Name: "c34-rt-shared-" + tag, OwnerAccount: "user:" + other.ID})
	c.Assert(s.api.tenancy.UpsertAppCollaborator(shared.ID, viewer.ID, "view"), IsNil)
	orgApp := s.createTestApp(c, &ct.App{Name: "c34-rt-org-" + tag, OwnerAccount: org})
	foreign := s.createTestApp(c, &ct.App{Name: "c34-rt-foreign-" + tag, OwnerAccount: "user:" + other.ID})
	system := s.createTestApp(c, &ct.App{Name: "c34-rt-sys-" + tag, OwnerAccount: "user:" + viewer.ID, Meta: map[string]string{"flynn-system-app": "true"}})

	route := func(app *ct.App) *router.Route {
		return s.createTestRoute(c, app.ID, (&router.HTTPRoute{Domain: random.String(10) + ".c34.example.com", Service: "web"}).ToRoute())
	}
	var mustSee, mustHide []string
	for _, app := range []*ct.App{own, shared, orgApp} {
		r := route(app)
		mustSee = append(mustSee, r.Type+"/"+r.ID)
	}
	for _, app := range []*ct.App{foreign, system} {
		r := route(app)
		mustHide = append(mustHide, r.Type+"/"+r.ID)
	}

	cs := s.newCountingServer(c, 5)
	defer cs.close()
	tok := s.signUser(c, viewer.ID, nil)
	ctx := s.userCtx(c, viewer.ID)
	acmeCtx := context.WithValue(context.Background(), authz.TokenContextKey, &authorizer.Token{Scopes: []string{authz.ScopeACMEIssue}})

	measure := func() int {
		code, body, n := cs.do(c, "GET", "/routes", tok, "")
		c.Assert(code, Equals, 200, Commentf("body=%s", body))
		var got []*router.Route
		c.Assert(json.Unmarshal([]byte(body), &got), IsNil)
		ids := sortedRouteIDs(got)
		for _, id := range mustSee {
			c.Assert(contains(ids, id), Equals, true, Commentf("missing %s", id))
		}
		for _, id := range mustHide {
			c.Assert(contains(ids, id), Equals, false, Commentf("leaked %s", id))
		}
		all, err := s.api.routeRepo.List("")
		c.Assert(err, IsNil)
		c.Assert(ids, DeepEquals, s.perRowVisibleRoutes(ctx, all))
		// The acme:issue service token sees every non-internal app's routes.
		acmeRoutes, err := cs.api.filterRoutes(acmeCtx, all)
		c.Assert(err, IsNil)
		acme := sortedRouteIDs(acmeRoutes)
		c.Assert(acme, DeepEquals, s.perRowVisibleRoutes(acmeCtx, all))
		c.Assert(contains(acme, mustHide[0]), Equals, true)
		c.Assert(contains(acme, mustHide[1]), Equals, false)
		return n
	}

	before := measure()
	for i := 0; i < 20; i++ {
		route(s.createTestApp(c, &ct.App{Name: fmt.Sprintf("c34-rt-f%d-%s", i, tag), OwnerAccount: "user:" + random.UUID()}))
	}
	after := measure()
	c.Logf("GET /routes statements: %d -> %d after 20 more routes", before, after)
	c.Assert(after, Equals, before, Commentf("statements per request grew with the route count: %d -> %d", before, after))
	c.Assert(after <= 10, Equals, true, Commentf("GET /routes ran %d statements", after))
}

// GetMany resolves refs as Get does: id first (dashed or not), then name;
// deleted and unknown apps are left out.
func (s *S) TestAppRepoGetManyMatchesGet(c *C) {
	tag := random.String(6)
	a := s.createTestApp(c, &ct.App{Name: "c34-many-a-" + tag})
	b := s.createTestApp(c, &ct.App{Name: "c34-many-b-" + tag})
	gone := s.createTestApp(c, &ct.App{Name: "c34-many-gone-" + tag})
	c.Assert(s.hc.db.Exec(`UPDATE apps SET deleted_at = now() WHERE app_id = $1`, gone.ID), IsNil)
	refs := []string{a.ID, strings.ReplaceAll(b.ID, "-", ""), b.Name, gone.ID, gone.Name, random.UUID(), "c34-none-" + tag}
	got, err := s.api.appRepo.GetMany(refs)
	c.Assert(err, IsNil)
	for _, ref := range refs {
		want, err := s.api.appRepo.Get(ref)
		app, ok := got[ref]
		if err != nil {
			c.Assert(ok, Equals, false, Commentf("ref %s", ref))
			continue
		}
		c.Assert(ok, Equals, true, Commentf("ref %s", ref))
		c.Assert(app.ID, Equals, want.(*ct.App).ID, Commentf("ref %s", ref))
	}
	c.Assert(got, HasLen, 3)
}

// addonForProvider scanned every app in the cluster, twice per provision
// (pass-app and instance-port checks). It reads plugin apps only now, once.
func (s *S) TestProvisionReadsAddonSpecOnceFromPluginApps(c *C) {
	tag := random.String(6)
	owner := random.UUID()
	app := s.createTestApp(c, &ct.App{Name: "c34-prov-" + tag, OwnerAccount: "user:" + owner})
	for i := 0; i < 10; i++ {
		s.createTestApp(c, &ct.App{Name: fmt.Sprintf("c34-prov-f%d-%s", i, tag), OwnerAccount: "user:" + random.UUID()})
	}
	p, calls, done := s.countingProvider(c, "c34-prov-"+tag)
	defer done()

	var plugins int
	c.Assert(s.hc.db.QueryRow(`SELECT COUNT(*) FROM apps WHERE deleted_at IS NULL AND COALESCE(meta->>'flynn-plugin', '') = 'true'`).Scan(&plugins), IsNil)

	cs := s.newCountingServer(c, 5)
	defer cs.close()
	code, body, _ := cs.do(c, "POST", "/providers/"+p.ID+"/resources", s.signUser(c, owner, nil), `{"apps":["`+app.ID+`"]}`)
	c.Assert(code, Equals, 200, Commentf(body))
	c.Assert(*calls, Equals, 1)

	full := cs.counter.rowsFor("app_list")
	c.Assert(full, HasLen, 0, Commentf("provision listed every app %d time(s)", len(full)))
	scans := cs.counter.rowsFor("app_list_plugins")
	c.Assert(scans, HasLen, 1)
	c.Assert(scans[0], Equals, plugins)
}

// Suspending an account listed every app in the cluster to find the
// account's own. It reads only the account's apps.
func (s *S) TestScaleAccountToZeroListsOnlyTheAccountsApps(c *C) {
	tag := random.String(6)
	account := "user:" + random.UUID()
	mine := s.createTestApp(c, &ct.App{Name: "c34-susp-" + tag, OwnerAccount: account})
	for i := 0; i < 5; i++ {
		s.createTestApp(c, &ct.App{Name: fmt.Sprintf("c34-susp-f%d-%s", i, tag), OwnerAccount: "user:" + random.UUID()})
	}
	release := s.createTestRelease(c, mine.ID, &ct.Release{Processes: map[string]ct.ProcessType{"web": {}}})
	s.createTestFormation(c, &ct.Formation{AppID: mine.ID, ReleaseID: release.ID, Processes: map[string]int{"web": 2}})

	cs := s.newCountingServer(c, 5)
	defer cs.close()
	cs.counter.reset()
	cs.api.scaleAccountToZero(account)
	c.Assert(cs.counter.rowsFor("app_list"), HasLen, 0)
	c.Assert(cs.counter.rowsFor("app_list_by_owner"), DeepEquals, []int{1})

	f, err := s.c.GetFormation(mine.ID, release.ID)
	c.Assert(err, IsNil)
	c.Assert(f.Processes["web"], Equals, 0)
}

// A tenant list call keeps one pool connection at a time: with every other
// connection taken by a slow caller it still finishes, and the other caller
// is never blocked behind more than one statement of it.
func (s *S) TestTenantListsFinishOnOneFreeConnection(c *C) {
	tag := random.String(6)
	viewer := s.createLoginUser(c, "c34-pool-"+tag+"@flynn.test", "c34-pool-"+tag, "pw-c34-"+tag, false)
	app := s.createTestApp(c, &ct.App{Name: "c34-pool-" + tag, OwnerAccount: "user:" + viewer.ID})
	s.createTestRoute(c, app.ID, (&router.HTTPRoute{Domain: random.String(10) + ".c34.example.com", Service: "web"}).ToRoute())

	cs := s.newCountingServer(c, 2)
	defer cs.close()
	tok := s.signUser(c, viewer.ID, nil)
	// Warm both connections, then hold one.
	cs.do(c, "GET", "/resources", tok, "")
	held, err := cs.pool.Acquire()
	c.Assert(err, IsNil)
	defer cs.pool.Release(held)
	for _, path := range []string{"/resources", "/routes", "/apps"} {
		code, body, _ := cs.do(c, "GET", path, tok, "")
		c.Assert(code, Equals, 200, Commentf("%s body=%s", path, body))
	}
}
