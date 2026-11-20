package plugin

import (
	"errors"
	"testing"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

func userCatalogApps() []*ct.App {
	return []*ct.App{
		{
			Name: "redis-plugin",
			Meta: map[string]string{
				MetaPlugin:       "true",
				MetaPluginKind:   KindResourceProvider,
				MetaPluginRecord: `{"provider":"redis"}`,
				MetaPluginCLI:    `{"command":"redis","usage":"manage redis"}`,
			},
		},
		{
			Name: "unsafe-plugin",
			Meta: map[string]string{
				MetaPlugin:      "true",
				MetaPluginKind:  KindResourceProvider,
				MetaPluginAddon: `{"type":"unsafe","verbs":["create","attach"],"backend":"resource","provider":"unsafe"}`,
				MetaPluginCLI:   `{"command":"unsafe","usage":"shared database"}`,
			},
		},
		{
			// A tenant app is never a catalog entry.
			Name: "tenant-app",
			Meta: map[string]string{MetaPluginCLI: `{"command":"tenant-app"}`},
		},
	}
}

func userCatalogProviders() []*ct.Provider {
	return []*ct.Provider{
		{Name: "redis", TenantSafe: true},
		{Name: "unsafe"},
		{Name: "platform-postgres"},
	}
}

// UserCatalog with no provider filter is the same catalog LoadCatalog built
// from GET /apps?all=true for a cluster admin.
func TestUserCatalogNoFilter(t *testing.T) {
	cat := UserCatalog(userCatalogApps(), userCatalogProviders(), nil)
	for _, cmd := range []string{"redis", "unsafe", "platform-postgres"} {
		if !cat.HasCommand(cmd) {
			t.Fatalf("missing %s: %+v", cmd, cat.Commands)
		}
	}
	if cat.HasCommand("tenant-app") {
		t.Fatalf("non-plugin app in catalog: %+v", cat.Commands)
	}
	if cat.AddonByType("redis") == nil || cat.AddonByType("unsafe") == nil {
		t.Fatalf("addons: %+v", cat.Addons)
	}
	if got := cat.AddonByType("redis").App; got != "redis-plugin" {
		t.Fatalf("redis addon app = %q", got)
	}
}

// A provider the caller may not provision drops out with its add-on type
// and the plugin's commands, so a hosted tenant does not see a type that
// addons:create would refuse.
func TestUserCatalogHidesRefusedProviders(t *testing.T) {
	allow := func(p *ct.Provider) bool { return p.TenantSafe }
	cat := UserCatalog(userCatalogApps(), userCatalogProviders(), allow)
	if !cat.HasCommand("redis") || cat.AddonByType("redis") == nil {
		t.Fatalf("tenant_safe redis must stay: %+v", cat)
	}
	if cat.HasCommand("unsafe") || cat.AddonByType("unsafe") != nil || cat.AddonByProvider("unsafe") != nil {
		t.Fatalf("refused provider must be hidden: %+v", cat)
	}
	if cat.HasCommand("platform-postgres") {
		t.Fatalf("refused provider-only command must be hidden: %+v", cat.Commands)
	}
}

type routeCatalogClient struct {
	stubCatalogClient
	cat      *Catalog
	err      error
	appCalls *int
}

func (r routeCatalogClient) AppList() ([]*ct.App, error) {
	if r.appCalls != nil {
		*r.appCalls++
	}
	return r.stubCatalogClient.AppList()
}

func (r routeCatalogClient) GetPluginCatalog(out interface{}) error {
	if r.err != nil {
		return r.err
	}
	*out.(*Catalog) = *r.cat
	return nil
}

// LoadCatalog asks the controller for the caller's catalog. GET /apps only
// lists a tenant's own apps, never plugin apps, so a tenant saw no add-on
// types and no plugin commands.
func TestLoadCatalogUsesControllerCatalog(t *testing.T) {
	calls := 0
	want := &Catalog{
		Commands: []CLI{{Command: "redis", App: "redis-plugin"}},
		Addons:   []AddonSpec{{Type: "redis", Backend: BackendResource, Provider: "redis"}},
	}
	cat, err := LoadCatalog(routeCatalogClient{cat: want, appCalls: &calls})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("GET /apps called %d times", calls)
	}
	if !cat.HasCommand("redis") || cat.AddonByType("redis") == nil {
		t.Fatalf("catalog: %+v", cat)
	}
}

// A controller that predates GET /plugin-catalog answers 404 (cluster key)
// or 403 (user token, unknown routes are cluster-only). LoadCatalog then
// builds the catalog from the app list as before.
func TestLoadCatalogFallsBackOnOldController(t *testing.T) {
	apps := userCatalogApps()
	for name, routeErr := range map[string]error{
		"not found":      controller.ErrNotFound,
		"json not found": httphelper.JSONError{Code: httphelper.NotFoundErrorCode, Message: "not found"},
		"forbidden":      httphelper.JSONError{Code: httphelper.ForbiddenErrorCode, Message: "forbidden"},
	} {
		calls := 0
		cat, err := LoadCatalog(routeCatalogClient{
			stubCatalogClient: stubCatalogClient{apps: apps},
			err:               routeErr,
			appCalls:          &calls,
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if calls != 1 || !cat.HasCommand("redis") {
			t.Fatalf("%s: fallback calls=%d catalog=%+v", name, calls, cat)
		}
	}

	// Any other failure is reported, not hidden behind an empty catalog.
	if _, err := LoadCatalog(routeCatalogClient{err: errors.New("offline")}); err == nil {
		t.Fatal("route error must fail")
	}
}
