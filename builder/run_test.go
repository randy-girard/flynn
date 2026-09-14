package main

import (
	"io"
	"strings"
	"testing"

	"github.com/flynn/flynn/pkg/squashfs"
)

// TestMksquashfsCommandCapsMemory guards against mksquashfs reverting to its
// default of 25% of physical RAM per process. With concurrent layer builds
// (APPS_CONCURRENCY=nproc) that exhausts the builder and mksquashfs fails with
// "Write failed because Cannot allocate memory".
func TestMksquashfsCommandCapsMemory(t *testing.T) {
	excludes := []string{".container-diff", "src", "out"}
	cmd := mksquashfsCommand("/mnt/diff", "/mnt/out/layer.squashfs", excludes)

	args := cmd.Args
	if len(args) < 3 || args[0] != "mksquashfs" || args[1] != "/mnt/diff" || args[2] != "/mnt/out/layer.squashfs" {
		t.Fatalf("unexpected argv prefix: %v", args)
	}

	flag := func(name string) (string, bool) {
		for i, a := range args {
			if a == name {
				if i+1 < len(args) {
					return args[i+1], true
				}
				return "", true
			}
		}
		return "", false
	}

	if v, ok := flag("-mem"); !ok || v != mksquashfsMem {
		t.Errorf("-mem = %q (present=%v), want %q", v, ok, mksquashfsMem)
	}
	if _, ok := flag("-mem-percent"); ok {
		t.Errorf("-mem-percent must not be used alongside -mem: %v", args)
	}
	if _, ok := flag("-noappend"); !ok {
		t.Errorf("-noappend missing: %v", args)
	}
	if v, ok := flag("-comp"); !ok || v != squashfs.Compression {
		t.Errorf("-comp = %q (present=%v), want %q", v, ok, squashfs.Compression)
	}
	if v, ok := flag("-Xcompression-level"); !ok || v != squashfs.CompressionLevel {
		t.Errorf("-Xcompression-level = %q (present=%v), want %q", v, ok, squashfs.CompressionLevel)
	}
	if v, ok := flag("-ef"); !ok || v != "/dev/stdin" {
		t.Errorf("-ef = %q (present=%v), want /dev/stdin", v, ok)
	}

	if cmd.Stdin == nil {
		t.Fatal("excludes must be fed via stdin")
	}
	got, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(excludes, "\n"); string(got) != want {
		t.Errorf("stdin excludes = %q, want %q", got, want)
	}
}

func TestMksquashfsDefaultExcludesDropDocs(t *testing.T) {
	joined := strings.Join(squashfs.DefaultExcludes(), "\n")
	for _, p := range []string{"usr/share/doc", "var/cache/apt", "tmp"} {
		if !strings.Contains(joined, p) {
			t.Errorf("DefaultExcludes missing %q", p)
		}
	}
}
