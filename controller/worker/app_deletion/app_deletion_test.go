package app_deletion

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flynn/que-go"
	"github.com/inconshreveable/log15"
	"github.com/jackc/pgx"
	controller "github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/random"
	pgtestutils "github.com/randy-girard/flynn/pkg/testutils/postgres"
	router "github.com/randy-girard/flynn/router/types"
)

// The destructive phases, in the order the worker must run them. A failure
// in one phase must leave every later phase untouched.
const (
	phaseResources = iota
	phaseCache
	phaseRoutes
	phaseReleases
)

var stepPhase = map[string]int{
	"list_resources":  phaseResources,
	"delete_resource": phaseResources,
	"cache":           phaseCache,
	"list_routes":     phaseRoutes,
	"delete_route":    phaseRoutes,
	"list_releases":   phaseReleases,
	"delete_release":  phaseReleases,
}

var errInjected = errors.New("injected failure")

// fakeClient is the slice of the controller API the worker calls. State
// changes as the worker deletes things, so a retry sees what is left.
type fakeClient struct {
	controller.Client

	mu        sync.Mutex
	app       *ct.App
	gone      bool
	fail      map[string]bool
	routes    []*router.Route
	releases  []*ct.Release
	resources []*ct.Resource
	// calls records destructive calls as "<phase>:<id>".
	calls []string
}

func (f *fakeClient) failing(step string) error {
	if f.fail[step] {
		return fmt.Errorf("%s: %w", step, errInjected)
	}
	return nil
}

func (f *fakeClient) record(phase int, id string) {
	f.calls = append(f.calls, fmt.Sprintf("%d:%s", phase, id))
}

func (f *fakeClient) GetApp(id string) (*ct.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gone || (id != f.app.ID && id != f.app.Name) {
		return nil, controller.ErrNotFound
	}
	a := *f.app
	return &a, nil
}

func (f *fakeClient) AppRouteList(appID string) ([]*router.Route, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failing("list_routes"); err != nil {
		return nil, err
	}
	return append([]*router.Route(nil), f.routes...), nil
}

func (f *fakeClient) DeleteRouteForAppDeletion(appID, routeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failing("delete_route"); err != nil {
		return err
	}
	for i, r := range f.routes {
		if r.FormattedID() == routeID {
			f.routes = append(f.routes[:i], f.routes[i+1:]...)
			f.record(phaseRoutes, routeID)
			return nil
		}
	}
	return controller.ErrNotFound
}

func (f *fakeClient) AppReleaseList(appID string) ([]*ct.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failing("list_releases"); err != nil {
		return nil, err
	}
	return append([]*ct.Release(nil), f.releases...), nil
}

func (f *fakeClient) DeleteRelease(appID, releaseID string) (*ct.ReleaseDeletion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failing("delete_release"); err != nil {
		return nil, err
	}
	for i, r := range f.releases {
		if r.ID == releaseID {
			f.releases = append(f.releases[:i], f.releases[i+1:]...)
			f.record(phaseReleases, releaseID)
			return &ct.ReleaseDeletion{AppID: appID, ReleaseID: releaseID}, nil
		}
	}
	return nil, controller.ErrNotFound
}

func (f *fakeClient) AppResourceList(appID string) ([]*ct.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failing("list_resources"); err != nil {
		return nil, err
	}
	return append([]*ct.Resource(nil), f.resources...), nil
}

func (f *fakeClient) DeleteResource(providerID, resourceID string) (*ct.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failing("delete_resource"); err != nil {
		return nil, err
	}
	for i, r := range f.resources {
		if r.ID == resourceID {
			f.resources = append(f.resources[:i], f.resources[i+1:]...)
			f.record(phaseResources, resourceID)
			return r, nil
		}
	}
	return nil, controller.ErrNotFound
}

func (f *fakeClient) destructive() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// fakeBlobstore answers the app cache DELETEs.
type fakeBlobstore struct {
	mu    sync.Mutex
	fail  bool
	stall time.Duration
	f     *fakeClient
}

func (b *fakeBlobstore) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b.mu.Lock()
	fail, stall := b.fail, b.stall
	b.mu.Unlock()
	if stall > 0 {
		select {
		case <-time.After(stall):
		case <-req.Context().Done():
			return
		}
	}
	if req.Method != "DELETE" {
		w.WriteHeader(405)
		return
	}
	if fail {
		w.WriteHeader(500)
		return
	}
	b.f.mu.Lock()
	b.f.record(phaseCache, req.URL.Path)
	b.f.mu.Unlock()
	w.WriteHeader(200)
}

type fixture struct {
	t       *testing.T
	db      *postgres.DB
	apps    *data.AppRepo
	app     *ct.App
	release *ct.Release
	client  *fakeClient
	blob    *fakeBlobstore
	ctx     *context
}

var setupOnce sync.Once
var sharedDB *postgres.DB
var setupErr error

func testDB(t *testing.T) *postgres.DB {
	t.Helper()
	setupOnce.Do(func() {
		dbname := "controller_app_deletion_test"
		if setupErr = pgtestutils.SetupPostgres(dbname); setupErr != nil {
			return
		}
		cfg, err := pgtestutils.ConnConfigForDatabase(dbname)
		if err != nil {
			setupErr = err
			return
		}
		migratePool, err := pgx.NewConnPool(pgx.ConnPoolConfig{ConnConfig: cfg})
		if err != nil {
			setupErr = err
			return
		}
		if setupErr = data.MigrateDB(postgres.New(migratePool, nil)); setupErr != nil {
			return
		}
		migratePool.Close()
		pool, err := pgx.NewConnPool(pgx.ConnPoolConfig{
			ConnConfig:   cfg,
			AfterConnect: data.PrepareStatements,
		})
		if err != nil {
			setupErr = err
			return
		}
		sharedDB = postgres.New(pool, nil)
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	return sharedDB
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := testDB(t)
	apps := data.NewAppRepo(db, "example.com", data.NewRouteRepo(db))
	app := &ct.App{Name: "del-" + strings.ToLower(random.String(8))}
	if err := apps.Add(app); err != nil {
		t.Fatal(err)
	}
	releases := data.NewReleaseRepo(db, data.NewArtifactRepo(db), que.NewClient(db.ConnPool))
	release := &ct.Release{AppID: app.ID}
	if err := releases.Add(release); err != nil {
		t.Fatal(err)
	}
	if err := apps.SetRelease(app, release.ID); err != nil {
		t.Fatal(err)
	}
	old := &ct.Release{ID: random.UUID(), AppID: app.ID}

	f := &fakeClient{
		app:  app,
		fail: map[string]bool{},
		routes: []*router.Route{
			{ID: "r1", Type: "http", Domain: app.Name + ".example.com"},
		},
		releases: []*ct.Release{release, old},
		resources: []*ct.Resource{
			{ID: "res-own", ProviderID: "p1", Apps: []string{app.ID}},
			// attached to another app too: never deprovisioned here
			{ID: "res-shared", ProviderID: "p1", Apps: []string{app.ID, random.UUID()}},
		},
	}
	blob := &fakeBlobstore{f: f}
	srv := httptest.NewServer(blob)
	t.Cleanup(srv.Close)

	c := newContext(db, f, log15.New())
	c.blobstoreURL = srv.URL
	return &fixture{t: t, db: db, apps: apps, app: app, release: release, client: f, blob: blob, ctx: c}
}

func (fx *fixture) run() error {
	fx.t.Helper()
	args, err := json.Marshal(&ct.App{ID: fx.app.ID, Name: fx.app.Name})
	if err != nil {
		fx.t.Fatal(err)
	}
	return fx.ctx.HandleAppDeletion(&que.Job{ID: 1, Args: args})
}

// live is the app row as the controller sees it, nil once deleted.
func (fx *fixture) live() *ct.App {
	fx.t.Helper()
	got, err := fx.apps.Get(fx.app.ID)
	if err == data.ErrNotFound {
		return nil
	}
	if err != nil {
		fx.t.Fatal(err)
	}
	return got.(*ct.App)
}

func (fx *fixture) events() []ct.AppDeletionEvent {
	fx.t.Helper()
	rows, err := fx.db.Query("SELECT data FROM events WHERE app_id = $1 AND object_type = 'app_deletion' ORDER BY event_id", fx.app.ID)
	if err != nil {
		fx.t.Fatal(err)
	}
	defer rows.Close()
	var out []ct.AppDeletionEvent
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			fx.t.Fatal(err)
		}
		var e ct.AppDeletionEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			fx.t.Fatal(err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		fx.t.Fatal(err)
	}
	return out
}

func phasesAfter(calls []string, phase int) []string {
	var out []string
	for _, c := range calls {
		var p int
		fmt.Sscanf(c, "%d:", &p)
		if p > phase {
			out = append(out, c)
		}
	}
	return out
}

// assertFailedVisibly checks the state a failed attempt must leave: the app
// row still there, an app_deletion event carrying the error, and the error
// on the app for apps:info.
func (fx *fixture) assertFailedVisibly(err error, want string) {
	fx.t.Helper()
	if err == nil {
		fx.t.Fatal("expected the deletion to fail")
	}
	app := fx.live()
	if app == nil {
		fx.t.Fatal("app row deleted although a step failed")
	}
	evs := fx.events()
	if len(evs) == 0 || !strings.Contains(evs[len(evs)-1].Error, want) {
		fx.t.Fatalf("expected an app_deletion event with error %q, got %+v", want, evs)
	}
	if msg := app.Meta[ct.AppMetaDeletionError]; !strings.Contains(msg, want) {
		fx.t.Fatalf("expected app meta %s to carry %q, got %q", ct.AppMetaDeletionError, want, msg)
	}
}

// assertCompleted checks a finished deletion: every own resource, route,
// release and cache blob deleted exactly once, shared resources left alone.
func (fx *fixture) assertCompleted() {
	fx.t.Helper()
	if fx.live() != nil {
		fx.t.Fatal("app row still live after a successful deletion")
	}
	counts := map[string]int{}
	for _, c := range fx.client.destructive() {
		counts[c]++
	}
	for _, want := range []string{
		fmt.Sprintf("%d:res-own", phaseResources),
		fmt.Sprintf("%d:/repos/%s.tar", phaseCache, fx.app.ID),
		fmt.Sprintf("%d:/%s-cache.tgz", phaseCache, fx.app.ID),
		fmt.Sprintf("%d:http/r1", phaseRoutes),
		fmt.Sprintf("%d:%s", phaseReleases, fx.release.ID),
	} {
		if counts[want] == 0 {
			fx.t.Fatalf("expected destructive call %s, got %v", want, fx.client.destructive())
		}
	}
	for c, n := range counts {
		if strings.HasPrefix(c, fmt.Sprintf("%d:", phaseResources)) && n > 1 {
			fx.t.Fatalf("resource deleted %d times: %s", n, c)
		}
		if strings.HasSuffix(c, "res-shared") {
			fx.t.Fatal("deprovisioned a resource another app still uses")
		}
	}
	evs := fx.events()
	if len(evs) == 0 || evs[len(evs)-1].Error != "" {
		fx.t.Fatalf("expected a final app_deletion event without error, got %+v", evs)
	}
	// Each attempt's event lists what that attempt deleted; together they
	// list everything once.
	var resources, routes, releases int
	for _, e := range evs {
		if e.AppDeletion == nil {
			continue
		}
		resources += len(e.AppDeletion.DeletedResources)
		routes += len(e.AppDeletion.DeletedRoutes)
		releases += len(e.AppDeletion.DeletedReleases)
	}
	if resources != 1 || routes != 1 || releases != 2 {
		fx.t.Fatalf("events list %d resources, %d routes, %d releases deleted; want 1, 1, 2", resources, routes, releases)
	}
}

// TestResourceFailureLeavesReleasesIntact is the CORE-33 case: a provider that
// refuses or is down must not cost the app its routes, releases or slugs.
func TestResourceFailureLeavesReleasesIntact(t *testing.T) {
	fx := newFixture(t)
	fx.client.fail["delete_resource"] = true

	err := fx.run()
	if calls := fx.client.destructive(); len(calls) != 0 {
		t.Fatalf("expected nothing deleted before the resource step succeeds, got %v", calls)
	}
	fx.assertFailedVisibly(err, "delete_resource")
	if app := fx.live(); app.ReleaseID != fx.release.ID {
		t.Fatalf("current release was unset (%q) although deletion failed", app.ReleaseID)
	}

	// The provider comes back: the retry finishes the job.
	delete(fx.client.fail, "delete_resource")
	if err := fx.run(); err != nil {
		t.Fatal(err)
	}
	fx.assertCompleted()
}

// TestFailingEachStepLeavesLaterStepsUntouched fails every fallible step in
// turn, checks that nothing in a later phase ran, then lets the retry finish.
func TestFailingEachStepLeavesLaterStepsUntouched(t *testing.T) {
	for _, step := range []string{
		"list_resources", "delete_resource", "cache",
		"list_routes", "delete_route", "list_releases", "delete_release",
	} {
		t.Run(step, func(t *testing.T) {
			fx := newFixture(t)
			if step == "cache" {
				fx.blob.fail = true
			} else {
				fx.client.fail[step] = true
			}

			err := fx.run()
			want := step
			if step == "cache" {
				want = "unexpected status code 500"
			}
			if later := phasesAfter(fx.client.destructive(), stepPhase[step]); len(later) != 0 {
				t.Fatalf("step %s failed but later phases ran: %v", step, later)
			}
			fx.assertFailedVisibly(err, want)
			if stepPhase[step] < phaseReleases {
				if app := fx.live(); app.ReleaseID != fx.release.ID {
					t.Fatalf("current release unset although %s failed", step)
				}
			}

			fx.blob.fail = false
			delete(fx.client.fail, step)
			if err := fx.run(); err != nil {
				t.Fatal(err)
			}
			fx.assertCompleted()
		})
	}
}

// TestResourceAlreadyGoneCountsAsDeleted covers a retry after the provider
// deprovisioned a resource whose row was removed by another path.
func TestResourceAlreadyGoneCountsAsDeleted(t *testing.T) {
	fx := newFixture(t)
	gone := &ct.Resource{ID: "res-gone", ProviderID: "p1", Apps: []string{fx.app.ID}}
	fx.client.resources = append(fx.client.resources, gone)
	orig := fx.client
	fx.ctx.client = &goneResourceClient{fakeClient: orig, gone: gone.ID}

	if err := fx.run(); err != nil {
		t.Fatal(err)
	}
	if fx.live() != nil {
		t.Fatal("app row still live")
	}
}

type goneResourceClient struct {
	*fakeClient
	gone string
}

func (g *goneResourceClient) DeleteResource(providerID, resourceID string) (*ct.Resource, error) {
	if resourceID == g.gone {
		return nil, controller.ErrNotFound
	}
	return g.fakeClient.DeleteResource(providerID, resourceID)
}

// TestStalledBlobstoreTimesOut: a blobstore that never answers must fail the
// attempt within the client timeout and leave releases in place.
func TestStalledBlobstoreTimesOut(t *testing.T) {
	fx := newFixture(t)
	fx.blob.stall = 10 * time.Second
	fx.ctx.http = &http.Client{Timeout: 200 * time.Millisecond}

	done := make(chan error, 1)
	go func() { done <- fx.run() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deletion still blocked on a stalled blobstore")
	}
	if later := phasesAfter(fx.client.destructive(), phaseCache); len(later) != 0 {
		t.Fatalf("routes or releases deleted after a blobstore timeout: %v", later)
	}
}

func TestDefaultBlobstoreClientHasTimeout(t *testing.T) {
	c := newContext(nil, nil, log15.New())
	if c.http == nil || c.http.Timeout <= 0 {
		t.Fatal("app deletion blobstore client has no timeout")
	}
}

// TestAlreadyDeletedAppIsDone: a second job for an app that is already gone
// (two DELETE requests) finishes without touching anything.
func TestAlreadyDeletedAppIsDone(t *testing.T) {
	fx := newFixture(t)
	fx.client.gone = true
	if err := fx.run(); err != nil {
		t.Fatalf("expected an already deleted app to finish the job, got %v", err)
	}
	if calls := fx.client.destructive(); len(calls) != 0 {
		t.Fatalf("expected no calls, got %v", calls)
	}
}
