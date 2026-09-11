package squashfs

import (
	"reflect"
	"strings"
	"testing"
)

func TestArgsZstd(t *testing.T) {
	got := Args("/mnt/diff", "/mnt/out/layer.squashfs", "-mem", MemLimit, "-ef", "/dev/stdin")
	want := []string{
		"/mnt/diff",
		"/mnt/out/layer.squashfs",
		"-noappend",
		"-comp", "zstd",
		"-Xcompression-level", "15",
		"-mem", MemLimit,
		"-ef", "/dev/stdin",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Args = %v, want %v", got, want)
	}
}

func TestDefaultExcludesDropDocsAndApt(t *testing.T) {
	joined := strings.Join(DefaultExcludes(), "\n")
	for _, p := range []string{"usr/share/doc", "usr/share/man", "var/cache/apt", "tmp"} {
		if !strings.Contains(joined, p) {
			t.Errorf("DefaultExcludes missing %q", p)
		}
	}
}
