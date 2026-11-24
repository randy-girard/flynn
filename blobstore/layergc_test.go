package main

import (
	"bytes"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	docopt "github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/blobstore/backend"
	"github.com/randy-girard/flynn/blobstore/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/random"
)

// EDGE-35: tarreceive layers that no artifact names any more are deleted
// once they are older than a grace period. Layers an artifact names, layers
// younger than the grace period (counting a reuse, which re-puts the
// config), and layers with an upload in progress are kept.

func layerGCDB(t *testing.T) (*postgres.DB, *data.FileRepo) {
	t.Helper()
	db := createDB(t, "blobstore_layergc")
	migrateDB(t, db)
	t.Cleanup(db.Close)
	return db, data.NewFileRepo(db, []backend.Backend{backend.Postgres}, "postgres")
}

func putObject(t *testing.T, r *data.FileRepo, name string) {
	t.Helper()
	if err := r.Put(name, bytes.NewReader([]byte("data of "+name)), 0, "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
}

func ageObject(t *testing.T, db *postgres.DB, name string, age time.Duration) {
	t.Helper()
	if err := db.Exec("UPDATE files SET created_at = now() - make_interval(secs => $2) WHERE name = $1 AND deleted_at IS NULL", name, age.Seconds()); err != nil {
		t.Fatal(err)
	}
}

func liveNames(t *testing.T, db *postgres.DB) []string {
	t.Helper()
	rows, err := db.Query("SELECT name FROM files WHERE deleted_at IS NULL ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	return names
}

// layerFixture stores the layers the GC tests look at and returns the live
// layer set (what the controller's artifacts name).
func layerFixture(t *testing.T, db *postgres.DB, r *data.FileRepo) map[string]bool {
	t.Helper()
	old := 48 * time.Hour
	for _, name := range []string{
		// unreferenced and old: deleted
		"/tarreceive/layers/old.squashfs", "/tarreceive/layers/old.json",
		"/tarreceive/apps/app-1/layers/gone.squashfs", "/tarreceive/apps/app-1/layers/gone.json",
		// a config left without its squashfs
		"/tarreceive/apps/app-1/layers/half.json",
		// referenced by an artifact: kept
		"/tarreceive/apps/app-1/layers/used.squashfs", "/tarreceive/apps/app-1/layers/used.json",
		"/tarreceive/layers/sharedused.squashfs", "/tarreceive/layers/sharedused.json",
		// old squashfs, config re-put by a reuse: kept
		"/tarreceive/apps/app-1/layers/touched.squashfs",
		// upload in progress: kept
		"/tarreceive/apps/app-1/layers/busy.squashfs",
		// not layers: never looked at
		"/tarreceive/apps/app-1/images/manifest.json",
		"/tarreceive/images/manifest.json",
		"/slugs/apps/app-1/layers/slug.squashfs",
		"/layers/system.squashfs",
	} {
		putObject(t, r, name)
		ageObject(t, db, name, old)
	}
	// young: kept
	putObject(t, r, "/tarreceive/apps/app-2/layers/young.squashfs")
	putObject(t, r, "/tarreceive/apps/app-2/layers/young.json")
	putObject(t, r, "/tarreceive/apps/app-1/layers/touched.json")
	if err := db.Exec("INSERT INTO uploads (name, backend) VALUES ($1, 'postgres')", "/tarreceive/apps/app-1/layers/busy.json"); err != nil {
		t.Fatal(err)
	}
	return map[string]bool{
		"/tarreceive/apps/app-1/layers/used.squashfs": true,
		"/tarreceive/layers/sharedused.squashfs":      true,
	}
}

var keptAfterGC = []string{
	"/layers/system.squashfs",
	"/slugs/apps/app-1/layers/slug.squashfs",
	"/tarreceive/apps/app-1/images/manifest.json",
	"/tarreceive/apps/app-1/layers/busy.squashfs",
	"/tarreceive/apps/app-1/layers/touched.json",
	"/tarreceive/apps/app-1/layers/touched.squashfs",
	"/tarreceive/apps/app-1/layers/used.json",
	"/tarreceive/apps/app-1/layers/used.squashfs",
	"/tarreceive/apps/app-2/layers/young.json",
	"/tarreceive/apps/app-2/layers/young.squashfs",
	"/tarreceive/images/manifest.json",
	"/tarreceive/layers/sharedused.json",
	"/tarreceive/layers/sharedused.squashfs",
}

func TestLayerGCDeletesOnlyUnreferencedOldLayers(t *testing.T) {
	db, r := layerGCDB(t)
	live := layerFixture(t, db, r)
	before := liveNames(t, db)

	g := &layerGC{repo: r, live: func() (map[string]bool, error) { return live, nil }, grace: 24 * time.Hour}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	if got := liveNames(t, db); strings.Join(got, "\n") != strings.Join(keptAfterGC, "\n") {
		t.Fatalf("after GC:\n%s\nwant:\n%s\n(before:\n%s)", strings.Join(got, "\n"), strings.Join(keptAfterGC, "\n"), strings.Join(before, "\n"))
	}
	want := []string{"/tarreceive/apps/app-1/layers/gone", "/tarreceive/apps/app-1/layers/half", "/tarreceive/layers/old"}
	sort.Strings(res.Deleted)
	if strings.Join(res.Deleted, " ") != strings.Join(want, " ") {
		t.Fatalf("deleted = %v, want %v", res.Deleted, want)
	}
	if res.Referenced != 2 || res.Young != 2 || res.Uploading != 1 {
		t.Fatalf("result = %+v, want 2 referenced, 2 young (young, touched), 1 uploading", res)
	}
	if res.DeletedBytes <= 0 {
		t.Fatalf("deleted bytes = %d", res.DeletedBytes)
	}

	// a second run finds nothing more
	res, err = g.run()
	if err != nil || len(res.Deleted) != 0 {
		t.Fatalf("second run = %+v, %v", res, err)
	}
}

func TestLayerGCDryRunChangesNothing(t *testing.T) {
	db, r := layerGCDB(t)
	live := layerFixture(t, db, r)
	before := liveNames(t, db)
	g := &layerGC{repo: r, live: func() (map[string]bool, error) { return live, nil }, grace: 24 * time.Hour, dryRun: true}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	if got := liveNames(t, db); strings.Join(got, " ") != strings.Join(before, " ") {
		t.Fatalf("dry run changed the store:\n%v\nwas\n%v", got, before)
	}
	if len(res.Deleted) != 3 {
		t.Fatalf("dry run would delete %v, want 3 layers", res.Deleted)
	}
}

// When the live set cannot be read the GC deletes nothing.
func TestLayerGCStopsWithoutLiveSet(t *testing.T) {
	db, r := layerGCDB(t)
	layerFixture(t, db, r)
	before := liveNames(t, db)
	g := &layerGC{repo: r, live: func() (map[string]bool, error) { return nil, errors.New("controller down") }, grace: 24 * time.Hour}
	if _, err := g.run(); err == nil || !strings.Contains(err.Error(), "controller down") {
		t.Fatalf("err = %v", err)
	}
	if got := liveNames(t, db); strings.Join(got, " ") != strings.Join(before, " ") {
		t.Fatal("GC deleted objects without a live set")
	}
}

// touchingRepo re-puts a layer's config between the listing and the delete,
// as a tarreceive reuse would.
type touchingRepo struct {
	*data.FileRepo
	t     *testing.T
	touch string
}

func (r touchingRepo) DeleteIdleLayer(names []string, grace time.Duration) (bool, int64, error) {
	putObject(r.t, r.FileRepo, r.touch)
	return r.FileRepo.DeleteIdleLayer(names, grace)
}

func TestLayerGCKeepsLayerReusedDuringTheRun(t *testing.T) {
	db, r := layerGCDB(t)
	for _, name := range []string{"/tarreceive/apps/app-1/layers/re.squashfs", "/tarreceive/apps/app-1/layers/re.json"} {
		putObject(t, r, name)
		ageObject(t, db, name, 48*time.Hour)
	}
	g := &layerGC{
		repo:  touchingRepo{FileRepo: r, t: t, touch: "/tarreceive/apps/app-1/layers/re.json"},
		live:  func() (map[string]bool, error) { return map[string]bool{"/x": true}, nil },
		grace: 24 * time.Hour,
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 0 || res.Young != 1 {
		t.Fatalf("result = %+v, want the reused layer kept", res)
	}
	if got := liveNames(t, db); len(got) != 2 {
		t.Fatalf("live = %v", got)
	}
}

func TestLiveLayerPaths(t *testing.T) {
	manifest := ct.ImageManifest{Type: ct.ImageManifestTypeV1, Rootfs: []*ct.ImageRootfs{
		{Layers: []*ct.ImageLayer{{ID: "a"}, {ID: "b"}}},
		{Layers: []*ct.ImageLayer{{ID: "c"}}},
	}}.RawManifest()
	paths, err := liveLayerPaths([]*ct.Artifact{
		{Type: ct.ArtifactTypeFlynn, RawManifest: manifest, LayerURLTemplate: "http://blobstore.discoverd/tarreceive/apps/app-1/layers/{id}.squashfs",
			Meta: map[string]string{"layer_url.c": "http://blobstore.discoverd/tarreceive/layers/c.squashfs"}},
		{Type: ct.ArtifactTypeFlynn, RawManifest: manifest, LayerURLTemplate: "http://blobstore.discoverd/tarreceive/layers/{id}.squashfs"},
		{Type: ct.ArtifactTypeFlynn, RawManifest: manifest, LayerURLTemplate: "https://example.com/{id}.squashfs"},
		{Type: ct.DeprecatedArtifactTypeDocker, URI: "https://registry.example.com/x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for p := range paths {
		got = append(got, p)
	}
	sort.Strings(got)
	want := []string{
		"/tarreceive/apps/app-1/layers/a.squashfs",
		"/tarreceive/apps/app-1/layers/b.squashfs",
		"/tarreceive/layers/a.squashfs",
		"/tarreceive/layers/b.squashfs",
		"/tarreceive/layers/c.squashfs",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("live = %v, want %v", got, want)
	}
	// a controller with no artifacts at all is a broken answer, not an
	// empty cluster: refuse to sweep on it
	if _, err := liveLayerPaths(nil); err == nil {
		t.Fatal("an empty artifact list must be refused")
	}
}

func TestLayerGCSettingsFromEnv(t *testing.T) {
	t.Setenv("LAYER_GC_INTERVAL", "")
	t.Setenv("LAYER_GC_GRACE", "")
	interval, grace, err := layerGCFromEnv()
	if err != nil || interval != 0 || grace != defaultLayerGCGrace {
		t.Fatalf("defaults = %s %s %v, want off and %s", interval, grace, err, defaultLayerGCGrace)
	}
	if defaultLayerGCGrace != 24*time.Hour {
		t.Fatalf("default grace = %s", defaultLayerGCGrace)
	}
	t.Setenv("LAYER_GC_INTERVAL", "6h")
	t.Setenv("LAYER_GC_GRACE", "72h")
	if interval, grace, err = layerGCFromEnv(); err != nil || interval != 6*time.Hour || grace != 72*time.Hour {
		t.Fatalf("set = %s %s %v", interval, grace, err)
	}
	for _, bad := range [][2]string{{"x", ""}, {"1h", "0"}, {"1h", "-1h"}, {"1h", "y"}} {
		t.Setenv("LAYER_GC_INTERVAL", bad[0])
		t.Setenv("LAYER_GC_GRACE", bad[1])
		if _, _, err := layerGCFromEnv(); err == nil {
			t.Fatalf("LAYER_GC_INTERVAL=%q LAYER_GC_GRACE=%q accepted", bad[0], bad[1])
		}
	}
}

func TestCleanupLayersCommandArgs(t *testing.T) {
	var got layerGC
	orig := cleanupLayersOpen
	cleanupLayersOpen = func(g *layerGC) (func(), error) {
		got = *g
		g.repo = emptyLayerRepo{}
		g.live = func() (map[string]bool, error) { return map[string]bool{}, nil }
		return func() {}, nil
	}
	defer func() { cleanupLayersOpen = orig }()
	parse := func(argv ...string) *docopt.Args {
		args, err := docopt.Parse(commands["cleanup-layers"].usage, append([]string{"cleanup-layers"}, argv...), true, "", false)
		if err != nil {
			t.Fatal(err)
		}
		return args
	}
	if err := runCleanupLayers(parse()); err != nil {
		t.Fatal(err)
	}
	if got.grace != 24*time.Hour || got.dryRun {
		t.Fatalf("defaults = %+v", got)
	}
	if err := runCleanupLayers(parse("--dry-run", "--grace", "90m")); err != nil {
		t.Fatal(err)
	}
	if got.grace != 90*time.Minute || !got.dryRun {
		t.Fatalf("flags = %+v", got)
	}
	if err := runCleanupLayers(parse("--grace", "0")); err == nil {
		t.Fatal("a zero grace period was accepted")
	}
}

type emptyLayerRepo struct{}

func (emptyLayerRepo) ListLayerObjects() ([]data.LayerObject, error) { return nil, nil }
func (emptyLayerRepo) DeleteIdleLayer([]string, time.Duration) (bool, int64, error) {
	return false, 0, nil
}

// A layer under an app's prefix counts toward that app's APP_QUOTA (EDGE-08
// appForPath); the refusal names the setting.
func TestAppLayerCountsTowardQuota(t *testing.T) {
	srv, _, done := limitServer(t, putLimits{appQuota: 1000})
	defer done()
	app := random.UUID()
	if code, msg := putBlob(t, srv, "/tarreceive/apps/"+app+"/layers/a.squashfs", bytes.Repeat([]byte("a"), 600), 0, false); code != 200 {
		t.Fatalf("first layer = %d %s", code, msg)
	}
	code, msg := putBlob(t, srv, "/tarreceive/apps/"+app+"/layers/b.squashfs", bytes.Repeat([]byte("b"), 600), 0, false)
	if code != http.StatusInsufficientStorage || !strings.Contains(msg, "APP_QUOTA") {
		t.Fatalf("second layer = %d %s, want 507 naming APP_QUOTA", code, msg)
	}
	// the shared legacy store is not an app's
	if code, msg := putBlob(t, srv, "/tarreceive/layers/"+random.String(8)+".squashfs", bytes.Repeat([]byte("c"), 1200), 0, false); code != 200 {
		t.Fatalf("shared layer = %d %s", code, msg)
	}
}
