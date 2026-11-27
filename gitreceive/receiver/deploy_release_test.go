package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/dockerimage"
)

// BUILD-15: deployContainer builds its build resources, release meta and
// extra processes with the same helpers as the flynn.yml path, and neither
// deploy path writes into the previous release's Meta map.

func TestDeployContainerUsesSharedResourceHelpers(t *testing.T) {
	body, err := os.ReadFile("flynn-receive.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	fn := functionBody(src, "deployContainer")
	if fn == "" {
		t.Fatal("missing deployContainer")
	}
	for _, call := range []string{"containerBuildResources(prevRelease)", "dockerfileRelease("} {
		if !strings.Contains(fn, call) {
			t.Errorf("deployContainer must call %s", call)
		}
	}
	for _, dup := range []string{`Processes["dockerbuilder"]`, "prevRelease.Meta", "DOCKERBUILDER_DEFAULT_MEMORY_LIMIT", "ParseLimit("} {
		if strings.Contains(fn, dup) {
			t.Errorf("deployContainer still duplicates the flynn.yml helpers (%s)", dup)
		}
	}
	rel := functionBody(src, "dockerfileRelease")
	for _, call := range []string{"containerReleaseMeta(", "containerExtraProcesses("} {
		if !strings.Contains(rel, call) {
			t.Errorf("dockerfileRelease must call %s", call)
		}
	}
	if fn := functionBody(src, "deployBuildpack"); strings.Contains(fn, "Meta:        prevRelease.Meta") || strings.Contains(fn, "release.Meta = prevRelease.Meta") {
		t.Error("deployBuildpack still writes into the previous release's Meta map")
	}
}

// legacyDockerfileRelease is the release the inline deployContainer code
// built before BUILD-15 (with prev.Meta copied first, so the comparison does
// not depend on the aliasing it had).
func legacyDockerfileRelease(appName string, prev *ct.Release, artifact *ct.Artifact, releaseEnv, meta map[string]string) *ct.Release {
	releaseMeta := map[string]string{}
	for k, v := range prev.Meta {
		releaseMeta[k] = v
	}
	for k, v := range meta {
		releaseMeta[k] = v
	}
	releaseMeta["git"] = "true"
	releaseMeta["slugrunner.stack"] = stackContainer
	extra := map[string]ct.ProcessType{}
	if db, ok := prev.Processes["dockerbuilder"]; ok {
		extra["dockerbuilder"] = db
	}
	return dockerimage.NewAppReleaseFromArtifact(appName, prev, artifact, dockerimage.ReleaseOptions{
		Env:                releaseEnv,
		Meta:               releaseMeta,
		ProcessName:        dockerimage.GitProcessName(prev),
		ServiceHealthCheck: true,
		ExtraProcesses:     extra,
	})
}

func legacyDockerfileResources(prev *ct.Release) resource.Resources {
	resources := resource.Defaults()
	if limit, err := resource.ParseLimit(resource.TypeTempDisk, "4G"); err == nil {
		resources[resource.TypeTempDisk] = resource.Spec{Limit: &limit, Request: &limit}
	}
	if db, ok := prev.Processes["dockerbuilder"]; ok && db.Resources != nil {
		resources = db.Resources
	} else if rawLimit := os.Getenv("DOCKERBUILDER_DEFAULT_MEMORY_LIMIT"); rawLimit != "" {
		if limit, err := resource.ParseLimit(resource.TypeMemory, rawLimit); err == nil {
			resources[resource.TypeMemory] = resource.Spec{Limit: &limit, Request: &limit}
		}
	}
	return resources
}

func mustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func deployFixtures() []*ct.Release {
	mem := int64(512 * 1024 * 1024)
	return []*ct.Release{
		{},
		{Meta: map[string]string{"git": "true", "keep": "me", "slugrunner.stack": "heroku-24"}},
		{
			Meta: map[string]string{"rev": "old"},
			Processes: map[string]ct.ProcessType{
				"app":           {Args: []string{"/bin/app"}},
				"dockerbuilder": {Resources: resource.Resources{resource.TypeMemory: resource.Spec{Limit: &mem, Request: &mem}}},
			},
		},
		{Processes: map[string]ct.ProcessType{"web": {Args: []string{"/bin/web"}}, "dockerbuilder": {}}},
	}
}

func TestDockerfileReleaseIsUnchanged(t *testing.T) {
	artifact := &ct.Artifact{ID: "artifact-1", Type: ct.ArtifactTypeFlynn, Meta: map[string]string{"docker-receive.expose": "8080"}}
	env := map[string]string{"A": "1"}
	meta := map[string]string{"rev": "new", "git.branch": "main"}
	for i, prev := range deployFixtures() {
		before := mustJSON(t, prev)
		want := mustJSON(t, legacyDockerfileRelease("app", prev, artifact, env, meta))
		got := mustJSON(t, dockerfileRelease("app", prev, artifact, env, meta))
		if got != want {
			t.Errorf("fixture %d: release\n%s\nwant\n%s", i, got, want)
		}
		if after := mustJSON(t, prev); after != before {
			t.Errorf("fixture %d: the previous release changed:\n%s\nwas\n%s", i, after, before)
		}
		for _, limit := range []string{"", "1G"} {
			t.Setenv("DOCKERBUILDER_DEFAULT_MEMORY_LIMIT", limit)
			if got, want := mustJSON(t, containerBuildResources(prev)), mustJSON(t, legacyDockerfileResources(prev)); got != want {
				t.Errorf("fixture %d limit %q: resources %s, want %s", i, limit, got, want)
			}
		}
	}
}

func TestSlugReleaseDoesNotWriteIntoPreviousMeta(t *testing.T) {
	prev := &ct.Release{Meta: map[string]string{"keep": "me", "rev": "old"}}
	before := mustJSON(t, prev)
	rel := slugRelease("app", prev, "runner", "slug", map[string]string{"A": "1"}, map[string]string{"rev": "new"}, "heroku-24", map[string]string{"slugbuilder.process_types": "web"})
	if after := mustJSON(t, prev); after != before {
		t.Fatalf("the previous release changed:\n%s\nwas\n%s", after, before)
	}
	wantMeta := map[string]string{"keep": "me", "rev": "new", "slugrunner.stack": "heroku-24"}
	if mustJSON(t, rel.Meta) != mustJSON(t, wantMeta) {
		t.Fatalf("meta = %v, want %v", rel.Meta, wantMeta)
	}
	if strings.Join(rel.ArtifactIDs, ",") != "runner,slug" || rel.Env["A"] != "1" {
		t.Fatalf("release = %+v", rel)
	}
	if _, ok := rel.Processes["web"]; !ok {
		t.Fatalf("processes = %v", rel.Processes)
	}
	if rel := slugRelease("app", &ct.Release{}, "runner", "slug", nil, nil, "heroku-24", nil); rel.Meta["slugrunner.stack"] != "heroku-24" {
		t.Fatalf("meta of a release with no previous meta = %v", rel.Meta)
	}
}
