package main

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	docopt "github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/blobstore/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
)

// NEW-48: slugs and image manifests stored before the per-app prefixes
// (/slugs/layers/, /slugs/images/, /tarreceive/images/) count toward no
// APP_QUOTA and nothing deleted them once no artifact named them. The legacy
// GC deletes those that no live artifact names (as a layer or as its URI),
// that are older than the grace period and that have no upload in progress.

func legacyFixture(t *testing.T, db *postgres.DB, r *data.FileRepo) map[string]bool {
	t.Helper()
	for _, name := range []string{
		// unreferenced and old: deleted
		"/slugs/layers/old.squashfs",
		"/slugs/images/old.json",
		"/tarreceive/images/old.json",
		// referenced: kept
		"/slugs/layers/used.squashfs",
		"/slugs/images/used.json",
		"/tarreceive/images/used.json",
		// upload in progress: kept
		"/slugs/images/busy.json",
		// not legacy shared objects: never looked at
		"/slugs/apps/app-1/layers/x.squashfs",
		"/slugs/apps/app-1/images/x.json",
		"/tarreceive/apps/app-1/images/m.json",
		"/tarreceive/layers/l.squashfs",
		"/tarreceive/layers/l.json",
		"/slugs/layers/sub/deep.squashfs",
		"/slugs/layers/other.tgz",
		"/layers/system.squashfs",
		"/app-1-cache.tgz",
	} {
		putObject(t, r, name)
		ageObject(t, db, name, 30*24*time.Hour)
	}
	putObject(t, r, "/slugs/layers/young.squashfs")
	ageObject(t, db, "/slugs/layers/young.squashfs", 3*24*time.Hour)
	if err := db.Exec("INSERT INTO uploads (name, backend) VALUES ($1, 'postgres')", "/slugs/images/busy.json"); err != nil {
		t.Fatal(err)
	}
	return map[string]bool{
		"/slugs/layers/used.squashfs":  true,
		"/slugs/images/used.json":      true,
		"/tarreceive/images/used.json": true,
	}
}

var keptAfterLegacyGC = []string{
	"/app-1-cache.tgz",
	"/layers/system.squashfs",
	"/slugs/apps/app-1/images/x.json",
	"/slugs/apps/app-1/layers/x.squashfs",
	"/slugs/images/busy.json",
	"/slugs/images/used.json",
	"/slugs/layers/other.tgz",
	"/slugs/layers/sub/deep.squashfs",
	"/slugs/layers/used.squashfs",
	"/slugs/layers/young.squashfs",
	"/tarreceive/apps/app-1/images/m.json",
	"/tarreceive/images/used.json",
	"/tarreceive/layers/l.json",
	"/tarreceive/layers/l.squashfs",
}

func TestLegacyGCDeletesOnlyUnreferencedOldLegacyObjects(t *testing.T) {
	db, r := layerGCDB(t)
	live := legacyFixture(t, db, r)
	g := &legacyGC{repo: r, live: func() (map[string]bool, error) { return live, nil }, grace: 7 * 24 * time.Hour}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	if got := liveNames(t, db); strings.Join(got, "\n") != strings.Join(keptAfterLegacyGC, "\n") {
		t.Fatalf("after GC:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(keptAfterLegacyGC, "\n"))
	}
	want := []string{"/slugs/images/old.json", "/slugs/layers/old.squashfs", "/tarreceive/images/old.json"}
	sort.Strings(res.Deleted)
	if strings.Join(res.Deleted, " ") != strings.Join(want, " ") {
		t.Fatalf("deleted = %v, want %v", res.Deleted, want)
	}
	if res.Referenced != 3 || res.Young != 1 || res.Uploading != 1 || res.DeletedBytes <= 0 {
		t.Fatalf("result = %+v, want 3 referenced, 1 young, 1 uploading", res)
	}
	if res, err := g.run(); err != nil || len(res.Deleted) != 0 {
		t.Fatalf("second run = %+v, %v", res, err)
	}
}

func TestLegacyGCDryRunChangesNothing(t *testing.T) {
	db, r := layerGCDB(t)
	live := legacyFixture(t, db, r)
	before := liveNames(t, db)
	g := &legacyGC{repo: r, live: func() (map[string]bool, error) { return live, nil }, grace: 7 * 24 * time.Hour, dryRun: true}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	if got := liveNames(t, db); strings.Join(got, " ") != strings.Join(before, " ") {
		t.Fatal("a dry run changed the store")
	}
	if len(res.Deleted) != 3 {
		t.Fatalf("dry run would delete %v, want 3", res.Deleted)
	}
}

func TestLegacyGCStopsWithoutLiveSet(t *testing.T) {
	db, r := layerGCDB(t)
	legacyFixture(t, db, r)
	before := liveNames(t, db)
	g := &legacyGC{repo: r, live: func() (map[string]bool, error) { return nil, errors.New("controller down") }, grace: time.Hour}
	if _, err := g.run(); err == nil || !strings.Contains(err.Error(), "controller down") {
		t.Fatalf("err = %v", err)
	}
	if got := liveNames(t, db); strings.Join(got, " ") != strings.Join(before, " ") {
		t.Fatal("the GC deleted objects without a live set")
	}
}

// An object stored again between the listing and the delete is kept.
func TestLegacyGCKeepsObjectRePutDuringTheRun(t *testing.T) {
	db, r := layerGCDB(t)
	putObject(t, r, "/slugs/layers/re.squashfs")
	ageObject(t, db, "/slugs/layers/re.squashfs", 30*24*time.Hour)
	g := &legacyGC{
		repo:  touchingRepo{FileRepo: r, t: t, touch: "/slugs/layers/re.squashfs"},
		live:  func() (map[string]bool, error) { return map[string]bool{"/x": true}, nil },
		grace: 7 * 24 * time.Hour,
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 0 || res.Young != 1 {
		t.Fatalf("result = %+v, want the re-put object kept", res)
	}
}

func TestLiveArtifactPathsIncludesURIs(t *testing.T) {
	manifest := (&ct.ImageManifest{Type: ct.ImageManifestTypeV1, Rootfs: []*ct.ImageRootfs{
		{Layers: []*ct.ImageLayer{{ID: "a"}}},
	}}).RawManifest()
	paths, err := liveArtifactPaths([]*ct.Artifact{
		{Type: ct.ArtifactTypeFlynn, URI: "http://blobstore.discoverd/slugs/images/i.json", RawManifest: manifest,
			LayerURLTemplate: "http://blobstore.discoverd/slugs/layers/{id}.squashfs"},
		{Type: ct.ArtifactTypeFlynn, URI: "https://example.com/images/e.json", RawManifest: manifest,
			LayerURLTemplate: "https://example.com/{id}.squashfs"},
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
	if want := "/slugs/images/i.json /slugs/layers/a.squashfs"; strings.Join(got, " ") != want {
		t.Fatalf("live = %v, want %s", got, want)
	}
	if _, err := liveArtifactPaths(nil); err == nil {
		t.Fatal("an empty artifact list must be refused")
	}
}

func TestCleanupLegacyCommandIsDryRunByDefault(t *testing.T) {
	var got legacyGC
	orig := cleanupLegacyOpen
	cleanupLegacyOpen = func(g *legacyGC) (func(), error) {
		got = *g
		g.repo = emptyLegacyRepo{}
		g.live = func() (map[string]bool, error) { return map[string]bool{}, nil }
		return func() {}, nil
	}
	defer func() { cleanupLegacyOpen = orig }()
	parse := func(argv ...string) *docopt.Args {
		args, err := docopt.Parse(commands["cleanup-legacy"].usage, append([]string{"cleanup-legacy"}, argv...), true, "", false)
		if err != nil {
			t.Fatal(err)
		}
		return args
	}
	if err := runCleanupLegacy(parse()); err != nil {
		t.Fatal(err)
	}
	if got.grace != 7*24*time.Hour || !got.dryRun {
		t.Fatalf("defaults = %+v, want a dry run with a 7 day grace", got)
	}
	if err := runCleanupLegacy(parse("--delete", "--grace", "48h")); err != nil {
		t.Fatal(err)
	}
	if got.grace != 48*time.Hour || got.dryRun {
		t.Fatalf("flags = %+v", got)
	}
	if err := runCleanupLegacy(parse("--grace", "0")); err == nil {
		t.Fatal("a zero grace period was accepted")
	}
}

type emptyLegacyRepo struct{}

func (emptyLegacyRepo) ListLegacyObjects() ([]data.LayerObject, error) { return nil, nil }
func (emptyLegacyRepo) DeleteIdleLayer([]string, time.Duration) (bool, int64, error) {
	return false, 0, nil
}
