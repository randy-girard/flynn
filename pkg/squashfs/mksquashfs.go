// Package squashfs holds the flags Flynn uses when writing image and slug
// layers. Keep shell call sites (ubuntu-noble.sh, busybox.sh, build.sh) in
// sync with Compression / CompressionLevel.
package squashfs

const (
	// Compression is the mksquashfs -comp algorithm for cluster images.
	Compression = "zstd"
	// CompressionLevel is passed as -Xcompression-level.
	CompressionLevel = "15"
	// MemLimit caps mksquashfs cache memory (same as builder/run.go).
	MemLimit = "1G"
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
