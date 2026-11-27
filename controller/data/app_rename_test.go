package data

import (
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/random"
	router "github.com/randy-girard/flynn/router/types"
)

// Since CORE-17 a user app may only declare discoverd services named "<app>"
// or "<app>-*", and flynn-host checks the same rule when it starts a job.
// Renaming an app whose current release (or a release that still has
// processes scaled up) declares "<old>" / "<old>-*" services would make the
// next push, deploy or job restart fail, so that rename is refused with a
// validation error (NEW-59). Otherwise the rename goes ahead and the app's
// own routes to "<old>-*" services move to "<new>-*" in the same
// transaction, so the next push is routed and nobody who later takes the
// old name receives this app's traffic.
type renameFixture struct {
	t        *testing.T
	apps     *AppRepo
	releases *ReleaseRepo
	routes   *RouteRepo
	db       interface {
		Exec(string, ...interface{}) error
	}
}

func newRenameFixture(t *testing.T) *renameFixture {
	db := smallPoolDB(t, "controller_app_rename_test")
	routes := NewRouteRepo(db)
	return &renameFixture{
		t:        t,
		apps:     NewAppRepo(db, "example.com", routes),
		releases: NewReleaseRepo(db, NewArtifactRepo(db), nil),
		routes:   routes,
		db:       db,
	}
}

func (f *renameFixture) app() *ct.App {
	f.t.Helper()
	app := &ct.App{Name: "rn-" + random.String(8)}
	if err := f.apps.Add(app); err != nil {
		f.t.Fatal(err)
	}
	return app
}

func webRelease(appID, service string) *ct.Release {
	procs := map[string]ct.ProcessType{"worker": {Args: []string{"work"}}}
	if service != "" {
		procs["web"] = ct.ProcessType{
			Args:    []string{"web"},
			Service: service,
			Ports: []ct.Port{{Port: 8080, Proto: "tcp", Service: &host.Service{
				Name: service, Create: true,
			}}},
		}
	}
	return &ct.Release{AppID: appID, Processes: procs}
}

func (f *renameFixture) release(app *ct.App, service string, current bool) *ct.Release {
	f.t.Helper()
	rel := webRelease(app.ID, service)
	if err := f.releases.Add(rel); err != nil {
		f.t.Fatal(err)
	}
	if current {
		if err := f.apps.SetRelease(app, rel.ID); err != nil {
			f.t.Fatal(err)
		}
	}
	return rel
}

func (f *renameFixture) renameRefused(app *ct.App, want string) {
	f.t.Helper()
	next := app.Name + "-new"
	_, err := f.apps.Update(app.ID, map[string]interface{}{"name": next})
	verr, ok := err.(ct.ValidationError)
	if !ok {
		f.t.Fatalf("rename = %#v, want a ct.ValidationError", err)
	}
	if verr.Field != "name" || !strings.Contains(verr.Message, want) {
		f.t.Fatalf("rename refused with %q on %q, want field name mentioning %q", verr.Message, verr.Field, want)
	}
	got, err := f.apps.Get(app.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	if name := got.(*ct.App).Name; name != app.Name {
		f.t.Fatalf("name %q after a refused rename, want %q", name, app.Name)
	}
}

func (f *renameFixture) routeService(app *ct.App, id string) string {
	f.t.Helper()
	routes, err := f.routes.List(ct.RouteParentRefPrefix + app.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, r := range routes {
		if r.ID == id {
			return r.Service
		}
	}
	f.t.Fatalf("route %s not found", id)
	return ""
}

func (f *renameFixture) appRoutes(app *ct.App) []*router.Route {
	f.t.Helper()
	routes, err := f.routes.List(ct.RouteParentRefPrefix + app.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return routes
}

func (f *renameFixture) renameOK(app *ct.App) {
	f.t.Helper()
	next := app.Name + "-new"
	if _, err := f.apps.Update(app.ID, map[string]interface{}{"name": next}); err != nil {
		f.t.Fatalf("rename: %s", err)
	}
	got, err := f.apps.Get(app.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	if name := got.(*ct.App).Name; name != next {
		f.t.Fatalf("name %q, want %q", name, next)
	}
}

func TestRenameAppRefusedWhileServicesUseOldName(t *testing.T) {
	f := newRenameFixture(t)

	t.Run("current release", func(t *testing.T) {
		f.t = t
		app := f.app()
		f.release(app, app.Name+"-web", true)
		f.renameRefused(app, app.Name+"-web")
	})

	t.Run("scaled formation of an older release", func(t *testing.T) {
		f.t = t
		app := f.app()
		old := f.release(app, app.Name+"-web", false)
		f.release(app, "", true)
		if err := f.db.Exec(`INSERT INTO formations (app_id, release_id, processes) VALUES ($1, $2, '{"web": 1}')`, app.ID, old.ID); err != nil {
			t.Fatal(err)
		}
		f.renameRefused(app, app.Name+"-web")
	})

	t.Run("bare app name as service", func(t *testing.T) {
		f.t = t
		app := f.app()
		f.release(app, app.Name, true)
		f.renameRefused(app, `"`+app.Name+`"`)
	})
}

func TestRenameAppAllowedWithoutOwnedServices(t *testing.T) {
	f := newRenameFixture(t)

	t.Run("no release, included route follows the name", func(t *testing.T) {
		f.t = t
		app := f.app()
		before := f.appRoutes(app)
		if len(before) != 1 || before[0].Service != app.Name+"-web" {
			t.Fatalf("routes %+v, want the included route to %s-web", before, app.Name)
		}
		old := app.Name
		f.renameOK(app)
		if got := f.routeService(app, before[0].ID); got != old+"-new-web" {
			t.Fatalf("included route service %q after rename, want %q", got, old+"-new-web")
		}
	})

	t.Run("route to a more specific app's service is left alone", func(t *testing.T) {
		f.t = t
		app := f.app()
		admin := &ct.App{Name: app.Name + "-admin"}
		if err := f.apps.Add(admin); err != nil {
			t.Fatal(err)
		}
		cross := &router.Route{
			Type:      "http",
			Domain:    app.Name + "-admin.custom.example.org",
			Path:      "/",
			Service:   admin.Name + "-web",
			ParentRef: ct.RouteParentRefPrefix + app.ID,
			// the included route of the admin app already uses that service
			DrainBackends: true,
		}
		if err := f.routes.Add(cross); err != nil {
			t.Fatal(err)
		}
		f.renameOK(app)
		if got := f.routeService(app, cross.ID); got != admin.Name+"-web" {
			t.Fatalf("cross-app route service %q after rename, want %q", got, admin.Name+"-web")
		}
	})

	t.Run("release without services", func(t *testing.T) {
		f.t = t
		app := f.app()
		f.release(app, "", true)
		f.renameOK(app)
	})

	t.Run("old release scaled to zero", func(t *testing.T) {
		f.t = t
		app := f.app()
		old := f.release(app, app.Name+"-web", false)
		f.release(app, "", true)
		if err := f.db.Exec(`INSERT INTO formations (app_id, release_id, processes) VALUES ($1, $2, '{"web": 0}')`, app.ID, old.ID); err != nil {
			t.Fatal(err)
		}
		f.renameOK(app)
	})
}
