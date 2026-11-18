package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Loading a package's inputs must list every file once at every level of the
// import graph. A diamond-shaped graph used to multiply shared dependencies
// (36M entries for discoverd/server), which OOM-killed flynn-builder.
func TestGoInputsListsEachFileOnce(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(filepath.Dir(thisFile), "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	g := NewGoInputs(GoPlatform{OS: "linux", Arch: "arm64"})
	if _, err := g.Load("discoverd"); err != nil {
		t.Fatal(err)
	}
	for pkg, files := range g.inputs {
		seen := make(map[string]struct{}, len(files))
		for _, f := range files {
			seen[f] = struct{}{}
		}
		if len(seen) != len(files) {
			t.Fatalf("%s lists %d files but only %d are distinct", pkg, len(files), len(seen))
		}
	}
}
