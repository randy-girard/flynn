package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/dbruntime"
	"github.com/randy-girard/flynn/pkg/plugin"
	router "github.com/randy-girard/flynn/router/types"
)

const (
	e49PG       = "postgresql-cedar-21320"
	e49Redis    = "redis-estuary-54554"
	e49PGURL    = "postgres://u:secret@leader.postgresql-cedar-21320.discoverd:5432/db"
	e49RedisURL = "redis://:secret@leader.redis-estuary-54554.discoverd:6379"
)

// e49Catalog is the part of the plugin catalog export and import read: the
// pg and redis CLI specs (dump/restore actions as the plugins publish them)
// and the redis add-on type.
func e49Catalog() *plugin.Catalog {
	return &plugin.Catalog{
		Commands: []plugin.CLI{
			{
				Command:     "pg",
				App:         "postgres-plugin",
				ResourceEnv: "FLYNN_POSTGRES",
				Actions: []plugin.CLIAction{
					{Name: "dump", Args: []string{"/bin/flynn-postgres-api", "task", "dump", "${resource}"}, Scope: plugin.CLIScopeApp},
					{Name: "restore", Args: []string{"/bin/flynn-postgres-api", "task", "restore", "${resource}"}, Append: "<timestamp>", Scope: plugin.CLIScopeApp},
				},
			},
			{
				Command:     "redis",
				App:         "redis-plugin",
				ResourceEnv: "FLYNN_REDIS",
				Actions: []plugin.CLIAction{
					{
						Name:       "dump",
						Args:       []string{"/bin/dump-flynn-redis", "-h", "${app.REDIS_HOST|leader.${resource}.discoverd}"},
						Env:        map[string]string{"REDISCLI_AUTH": "${app.REDIS_PASSWORD}"},
						ReleaseEnv: true,
						StdoutFile: "--file",
						Scope:      plugin.CLIScopeApp,
					},
					{
						Name:      "restore",
						Args:      []string{"/bin/restore-flynn-redis", "-h", "${app.REDIS_HOST|leader.${resource}.discoverd}"},
						Env:       map[string]string{"APPLIANCE_KEY": "${resource.APPLIANCE_KEY}"},
						StdinFile: "--file",
						Scope:     plugin.CLIScopeApp,
					},
				},
			},
		},
		Addons: []plugin.AddonSpec{
			{
				Type:     "redis",
				Verbs:    []string{"create", "destroy", "attach", "detach"},
				Backend:  plugin.BackendResource,
				Provider: "redis",
				Env:      &plugin.AddonEnv{Prefix: "FLYNN_REDIS_", Conventional: "REDIS_URL"},
			},
		},
	}
}

func withCatalog(t *testing.T, cat *plugin.Catalog) {
	prev := fetchPluginCatalog
	fetchPluginCatalog = func() (*plugin.Catalog, error) { return cat, nil }
	t.Cleanup(func() { fetchPluginCatalog = prev })
}

// fakeJob replaces the plugin dump/restore job runner.
func withExportRunJob(t *testing.T, fn func(runConfig) error) *[]runConfig {
	var calls []runConfig
	prev := exportRunJob
	exportRunJob = func(_ controller.Client, c runConfig) error {
		calls = append(calls, c)
		return fn(c)
	}
	t.Cleanup(func() { exportRunJob = prev })
	return &calls
}

// dbExportClient serves an app whose release carries plugin attachment env
// and whose resources are plugin Postgres and/or Redis, the shape seen live
// for an app after addons:create postgres and addons:create redis.
type dbExportClient struct {
	controller.Client
	env       map[string]string
	resources []*ct.Resource
}

func (c dbExportClient) GetApp(name string) (*ct.App, error) {
	return &ct.App{ID: "app-id", Name: name}, nil
}

func (c dbExportClient) AppRouteList(string) ([]*router.Route, error) { return nil, nil }

func (c dbExportClient) GetAppRelease(name string) (*ct.Release, error) {
	switch name {
	case "demo", "app-id":
		env := make(map[string]string, len(c.env))
		for k, v := range c.env {
			env[k] = v
		}
		return &ct.Release{ID: "rel-1", Env: env}, nil
	case "blobstore":
		return &ct.Release{ID: "blob-rel"}, nil
	case "postgres":
		return &ct.Release{ID: "appliance-pg-rel"}, nil
	case e49PG, e49Redis:
		return &ct.Release{ID: "inst-rel-" + name}, nil
	}
	return nil, controller.ErrNotFound
}

func (c dbExportClient) GetFormation(string, string) (*ct.Formation, error) {
	return nil, controller.ErrNotFound
}

func (c dbExportClient) AppResourceList(string) ([]*ct.Resource, error) { return c.resources, nil }

func (c dbExportClient) GetProvider(id string) (*ct.Provider, error) {
	return &ct.Provider{ID: id, Name: strings.TrimPrefix(id, "prov-")}, nil
}

func pluginPGResource() *ct.Resource {
	return &ct.Resource{ID: "res-pg", ProviderID: "prov-postgres", Env: map[string]string{
		"FLYNN_POSTGRES":             e49PG,
		"FLYNN_POSTGRESQL_GREEN_URL": e49PGURL,
		"FLYNN_POSTGRES_ROLE":        "primary",
	}}
}

func pluginRedisResource() *ct.Resource {
	return &ct.Resource{ID: "res-redis", ProviderID: "prov-redis", Env: map[string]string{
		"FLYNN_REDIS":           e49Redis,
		"FLYNN_REDIS_AMBER_URL": e49RedisURL,
		"FLYNN_REDIS_ROLE":      "primary",
	}}
}

func pgAppEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":               e49PGURL,
		"FLYNN_POSTGRESQL_GREEN_URL": e49PGURL,
		"FLYNN_POSTGRES_ROLE":        "primary",
		"PORT":                       "8080",
	}
}

func redisAppEnv() map[string]string {
	return map[string]string{
		"REDIS_URL":             e49RedisURL,
		"FLYNN_REDIS_AMBER_URL": e49RedisURL,
		"FLYNN_REDIS_ROLE":      "primary",
		"PORT":                  "8080",
	}
}

func exportDBArgs(file string, skip bool) *docopt.Args {
	return &docopt.Args{
		String: map[string]string{"--file": file},
		Bool:   map[string]bool{"--quiet": true, "--skip-databases": skip},
	}
}

func tarFiles(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string][]byte{}
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("export is not a complete tar: %s", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name] = data
	}
}

func readExportManifest(t *testing.T, files map[string][]byte) exportResourceManifest {
	t.Helper()
	raw, ok := files["demo/resources.json"]
	if !ok {
		t.Fatalf("export has no resources.json: %v", sortedNames(files))
	}
	var m exportResourceManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("resources.json: %s", err)
	}
	return m
}

func sortedNames(files map[string][]byte) []string {
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// EDGE-49: an app with a plugin Postgres and no FLYNN_POSTGRES used to export
// app.json, routes.json and release.json and exit 0.
func TestExportFailsWhenAttachedDatabaseCannotBeDumped(t *testing.T) {
	withExportApp(t, "demo")
	withCatalog(t, e49Catalog())
	dir := t.TempDir()
	path := filepath.Join(dir, "app.tar")
	client := dbExportClient{env: pgAppEnv(), resources: []*ct.Resource{pluginPGResource()}}
	err := runExport(exportDBArgs(path, false), client)
	if err == nil {
		t.Fatalf("export of an app with a plugin Postgres it cannot dump succeeded: %v", sortedNames(tarFiles(t, path)))
	}
	for _, want := range []string{e49PG, "provider postgres", "flynn -a demo pg:dump " + e49PG, "--skip-databases"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if _, serr := os.Stat(path); !os.IsNotExist(serr) {
		t.Fatalf("failed export left %s (stat err %v)", path, serr)
	}
	assertNoExportTemp(t, dir)
}

// The built-in appliance (FLYNN_POSTGRES and PG* on the app release) keeps
// the postgres.dump it always had, and its resource counts as exported.
func TestExportPlanKeepsLegacyPostgresDump(t *testing.T) {
	env := map[string]string{
		"FLYNN_POSTGRES": "postgres", "PGHOST": "leader.postgres.discoverd", "PGUSER": "u",
		"PGPASSWORD": "p", "PGDATABASE": "db", "DATABASE_URL": "postgres://u:p@leader.postgres.discoverd/db",
	}
	res := &ct.Resource{ID: "res-legacy", ProviderID: "prov-postgres", Env: map[string]string{}}
	for k, v := range env {
		res.Env[k] = v
	}
	client := dbExportClient{env: env, resources: []*ct.Resource{res}}
	release, _ := client.GetAppRelease("demo")
	plan, err := planExport(client, "demo", release, exportOptions{})
	if err != nil {
		t.Fatalf("plan: %s", err)
	}
	if plan.legacyPG == nil || plan.legacyPG.Release != "appliance-pg-rel" {
		t.Fatalf("legacy pg config = %+v", plan.legacyPG)
	}
	if len(plan.manifest.Resources) != 1 || plan.manifest.Resources[0].Data != "postgres.dump" || len(plan.pluginDumps) != 0 {
		t.Fatalf("manifest = %+v", plan.manifest)
	}
	if got := strings.Join(plan.manifest.Resources[0].EnvKeys, ","); got != "DATABASE_URL,FLYNN_POSTGRES,PGDATABASE,PGHOST,PGPASSWORD,PGUSER" {
		t.Fatalf("env_keys = %s", got)
	}
}

func TestExportFailsWhenCatalogIsUnavailable(t *testing.T) {
	withExportApp(t, "demo")
	// TestMain's catalog stub fails, like a cluster whose plugin apps cannot be read.
	client := dbExportClient{env: redisAppEnv(), resources: []*ct.Resource{pluginRedisResource()}}
	err := exportApp(client, io.Discard, nil, exportOptions{})
	if err == nil || !strings.Contains(err.Error(), e49Redis) || !strings.Contains(err.Error(), "provider redis") {
		t.Fatalf("err = %v, want a failure naming %s and its provider", err, e49Redis)
	}
}

func TestExportSkipDatabasesWarnsAndRecordsSkipped(t *testing.T) {
	withExportApp(t, "demo")
	withCatalog(t, e49Catalog())
	path := filepath.Join(t.TempDir(), "app.tar")
	client := dbExportClient{env: pgAppEnv(), resources: []*ct.Resource{pluginPGResource()}}
	var logs bytes.Buffer
	withLogOutput(t, &logs)
	if err := runExport(exportDBArgs(path, true), client); err != nil {
		t.Fatalf("export --skip-databases: %s", err)
	}
	files := tarFiles(t, path)
	m := readExportManifest(t, files)
	if len(m.DatabasesSkipped) != 1 || m.DatabasesSkipped[0] != e49PG {
		t.Fatalf("databases-skipped = %v", m.DatabasesSkipped)
	}
	if len(m.Resources) != 1 || !m.Resources[0].Skipped || m.Resources[0].Data != "" || m.Resources[0].Provider != "postgres" {
		t.Fatalf("resources = %+v", m.Resources)
	}
	keys := strings.Join(m.Resources[0].EnvKeys, ",")
	if keys != "DATABASE_URL,FLYNN_POSTGRESQL_GREEN_URL,FLYNN_POSTGRES_ROLE" {
		t.Fatalf("env_keys = %s", keys)
	}
	if _, ok := files["demo/postgres.dump"]; ok {
		t.Fatal("skipped database has a dump")
	}
	if !strings.Contains(logs.String(), "WARNING") || !strings.Contains(logs.String(), e49PG) {
		t.Fatalf("no warning naming the skipped resource: %q", logs.String())
	}
}

func TestExportIncludesPluginRedisDump(t *testing.T) {
	withExportApp(t, "demo")
	withCatalog(t, e49Catalog())
	calls := withExportRunJob(t, func(c runConfig) error {
		_, err := c.Stdout.Write([]byte("REDIS0011-fake-rdb"))
		return err
	})
	path := filepath.Join(t.TempDir(), "app.tar")
	client := dbExportClient{env: redisAppEnv(), resources: []*ct.Resource{pluginRedisResource()}}
	if err := runExport(exportDBArgs(path, false), client); err != nil {
		t.Fatalf("export: %s", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("dump jobs = %d, want 1", len(*calls))
	}
	c := (*calls)[0]
	if c.App != "demo" || c.Release != "inst-rel-"+e49Redis || len(c.Args) == 0 || c.Args[0] != "/bin/dump-flynn-redis" {
		t.Fatalf("dump job = app %q release %q args %v", c.App, c.Release, c.Args)
	}
	if c.Exit {
		t.Fatal("dump job would exit the CLI when it finishes")
	}
	files := tarFiles(t, path)
	entry := "resources/" + e49Redis + ".dump"
	if got := string(files["demo/"+entry]); got != "REDIS0011-fake-rdb" {
		t.Fatalf("%s = %q (entries %v)", entry, got, sortedNames(files))
	}
	m := readExportManifest(t, files)
	if len(m.Resources) != 1 || m.Resources[0].Data != entry || m.Resources[0].Skipped || len(m.DatabasesSkipped) != 0 {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestExportFailsWhenPluginDumpFails(t *testing.T) {
	withExportApp(t, "demo")
	withCatalog(t, e49Catalog())
	withExportRunJob(t, func(c runConfig) error {
		io.WriteString(c.Stderr, "redis-cli produced an empty dump\n")
		return RunExitError(1)
	})
	client := dbExportClient{env: redisAppEnv(), resources: []*ct.Resource{pluginRedisResource()}}
	err := exportApp(client, io.Discard, nil, exportOptions{})
	if err == nil || !strings.Contains(err.Error(), e49Redis) || !strings.Contains(err.Error(), "empty dump") {
		t.Fatalf("err = %v, want the dump failure for %s", err, e49Redis)
	}
}

func withLogOutput(t *testing.T, w io.Writer) {
	t.Helper()
	prev := exportLogf
	exportLogf = func(format string, args ...interface{}) {
		io.WriteString(w, strings.TrimSpace(fmt.Sprintf(format, args...))+"\n")
	}
	t.Cleanup(func() { exportLogf = prev })
}

// importClient records what apps:import creates.
type importClient struct {
	controller.Client
	app        *ct.App
	release    *ct.Release
	provisions []*ct.ResourceReq
	resources  []*ct.Resource
	newRes     *ct.Resource
}

func (c *importClient) CreateApp(app *ct.App) error {
	app.ID = "new-app-id"
	c.app = app
	return nil
}

func (c *importClient) GetApp(string) (*ct.App, error) {
	cp := *c.app
	return &cp, nil
}

func (c *importClient) UpdateAppMeta(app *ct.App) error {
	c.app.Meta = app.Meta
	return nil
}

func (c *importClient) CreateRelease(appID string, r *ct.Release) error {
	r.ID = "new-rel"
	cp := *r
	cp.Env = make(map[string]string, len(r.Env))
	for k, v := range r.Env {
		cp.Env[k] = v
	}
	c.release = &cp
	return nil
}

func (c *importClient) SetAppRelease(string, string) error { return nil }

func (c *importClient) ProvisionResource(req *ct.ResourceReq) (*ct.Resource, error) {
	c.provisions = append(c.provisions, req)
	c.resources = append(c.resources, c.newRes)
	return c.newRes, nil
}

func (c *importClient) AppResourceList(string) ([]*ct.Resource, error) { return c.resources, nil }

func (c *importClient) ListDBRuntimes() (*dbruntime.Catalog, error) {
	return &dbruntime.Catalog{}, nil
}

func (c *importClient) GetAppRelease(name string) (*ct.Release, error) {
	if c.newRes != nil && name == c.newRes.Env["FLYNN_REDIS"] {
		return &ct.Release{ID: "inst-rel-" + name, Env: map[string]string{"APPLIANCE_KEY": "new-appliance-key"}}, nil
	}
	return nil, controller.ErrNotFound
}

type tarEntry struct {
	name string
	data []byte
}

func writeTestArchive(t *testing.T, entries ...tarEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in.tar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: "e49-src/" + e.name, Mode: 0600, Size: int64(len(e.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func jsonEntry(t *testing.T, name string, v interface{}) tarEntry {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return tarEntry{name, data}
}

func importArgs(file string, keep bool) *docopt.Args {
	return &docopt.Args{
		String: map[string]string{"--file": file, "--name": "e49-copy", "--jobs": "4"},
		Bool:   map[string]bool{"--quiet": true, "--keep-database-env": keep},
	}
}

// sourceEnv is the release env of the live EDGE-49 source app plus one plain
// setting and one non-database URL that import must keep.
func sourceEnv() map[string]string {
	env := map[string]string{"API_URL": "https://api.example.com/v1"}
	for k, v := range pgAppEnv() {
		env[k] = v
	}
	for k, v := range redisAppEnv() {
		env[k] = v
	}
	return env
}

func sourceApp() *ct.App {
	return &ct.App{ID: "src-id", Name: "e49-src", Meta: map[string]string{
		metaAddonAttachments: `{"GREEN":{"addon":"` + e49PG + `","resource_id":"res-pg","env_key":"FLYNN_POSTGRESQL_GREEN_URL"}}`,
		"other":              "kept",
	}}
}

func envKeys(env map[string]string) string {
	var keys []string
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// EDGE-49: an archive made by the old export (no resources.json) carries the
// source app's DATABASE_URL, and the old import copied it, so the copy wrote
// into the source database.
func TestImportDropsSourceDatabaseEnv(t *testing.T) {
	path := writeTestArchive(t,
		jsonEntry(t, "app.json", sourceApp()),
		jsonEntry(t, "release.json", &ct.Release{ID: "rel-1", Env: sourceEnv()}),
	)
	client := &importClient{}
	if err := runImport(importArgs(path, false), client); err != nil {
		t.Fatalf("import: %s", err)
	}
	if client.release == nil {
		t.Fatal("no release created")
	}
	if got := envKeys(client.release.Env); got != "API_URL,PORT" {
		t.Fatalf("imported env keys = %s, want API_URL,PORT (the source app's database env must not be copied)", got)
	}
	if _, ok := client.app.Meta[metaAddonAttachments]; ok {
		t.Fatal("imported app kept the source app's add-on attachment records")
	}
	if client.app.Meta["other"] != "kept" {
		t.Fatalf("app meta = %v", client.app.Meta)
	}
	if len(client.provisions) != 0 {
		t.Fatalf("provisioned %d resources", len(client.provisions))
	}
}

func TestImportKeepDatabaseEnvFlagKeepsSourceEnv(t *testing.T) {
	path := writeTestArchive(t,
		jsonEntry(t, "app.json", sourceApp()),
		jsonEntry(t, "release.json", &ct.Release{ID: "rel-1", Env: sourceEnv()}),
	)
	client := &importClient{}
	if err := runImport(importArgs(path, true), client); err != nil {
		t.Fatalf("import: %s", err)
	}
	if got, want := envKeys(client.release.Env), envKeys(sourceEnv()); got != want {
		t.Fatalf("imported env keys = %s, want %s", got, want)
	}
}

// With resources.json the export names the env each resource owns, so an
// unrelated database URL the user set by hand stays.
func TestImportManifestDropsOnlyResourceEnv(t *testing.T) {
	env := pgAppEnv()
	env["EXTERNAL_DATABASE_URL"] = "postgres://u:p@db.example.com/x"
	path := writeTestArchive(t,
		jsonEntry(t, "app.json", sourceApp()),
		jsonEntry(t, "release.json", &ct.Release{ID: "rel-1", Env: env}),
		jsonEntry(t, "resources.json", exportResourceManifest{
			Resources: []exportResource{{
				Name: e49PG, Provider: "postgres", ID: "res-pg", Skipped: true,
				EnvKeys: []string{"DATABASE_URL", "FLYNN_POSTGRESQL_GREEN_URL", "FLYNN_POSTGRES_ROLE"},
			}},
			DatabasesSkipped: []string{e49PG},
		}),
	)
	client := &importClient{}
	if err := runImport(importArgs(path, false), client); err != nil {
		t.Fatalf("import: %s", err)
	}
	if got := envKeys(client.release.Env); got != "EXTERNAL_DATABASE_URL,PORT" {
		t.Fatalf("imported env keys = %s", got)
	}
}

func TestImportRestoresPluginRedisDumpIntoNewResource(t *testing.T) {
	withCatalog(t, e49Catalog())
	prevDelay := importRestoreRetryDelay
	importRestoreRetryDelay = time.Millisecond
	t.Cleanup(func() { importRestoreRetryDelay = prevDelay })
	prevApp := flagApp
	t.Cleanup(func() { flagApp = prevApp })

	const newName = "redis-harbor-11111"
	const newURL = "redis://:newsecret@leader.redis-harbor-11111.discoverd:6379"
	entry := "resources/" + e49Redis + ".dump"
	path := writeTestArchive(t,
		jsonEntry(t, "app.json", sourceApp()),
		jsonEntry(t, "release.json", &ct.Release{ID: "rel-1", Env: redisAppEnv()}),
		jsonEntry(t, "resources.json", exportResourceManifest{Resources: []exportResource{{
			Name: e49Redis, Provider: "redis", ID: "res-redis", Data: entry,
			EnvKeys: []string{"FLYNN_REDIS_AMBER_URL", "FLYNN_REDIS_ROLE", "REDIS_URL"},
		}}}),
		tarEntry{entry, []byte("REDIS0011-fake-rdb")},
	)
	client := &importClient{newRes: &ct.Resource{ID: "new-res", ProviderID: "prov-redis", Apps: []string{"new-app-id"}, Env: map[string]string{
		"FLYNN_REDIS":          newName,
		"FLYNN_REDIS_BLUE_URL": newURL,
		"REDIS_URL":            newURL,
	}}}
	attempts := 0
	var stdin []string
	calls := withExportRunJob(t, func(c runConfig) error {
		attempts++
		data, _ := io.ReadAll(c.Stdin)
		stdin = append(stdin, string(data))
		if attempts == 1 {
			io.WriteString(c.Stderr, "curl: (6) Could not resolve host\n")
			return RunExitError(6)
		}
		return nil
	})
	if err := runImport(importArgs(path, false), client); err != nil {
		t.Fatalf("import: %s", err)
	}
	if len(client.provisions) != 1 || client.provisions[0].ProviderID != "redis" || strings.Join(client.provisions[0].Apps, ",") != "new-app-id" {
		t.Fatalf("provisions = %+v", client.provisions)
	}
	if len(*calls) != 2 {
		t.Fatalf("restore attempts = %d, want 2 (one retry while the new instance starts)", len(*calls))
	}
	c := (*calls)[1]
	if c.App != "e49-copy" || len(c.Args) == 0 || c.Args[0] != "/bin/restore-flynn-redis" || c.Env["APPLIANCE_KEY"] != "new-appliance-key" || c.Exit {
		t.Fatalf("restore job = app %q args %v env keys %s exit %v", c.App, c.Args, envKeys(c.Env), c.Exit)
	}
	for i, s := range stdin {
		if s != "REDIS0011-fake-rdb" {
			t.Fatalf("restore attempt %d read %q", i+1, s)
		}
	}
	if got := client.release.Env["REDIS_URL"]; got != newURL {
		t.Fatalf("REDIS_URL = %q, want the new resource's URL", got)
	}
	for k, v := range client.release.Env {
		if v == e49RedisURL {
			t.Fatalf("%s still points at the source Redis", k)
		}
	}
	if _, ok := client.release.Env["FLYNN_REDIS_AMBER_URL"]; ok {
		t.Fatal("source attachment key copied")
	}
	if flagApp != prevApp {
		t.Fatalf("flagApp left at %q", flagApp)
	}
}

func TestImportFailsWhenRestoreNeverSucceeds(t *testing.T) {
	withCatalog(t, e49Catalog())
	prevDelay, prevTimeout := importRestoreRetryDelay, importRestoreTimeout
	importRestoreRetryDelay, importRestoreTimeout = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { importRestoreRetryDelay, importRestoreTimeout = prevDelay, prevTimeout })
	prevApp := flagApp
	t.Cleanup(func() { flagApp = prevApp })

	entry := "resources/" + e49Redis + ".dump"
	path := writeTestArchive(t,
		jsonEntry(t, "app.json", sourceApp()),
		jsonEntry(t, "release.json", &ct.Release{ID: "rel-1", Env: redisAppEnv()}),
		jsonEntry(t, "resources.json", exportResourceManifest{Resources: []exportResource{{
			Name: e49Redis, Provider: "redis", Data: entry, EnvKeys: []string{"REDIS_URL"},
		}}}),
		tarEntry{entry, []byte("REDIS0011-fake-rdb")},
	)
	client := &importClient{newRes: &ct.Resource{ID: "new-res", ProviderID: "prov-redis", Env: map[string]string{"FLYNN_REDIS": "redis-harbor-11111"}}}
	withExportRunJob(t, func(c runConfig) error { return errors.New("connection refused") })
	err := runImport(importArgs(path, false), client)
	if err == nil || !strings.Contains(err.Error(), "redis-harbor-11111") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v, want the restore failure naming the new resource", err)
	}
	if client.release != nil {
		t.Fatal("release created after a failed restore")
	}
}

func TestDatabaseEnvKeyHeuristic(t *testing.T) {
	drop := map[string]string{
		"DATABASE_URL":                  "postgres://x",
		"REDIS_URL":                     "redis://x",
		"FLYNN_POSTGRES":                "postgres",
		"FLYNN_MYSQL":                   "mysql",
		"FLYNN_REDIS":                   "redis-x",
		"FLYNN_POSTGRESQL_GREEN_URL":    "postgres://x",
		"FLYNN_MONGODB_RED_URL":         "mongodb://x",
		"PGHOST":                        "leader.postgres.discoverd",
		"PGPASSWORD":                    "p",
		"MYSQL_PWD":                     "p",
		"ANALYTICS_URL":                 "postgresql://x",
		"PG_WILLOW_ACMAOS_DATABASE_URL": "postgres://x",
		"CACHE_URL":                     "rediss://x",
	}
	for k, v := range drop {
		if !databaseEnvKey(k, v) {
			t.Errorf("%s=%s kept", k, v)
		}
	}
	keep := map[string]string{
		"PORT":       "8080",
		"API_URL":    "https://api.example.com",
		"SECRET_KEY": "postgres://not-a-url-key",
		"SLUG_URL":   "http://blobstore.discoverd/x",
	}
	for k, v := range keep {
		if databaseEnvKey(k, v) {
			t.Errorf("%s=%s dropped", k, v)
		}
	}
}
