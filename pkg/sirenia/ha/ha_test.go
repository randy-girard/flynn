package ha

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestNeedsEnvFlip(t *testing.T) {
	pg := &ct.Release{Env: map[string]string{"SIRENIA_PROCESS": "postgres", "SINGLETON": "true"}}
	if !NeedsEnvFlip(pg, map[string]int{"postgres": 1, "web": 1}) {
		t.Fatal("running singleton postgres must need an env flip")
	}
	if NeedsEnvFlip(pg, map[string]int{"postgres": 0, "web": 1}) {
		t.Fatal("unprovisioned singleton must not be flipped")
	}
	haRel := &ct.Release{Env: map[string]string{"SIRENIA_PROCESS": "postgres", "SINGLETON": "false"}}
	if NeedsEnvFlip(haRel, map[string]int{"postgres": 1}) {
		t.Fatal("HA release must not need an env flip")
	}
	if NeedsEnvFlip(nil, map[string]int{"postgres": 1}) {
		t.Fatal("nil release must not need an env flip")
	}
}

func TestNeedsScale(t *testing.T) {
	haRel := &ct.Release{Env: map[string]string{"SIRENIA_PROCESS": "mongodb", "SINGLETON": "false"}}
	if !NeedsScale(haRel, map[string]int{"mongodb": 1}) {
		t.Fatal("HA mongodb at scale 1 must need scale-up")
	}
	if NeedsScale(haRel, map[string]int{"mongodb": 3}) {
		t.Fatal("already-HA mongodb must not need scale-up")
	}
	if NeedsScale(haRel, map[string]int{"mongodb": 0}) {
		t.Fatal("unprovisioned mongodb must not be scaled")
	}
	single := &ct.Release{Env: map[string]string{"SIRENIA_PROCESS": "mongodb", "SINGLETON": "true"}}
	if NeedsScale(single, map[string]int{"mongodb": 1}) {
		t.Fatal("singleton must env-flip before scale-up")
	}
}

func TestHAProcesses(t *testing.T) {
	r := &ct.Release{Env: map[string]string{"SIRENIA_PROCESS": "mariadb"}}
	got := HAProcesses(r, map[string]int{"mariadb": 1, "web": 1})
	if got["mariadb"] != DataCount || got["web"] != WebCount {
		t.Fatalf("got %#v", got)
	}
	mongo := HAProcesses(
		&ct.Release{Env: map[string]string{"SIRENIA_PROCESS": "mongodb"}},
		map[string]int{"mongodb": 1},
	)
	if mongo["mongodb"] != DataCount {
		t.Fatalf("mongodb scale=%d", mongo["mongodb"])
	}
	if _, ok := mongo["web"]; ok {
		t.Fatal("must not invent a web process")
	}
}

func TestCloneWithoutSingleton(t *testing.T) {
	src := &ct.Release{
		ID:          "old",
		AppID:       "app-1",
		ArtifactIDs: []string{"art-1"},
		Env:         map[string]string{"SIRENIA_PROCESS": "postgres", "SINGLETON": "true", "PGPASSWORD": "x"},
		Meta:        map[string]string{"k": "v"},
	}
	got := CloneWithoutSingleton(src)
	if got.ID != "" {
		t.Fatalf("clone must drop ID, got %q", got.ID)
	}
	if got.Env["SINGLETON"] != "false" {
		t.Fatalf("SINGLETON=%q", got.Env["SINGLETON"])
	}
	if got.Env["PGPASSWORD"] != "x" || got.Env["SIRENIA_PROCESS"] != "postgres" {
		t.Fatalf("clone dropped env: %#v", got.Env)
	}
	if src.Env["SINGLETON"] != "true" {
		t.Fatal("clone must not mutate the source env")
	}
	if got.AppID != "app-1" || got.Meta["k"] != "v" || got.ArtifactIDs[0] != "art-1" {
		t.Fatalf("clone missing fields: %#v", got)
	}
	got.ArtifactIDs[0] = "mutated"
	if src.ArtifactIDs[0] != "art-1" {
		t.Fatal("clone must not share ArtifactIDs")
	}
}
