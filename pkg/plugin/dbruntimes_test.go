package plugin

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/randy-girard/flynn/pkg/dbruntime"
)

func TestPluginDBRuntimeEngine(t *testing.T) {
	if got := pluginDBRuntimeEngine("postgres", "postgres", true); got != "postgres" {
		t.Fatalf("postgres: %q", got)
	}
	if got := pluginDBRuntimeEngine("postgres-plugin", "", true); got != "postgres" {
		t.Fatalf("postgres-plugin: %q", got)
	}
	if got := pluginDBRuntimeEngine("mysql", "mysql", true); got != "mysql" {
		t.Fatalf("mysql: %q", got)
	}
	if got := pluginDBRuntimeEngine("mariadb", "", true); got != "mysql" {
		t.Fatalf("mariadb: %q", got)
	}
	for _, name := range []string{"redis", "mongodb", "kafka", "clickhouse"} {
		if got := pluginDBRuntimeEngine(name+"-plugin", "", true); got != name {
			t.Fatalf("%s-plugin: %q", name, got)
		}
	}
	if got := pluginDBRuntimeEngine("dashboard", "", false); got != "" {
		t.Fatalf("dashboard: %q", got)
	}
}

func TestInstallerPublishesAndRemovesDBRuntimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db-runtimes.json")
	t.Setenv(dbruntime.EnvFile, path)
	in := &Installer{Stdout: io.Discard, Stderr: io.Discard}
	m := &Manifest{
		Name:     "postgres",
		Kind:     KindResourceProvider,
		Provider: &Provider{Name: "postgres"},
		App:      AppSpec{Meta: map[string]string{MetaDatastore: "true"}},
	}
	in.ensureManifestDBRuntimes(m)
	cat, err := dbruntime.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cat.Find("postgres", "small"); !ok {
		t.Fatal("install must publish postgres small")
	}
	if _, ok := cat.Find("postgres", "medium"); !ok {
		t.Fatal("install must publish postgres medium")
	}
	if _, ok := cat.Find("postgres", "large"); !ok {
		t.Fatal("install must publish postgres large")
	}
	if _, ok := cat.Find("redis", "small"); ok {
		t.Fatal("other engines must stay unpublished")
	}
	in.removeInstalledDBRuntimes(m.Record())
	cat, err = dbruntime.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cat.Find("postgres", "small"); ok {
		t.Fatal("uninstall must drop postgres runtimes")
	}
}

type memDBRuntimeAPI struct {
	cat dbruntime.Catalog
}

func (m *memDBRuntimeAPI) ListDBRuntimes() (*dbruntime.Catalog, error) {
	c := m.cat
	return &c, nil
}

func (m *memDBRuntimeAPI) ReplaceDBRuntimes(catalog *dbruntime.Catalog) error {
	m.cat = *catalog
	return nil
}

func TestLoadPluginDBRuntimeCatalogPrefersController(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db-runtimes.json")
	file := dbruntime.EmptyCatalog()
	if err := file.EnsureEngine("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := dbruntime.Save(path, file); err != nil {
		t.Fatal(err)
	}
	api := &memDBRuntimeAPI{}
	if err := api.cat.EnsureEngine("redis"); err != nil {
		t.Fatal(err)
	}
	got := loadPluginDBRuntimeCatalog(api, path)
	if _, ok := got.Find("redis", "small"); !ok {
		t.Fatal("controller catalog must win")
	}
	if _, ok := got.Find("postgres", "small"); ok {
		t.Fatal("stale host file must not replace controller catalog")
	}
	empty := loadPluginDBRuntimeCatalog(&memDBRuntimeAPI{}, path)
	if _, ok := empty.Find("postgres", "small"); !ok {
		t.Fatal("empty controller must fall back to host file")
	}
}

func TestPluginInstallMergesControllerCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db-runtimes.json")
	t.Setenv(dbruntime.EnvFile, path)
	api := &memDBRuntimeAPI{}
	if err := api.cat.EnsureEngine("redis"); err != nil {
		t.Fatal(err)
	}
	in := &Installer{Stdout: io.Discard, Stderr: io.Discard, dbRuntimeAPI: api}
	m := &Manifest{
		Name:     "postgres",
		Kind:     KindResourceProvider,
		Provider: &Provider{Name: "postgres"},
		App:      AppSpec{Meta: map[string]string{MetaDatastore: "true"}},
	}
	in.ensureManifestDBRuntimes(m)
	if _, ok := api.cat.Find("redis", "small"); !ok {
		t.Fatal("install must keep other engines already on the controller")
	}
	if _, ok := api.cat.Find("postgres", "small"); !ok {
		t.Fatal("install must add postgres to the controller catalog")
	}
}
