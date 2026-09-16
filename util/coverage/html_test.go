package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBlockLineAndAreas(t *testing.T) {
	name, b, err := parseBlockLine("github.com/flynn/flynn/pkg/plugin/manifest.go:96.32,117.2 8 1")
	if err != nil {
		t.Fatal(err)
	}
	if name != "github.com/flynn/flynn/pkg/plugin/manifest.go" || b.StartLine != 96 || b.EndLine != 117 || b.NumStmt != 8 || b.Count != 1 {
		t.Fatalf("%s %+v", name, b)
	}
	area, pkg := splitAreaPackage("pkg/plugin/manifest.go")
	if area != "pkg" || pkg != "pkg/plugin" {
		t.Fatalf("area=%s pkg=%s", area, pkg)
	}
	area, pkg = splitAreaPackage("cli/main.go")
	if area != "cli" || pkg != "cli" {
		t.Fatalf("area=%s pkg=%s", area, pkg)
	}
}

func TestWriteHTMLPerFileAndIndexSections(t *testing.T) {
	root := repoRoot(t)
	dir := t.TempDir()
	profile := filepath.Join(dir, "coverage.out")
	data := "mode: atomic\n" +
		"github.com/flynn/flynn/pkg/plugin/manifest.go:96.32,117.2 8 1\n" +
		"github.com/flynn/flynn/pkg/plugin/catalog.go:16.48,23.2 2 0\n" +
		"github.com/flynn/flynn/cli/plugin_catalog.go:18.43,24.2 2 1\n"
	if err := os.WriteFile(profile, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "html")
	files, err := parseProfile(profile, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("files=%d", len(files))
	}
	r := buildReport(files)
	if len(r.Areas) != 2 {
		t.Fatalf("areas=%v", areaNames(r))
	}
	if r.Areas[0].Name != "cli" || r.Areas[1].Name != "pkg" {
		t.Fatalf("area order %v", areaNames(r))
	}
	if err := writeHTML(r, out); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(index)
	for _, want := range []string{
		"Overall by package area",
		`id="pkg"`,
		`id="cli"`,
		"pkg/plugin/manifest.go",
		"files/pkg/plugin/manifest.go.html",
		"files/cli/plugin_catalog.go.html",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("index missing %q", want)
		}
	}
	page := filepath.Join(out, "files", "pkg", "plugin", "manifest.go.html")
	body, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	if !strings.Contains(src, "func (m *Manifest) Validate()") {
		t.Fatalf("file page missing source: %s", page)
	}
	if !strings.Contains(src, `href="../../../index.html"`) {
		t.Fatal("file page must link back to the index")
	}
	if !strings.Contains(src, `class="cov"`) {
		t.Fatal("covered lines must be marked")
	}
	catalog, err := os.ReadFile(filepath.Join(out, "files", "pkg", "plugin", "catalog.go.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(catalog), `class="uncov"`) {
		t.Fatal("uncovered lines must be marked")
	}
}

func areaNames(r Report) []string {
	out := make([]string, len(r.Areas))
	for i, a := range r.Areas {
		out[i] = a.Name
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
