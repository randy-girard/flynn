package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyFlynnReplaceLine(t *testing.T) {
	mod := "module github.com/acme/plugin\n\ngo 1.24.0\n"
	got := applyFlynnReplaceLine(mod, "/src/flynn")
	if !strings.Contains(got, "replace github.com/randy-girard/flynn => /src/flynn") {
		t.Fatalf("missing replace: %s", got)
	}
	again := applyFlynnReplaceLine(got, "/other/flynn")
	if strings.Count(again, "replace github.com/randy-girard/flynn") != 1 {
		t.Fatalf("duplicate replace: %s", again)
	}
	if !strings.Contains(again, "=> /other/flynn") {
		t.Fatalf("did not rewrite replace: %s", again)
	}
}

func TestReplacePluginFlynnModuleRestores(t *testing.T) {
	plugin := t.TempDir()
	flynn := t.TempDir()
	if err := os.WriteFile(filepath.Join(flynn, "go.mod"), []byte("module github.com/randy-girard/flynn\n\ngo 1.24.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	orig := "module github.com/acme/plugin\n\ngo 1.24.0\nrequire github.com/randy-girard/flynn v0.0.0\n"
	if err := os.WriteFile(filepath.Join(plugin, "go.mod"), []byte(orig), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin, "go.sum"), []byte("github.com/randy-girard/flynn v0.0.0 h1:abc\n"), 0644); err != nil {
		t.Fatal(err)
	}

	restore, err := replacePluginFlynnModule(plugin, flynn)
	if err != nil {
		t.Fatal(err)
	}
	if restore == nil {
		t.Fatal("expected restore")
	}
	got, err := os.ReadFile(filepath.Join(plugin, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "replace github.com/randy-girard/flynn => "+filepath.ToSlash(flynn)) {
		t.Fatalf("go.mod=%s", got)
	}
	restore()
	back, err := os.ReadFile(filepath.Join(plugin, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != orig {
		t.Fatalf("restore go.mod=%s", back)
	}
}

func TestFlynnRootForPluginSibling(t *testing.T) {
	ws := t.TempDir()
	flynn := filepath.Join(ws, "flynn")
	plugin := filepath.Join(ws, "flynn-plugin-postgres")
	if err := os.MkdirAll(flynn, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(plugin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flynn, "go.mod"), []byte("module github.com/randy-girard/flynn\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLYNN_ROOT", "")
	t.Setenv(EnvImagesJSON, "")
	if got := flynnRootForPlugin(plugin); got != flynn {
		t.Fatalf("got %q want %q", got, flynn)
	}
}

func TestFlynnSourceRootFromEnv(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/randy-girard/flynn\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLYNN_ROOT", root)
	t.Setenv(EnvImagesJSON, "")
	if FlynnSourceRoot() != root {
		t.Fatalf("got %q want %q", FlynnSourceRoot(), root)
	}

	bogus := t.TempDir()
	t.Setenv("FLYNN_ROOT", bogus)
	got := FlynnSourceRoot()
	if got == bogus {
		t.Fatal("non-Flynn FLYNN_ROOT must be ignored")
	}
}

func TestFlynnRootFromImagesJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/randy-girard/flynn\n"), 0644); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(root, "build", "images.json")
	if err := os.MkdirAll(filepath.Dir(img), 0755); err != nil {
		t.Fatal(err)
	}
	if got := flynnRootFromImagesJSON(img); got != root {
		t.Fatalf("got %q want %q", got, root)
	}
	if flynnRootFromImagesJSON("/etc/flynn/images.json") != "" {
		t.Fatal("cluster images.json is not a Flynn checkout")
	}
}
