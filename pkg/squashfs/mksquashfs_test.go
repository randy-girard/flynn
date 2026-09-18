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

func TestLayerArgsFasterZstd(t *testing.T) {
	got := LayerArgs("/extract", "/layer.squashfs", "-processors", ProcessorCount(8))
	want := []string{
		"/extract",
		"/layer.squashfs",
		"-noappend",
		"-comp", "zstd",
		"-Xcompression-level", "6",
		"-processors", "4",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LayerArgs = %v, want %v", got, want)
	}
}

func TestProcessorCount(t *testing.T) {
	if ProcessorCount(0) != "1" {
		t.Fatalf("0 = %s", ProcessorCount(0))
	}
	if ProcessorCount(2) != "2" {
		t.Fatalf("2 = %s", ProcessorCount(2))
	}
	if ProcessorCount(16) != "4" {
		t.Fatalf("16 = %s", ProcessorCount(16))
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
