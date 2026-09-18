// Package squashfs holds the flags Flynn uses when writing image and slug
// layers. Keep shell call sites (ubuntu-noble.sh, busybox.sh, build.sh) in
// sync with Compression / CompressionLevel.
package squashfs

import (
	"strconv"
)

const (
	// Compression is the mksquashfs -comp algorithm for cluster images.
	Compression = "zstd"
	// CompressionLevel is passed as -Xcompression-level for cluster image
	// layers (builder). Higher is smaller and slower.
	CompressionLevel = "15"
	// LayerCompressionLevel is used for app/slug layers uploaded through
	// tarreceive. zstd 6 is much faster than 15 with only a modest size
	// increase — git-push Docker deploys spend a lot of time here.
	LayerCompressionLevel = "6"
	// MemLimit caps mksquashfs cache memory (same as builder/run.go).
	MemLimit = "1G"
	// MaxLayerProcessors caps mksquashfs threads on tarreceive so a squash
	// does not starve the host.
	MaxLayerProcessors = 4
)

// DefaultExcludes are overlay-diff paths that must not land in a layer.
// Paths are relative to the mksquashfs source directory.
func DefaultExcludes() []string {
	return []string{
		".container-diff",
		".container-shared",
		".containerconfig",
		".containerinit",
		"etc/hosts",
		"src",
		"out",
		"usr/share/doc",
		"usr/share/man",
		"usr/share/info",
		"usr/share/lintian",
		"var/cache/apt",
		"var/lib/apt/lists",
		"tmp",
		"root/.cache",
	}
}

// Args returns mksquashfs arguments after the binary name: src, dst, then
// Flynn's compression flags, then any extra flags (e.g. -mem, -ef, -processors).
func Args(src, dst string, extra ...string) []string {
	args := []string{
		src,
		dst,
		"-noappend",
		"-comp", Compression,
		"-Xcompression-level", CompressionLevel,
	}
	return append(args, extra...)
}

// LayerArgs is Args for app/slug layers: faster zstd, same extra-flag pattern.
func LayerArgs(src, dst string, extra ...string) []string {
	args := []string{
		src,
		dst,
		"-noappend",
		"-comp", Compression,
		"-Xcompression-level", LayerCompressionLevel,
	}
	return append(args, extra...)
}

// ProcessorCount is the -processors value for tarreceive mksquashfs.
func ProcessorCount(ncpu int) string {
	if ncpu < 1 {
		ncpu = 1
	}
	if ncpu > MaxLayerProcessors {
		ncpu = MaxLayerProcessors
	}
	return strconv.Itoa(ncpu)
}
