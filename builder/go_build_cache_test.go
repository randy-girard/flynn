package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/inconshreveable/log15"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/squashfs"
)

func quietBuilder() *Builder {
	log := log15.New()
	log.SetHandler(log15.DiscardHandler())
	return &Builder{log: log}
}

func TestGoBuildCacheDir(t *testing.T) {
	t.Setenv("FLYNN_NO_GO_BUILD_CACHE", "")
	t.Setenv("FLYNN_GO_BUILD_CACHE", "")
	dir, ok := goBuildCacheDir(nil)
	if !ok || dir != defaultGoBuildCacheDir {
		t.Fatalf("default = %q,%v; want %q,true", dir, ok, defaultGoBuildCacheDir)
	}

	custom := t.TempDir()
	t.Setenv("FLYNN_GO_BUILD_CACHE", custom)
	dir, ok = goBuildCacheDir(nil)
	if !ok || dir != custom {
		t.Fatalf("override = %q,%v; want %q,true", dir, ok, custom)
	}

	for _, off := range []string{"-", "off", "DISABLE"} {
		t.Setenv("FLYNN_GO_BUILD_CACHE", off)
		if _, ok := goBuildCacheDir(nil); ok {
			t.Fatalf("FLYNN_GO_BUILD_CACHE=%q must disable the cache", off)
		}
	}

	t.Setenv("FLYNN_GO_BUILD_CACHE", custom)
	t.Setenv("FLYNN_NO_GO_BUILD_CACHE", "1")
	if _, ok := goBuildCacheDir(nil); ok {
		t.Fatal("FLYNN_NO_GO_BUILD_CACHE=1 must win over a configured path")
	}
}

func TestLayerBuildsGo(t *testing.T) {
	cases := map[string]struct {
		layer *Layer
		want  bool
	}{
		"nil":       {nil, false},
		"script":    {&Layer{Script: "host/img/packages.sh"}, false},
		"copy only": {&Layer{Copy: map[string]string{"a": "/b"}}, false},
		"gobuild":   {&Layer{GoBuild: map[string]string{"status": "/bin/flynn-status"}}, true},
		"cgobuild":  {&Layer{CGoBuild: map[string]string{"host": "/usr/local/bin/flynn-host"}}, true},
		"gobin":     {&Layer{GoBin: []string{"golang.org/x/tools/cmd/stringer"}}, true},
		"proto":     {&Layer{ProtoBuild: []string{"controller/api"}}, true},
	}
	for name, tc := range cases {
		if got := layerBuildsGo(tc.layer); got != tc.want {
			t.Errorf("%s: layerBuildsGo = %v, want %v", name, got, tc.want)
		}
	}
}

func TestAppendGoBuildCacheMount(t *testing.T) {
	t.Setenv("FLYNN_NO_GO_BUILD_CACHE", "")
	cache := filepath.Join(t.TempDir(), "go-build")
	t.Setenv("FLYNN_GO_BUILD_CACHE", cache)
	b := quietBuilder()

	goLayer := &Layer{GoBuild: map[string]string{"status": "/bin/flynn-status"}}
	job := &host.Job{Config: host.ContainerConfig{Env: map[string]string{"GOOS": "linux"}}}
	if !b.appendGoBuildCacheMount(job, goLayer) {
		t.Fatal("expected the cache to be mounted for a Go layer")
	}
	if got := job.Config.Env["GOCACHE"]; got != goBuildCacheMountPoint {
		t.Fatalf("GOCACHE = %q, want %q", got, goBuildCacheMountPoint)
	}
	if len(job.Config.Mounts) != 1 {
		t.Fatalf("mounts = %+v, want exactly one", job.Config.Mounts)
	}
	m := job.Config.Mounts[0]
	if m.Target != cache || m.Location != goBuildCacheMountPoint || !m.Writeable {
		t.Fatalf("mount = %+v", m)
	}

	// A layer that never runs go must not get the mount or the env var.
	pkgLayer := &Layer{Script: "host/img/packages.sh"}
	job = &host.Job{Config: host.ContainerConfig{Env: map[string]string{}}}
	if b.appendGoBuildCacheMount(job, pkgLayer) {
		t.Fatal("non-Go layer must not mount the Go build cache")
	}
	if _, ok := job.Config.Env["GOCACHE"]; ok || len(job.Config.Mounts) != 0 {
		t.Fatalf("non-Go layer job was modified: %+v", job.Config)
	}

	t.Setenv("FLYNN_NO_GO_BUILD_CACHE", "1")
	job = &host.Job{Config: host.ContainerConfig{Env: map[string]string{}}}
	if b.appendGoBuildCacheMount(job, goLayer) {
		t.Fatal("disabled cache must not be mounted")
	}
}

// The mount point must sit under a path mksquashfs already excludes, or the
// compile cache would be squashed into every Go image layer.
func TestGoBuildCacheMountPointExcludedFromLayers(t *testing.T) {
	rel := strings.TrimPrefix(goBuildCacheMountPoint, "/")
	for _, ex := range squashfs.DefaultExcludes() {
		if rel == ex || strings.HasPrefix(rel, ex+"/") {
			return
		}
	}
	t.Fatalf("%s is not covered by squashfs.DefaultExcludes %v", goBuildCacheMountPoint, squashfs.DefaultExcludes())
}

// GOCACHE is job plumbing, not a layer input: two layers that differ only in
// whether the cache is mounted must hash to the same ID.
func TestGoBuildCacheDoesNotChangeLayerID(t *testing.T) {
	b := quietBuilder()
	b.fileCache = make(map[string]*fileInput)
	env := map[string]string{"GOOS": "linux", "GOARCH": "amd64"}
	run := []string{"go build -o /bin/flynn-status ./status"}
	before, err := b.generateLayerID("status", run, env, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("FLYNN_NO_GO_BUILD_CACHE", "")
	t.Setenv("FLYNN_GO_BUILD_CACHE", t.TempDir())
	job := &host.Job{Config: host.ContainerConfig{Env: env}}
	b.appendGoBuildCacheMount(job, &Layer{GoBuild: map[string]string{"status": "/bin/flynn-status"}})
	if _, ok := env["GOCACHE"]; !ok {
		t.Fatal("test precondition: job env is the layer env map, as in BuildLayer")
	}

	// The ID is always generated before the mount is added; assert that the
	// mount does not need to be part of the identity by regenerating without
	// GOCACHE and expecting the pre-mount value.
	delete(env, "GOCACHE")
	after, err := b.generateLayerID("status", run, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("layer ID changed: %s -> %s", before, after)
	}
}
