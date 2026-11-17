package main

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/appmanifest"
)

func TestBlueprintPlaceholdersAndScaleHold(t *testing.T) {
	app := &ct.App{Name: "shop-web", Meta: map[string]string{
		appmanifest.MetaBlueprint:     appmanifest.StatusPending,
		appmanifest.MetaBlueprintEnv:  "shop",
		appmanifest.MetaBlueprintSlot: "web",
	}}
	if !blueprintPending(app) {
		t.Fatal("a pending blueprint app holds web scale until seed exits 0")
	}
	if blueprintPending(&ct.App{Name: "preview", Meta: map[string]string{"flynn-pipeline": "pipe"}}) {
		t.Fatal("a preview app is not a blueprint app")
	}
	ph := receivePlaceholders(app, "from-arg", map[string]string{"git.branch": "topic", "rev": "deadbeef"})
	if ph["base"] != "shop" || ph["slot"] != "web" || ph["app"] != "shop-web" || ph["sha"] != "deadbeef" || ph["branch"] != "topic" {
		t.Fatalf("%v", ph)
	}
	ph = receivePlaceholders(app, "from-arg", map[string]string{"git.commit": "abc123", "rev": "other"})
	if ph["sha"] != "abc123" {
		t.Fatalf("git.commit wins: %q", ph["sha"])
	}
	ph = receivePlaceholders(app, "from-arg", nil)
	if ph["sha"] != "from-arg" {
		t.Fatalf("rev argument: %q", ph["sha"])
	}
}
