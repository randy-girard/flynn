package appmanifest

import (
	"errors"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/dbruntime"
	"github.com/randy-girard/flynn/pkg/plugin"
	router "github.com/randy-girard/flynn/router/types"
)

func TestContainerSetupProvisionsOnce(t *testing.T) {
	backend := newSetupBackend()
	ap := &Applier{
		b:        backend,
		Catalog:  setupCatalog(),
		Runtimes: dbruntime.BuiltinCatalog(),
		Log:      ioDiscard(),
	}
	env, err := ap.ApplyContainerSetup("app", []ContainerAddon{{Type: "postgres", As: "DATABASE"}}, map[string]string{"S3_BUCKET": "bucket"})
	if err != nil {
		t.Fatal(err)
	}
	if backend.provisions != 1 {
		t.Fatalf("provisions %d", backend.provisions)
	}
	if !strings.Contains(backend.lastConfig, `"runtime":"small"`) {
		t.Fatalf("config %s", backend.lastConfig)
	}
	if env["S3_BUCKET"] != "bucket" || env["DATABASE_URL"] != "postgres://db" {
		t.Fatalf("env %#v", env)
	}
	if backend.apps["app"].Meta[MetaStatus] != StatusApplied {
		t.Fatalf("meta %#v", backend.apps["app"].Meta)
	}
	if _, err := ap.ApplyContainerSetup("app", []ContainerAddon{{Type: "postgres", As: "DATABASE"}}, map[string]string{"S3_BUCKET": "bucket"}); err != nil {
		t.Fatal(err)
	}
	if backend.provisions != 1 {
		t.Fatalf("second push provisions %d", backend.provisions)
	}
}

func TestContainerSetupResumesPendingAddon(t *testing.T) {
	backend := newSetupBackend()
	backend.apps["app"].Meta = map[string]string{
		MetaStatus: StatusPending,
		MetaAddons: `[{"type":"postgres","as":"DATABASE","id":"pg-1"}]`,
	}
	backend.head = &ct.Release{ID: "rel", Env: map[string]string{"DATABASE_URL": "postgres://db"}}
	ap := &Applier{b: backend, Catalog: setupCatalog(), Runtimes: dbruntime.BuiltinCatalog(), Log: ioDiscard()}
	if _, err := ap.ApplyContainerSetup("app", []ContainerAddon{{Type: "postgres", As: "DATABASE"}}, nil); err != nil {
		t.Fatal(err)
	}
	if backend.provisions != 0 {
		t.Fatalf("provisions %d", backend.provisions)
	}
	if backend.apps["app"].Meta[MetaStatus] != StatusApplied {
		t.Fatalf("meta %#v", backend.apps["app"].Meta)
	}
}

func TestContainerSetupAppliesAfterStackRelease(t *testing.T) {
	backend := newSetupBackend()
	backend.head = &ct.Release{ID: "stack", Env: map[string]string{"FLYNN_STACK": "container"}}
	ap := &Applier{b: backend, Catalog: setupCatalog(), Runtimes: dbruntime.BuiltinCatalog(), Log: ioDiscard()}
	env, err := ap.ApplyContainerSetup("app", []ContainerAddon{{Type: "postgres", As: "DATABASE"}}, map[string]string{"S3_BUCKET": "bucket"})
	if err != nil {
		t.Fatal(err)
	}
	if backend.provisions != 1 {
		t.Fatalf("stack release provisions %d", backend.provisions)
	}
	if env["DATABASE_URL"] != "postgres://db" || env["S3_BUCKET"] != "bucket" {
		t.Fatalf("env %#v", env)
	}
	coded := newSetupBackend()
	coded.head = &ct.Release{
		ID:        "code",
		Env:       map[string]string{"FLYNN_STACK": "container"},
		Processes: map[string]ct.ProcessType{"web": {}},
	}
	ap = &Applier{b: coded, Catalog: setupCatalog(), Runtimes: dbruntime.BuiltinCatalog(), Log: ioDiscard()}
	if _, err := ap.ApplyContainerSetup("app", []ContainerAddon{{Type: "postgres", As: "DATABASE"}}, nil); err != nil {
		t.Fatal(err)
	}
	if coded.provisions != 0 {
		t.Fatalf("code release provisions %d", coded.provisions)
	}
}

func TestContainerSetupUnknownTypeProvisionsNothing(t *testing.T) {
	backend := newSetupBackend()
	ap := &Applier{b: backend, Catalog: setupCatalog(), Runtimes: dbruntime.BuiltinCatalog(), Log: ioDiscard()}
	if _, err := ap.ApplyContainerSetup("app", []ContainerAddon{{Type: "no-such"}}, nil); err == nil {
		t.Fatal("unknown type must fail")
	}
	if backend.provisions != 0 {
		t.Fatalf("provisions %d", backend.provisions)
	}
}

func setupCatalog() *plugin.Catalog {
	return &plugin.Catalog{Addons: []plugin.AddonSpec{{
		Type:     "postgres",
		Provider: "postgres",
		Backend:  plugin.BackendResource,
		Verbs:    []string{plugin.VerbCreate},
		Runtimes: &plugin.AddonRuntimes{Source: "catalog", Engine: "postgres", Default: "small"},
	}}}
}

type setupBackend struct {
	apps       map[string]*ct.App
	head       *ct.Release
	provisions int
	lastConfig string
}

func newSetupBackend() *setupBackend {
	return &setupBackend{apps: map[string]*ct.App{
		"app": {ID: "app", Name: "example", Strategy: "all-at-once", DeployTimeout: 120},
	}}
}

func (f *setupBackend) GetApp(id string) (*ct.App, error) {
	app := f.apps[id]
	if app == nil {
		return nil, errors.New("missing app")
	}
	cp := *app
	cp.Meta = cloneStrings(app.Meta)
	return &cp, nil
}

func (f *setupBackend) UpdateApp(app *ct.App) error { return nil }

func (f *setupBackend) UpdateAppMeta(app *ct.App) error {
	f.apps[app.ID].Meta = cloneStrings(app.Meta)
	return nil
}

func (f *setupBackend) DeleteApp(string) error { return nil }

func (f *setupBackend) Head(string) (*ct.Release, error) {
	if f.head == nil {
		return nil, nil
	}
	cp := *f.head
	cp.Env = cloneStrings(f.head.Env)
	return &cp, nil
}

func (f *setupBackend) SaveRelease(_ string, rel *ct.Release, _ bool) error {
	rel.ID = "rel"
	cp := *rel
	cp.Env = cloneStrings(rel.Env)
	f.head = &cp
	return nil
}

func (f *setupBackend) Provision(req *ct.ResourceReq) (*ct.Resource, error) {
	f.provisions++
	if req.Config != nil {
		f.lastConfig = string(*req.Config)
	}
	return &ct.Resource{ID: "pg-1", Env: map[string]string{
		"DATABASE_URL":   "postgres://db",
		"FLYNN_POSTGRES": "postgresql-concave-48291",
	}}, nil
}

func (f *setupBackend) PutPluginAddon(string, string) error { return nil }

func (f *setupBackend) Routes(string) ([]*router.Route, error) { return nil, nil }

func (f *setupBackend) CreateRoute(string, *router.Route) error { return nil }

func (f *setupBackend) Scale(string, string, ct.ScaleOptions) error { return nil }

func (f *setupBackend) Profiles() ([]*ct.RuntimeProfile, error) { return nil, nil }

func (f *setupBackend) Settings() (*ct.RuntimeSettings, error) { return &ct.RuntimeSettings{}, nil }

func (f *setupBackend) RunCommand(string, *ct.Release, string, resource.Resources) error {
	return nil
}

func (f *setupBackend) RunRelease(string, *ct.Release) error { return nil }

func (f *setupBackend) Formation(string, string) (*ct.Formation, error) { return nil, nil }

func (f *setupBackend) Resources(string) ([]*ct.Resource, error) { return nil, nil }
