package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/mrunalp/fileutils"
)

// collapseConsecutiveLowerDirs removes adjacent duplicate paths from a
// lowerdir list. Docker images sometimes reference the same layer blob more
// than once; repeating the same path in lowerdir can confuse overlayfs.
func collapseConsecutiveLowerDirs(paths []string) []string {
	if len(paths) == 0 {
		return paths
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if len(out) > 0 && out[len(out)-1] == p {
			continue
		}
		out = append(out, p)
	}
	return out
}

// dedupeLowerDirPaths removes duplicate paths, keeping only the topmost
// (leftmost) occurrence of each path in the overlay stack.
func dedupeLowerDirPaths(paths []string) []string {
	if len(paths) == 0 {
		return paths
	}
	first := make(map[string]int, len(paths))
	for i := len(paths) - 1; i >= 0; i-- {
		if _, ok := first[paths[i]]; !ok {
			first[paths[i]] = i
		}
	}
	out := make([]string, 0, len(first))
	for i, p := range paths {
		if first[p] == i {
			out = append(out, p)
		}
	}
	return out
}

// buildOverlayLowerdir merges squashfs layer mount points into a single plain
// directory. Each step uses a two-layer overlay mount and then copies the
// merged view to disk so later steps never stack overlay on overlay (which
// triggers ELOOP for images with many Docker layers).
//
// lowers must be ordered for overlay lowerdir (leftmost entry is the topmost
// layer). scratch is a writable directory with enough space for the merged
// image (typically the per-job path under /var/lib/flynn/image/tmp).
func buildOverlayLowerdir(lowers []string, scratch string) (string, error) {
	lowers = dedupeLowerDirPaths(collapseConsecutiveLowerDirs(lowers))
	if len(lowers) == 0 {
		return "", fmt.Errorf("no overlay layers")
	}
	if len(lowers) == 1 {
		return lowers[0], nil
	}

	cur := filepath.Join(scratch, ".materialized")
	if err := os.RemoveAll(cur); err != nil {
		return "", err
	}
	if err := fileutils.CopyDirectory(lowers[len(lowers)-1], cur); err != nil {
		return "", fmt.Errorf("copying base layer: %s", err)
	}

	for i := len(lowers) - 2; i >= 0; i-- {
		stageDir := filepath.Join(scratch, fmt.Sprintf(".overlay-stage-%d", i))
		upperDir := filepath.Join(scratch, fmt.Sprintf(".overlay-stage-%d-upper", i))
		workDir := filepath.Join(scratch, fmt.Sprintf(".overlay-stage-%d-work", i))
		for _, dir := range []string{stageDir, upperDir, workDir} {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return "", err
			}
		}
		if err := mountOverlay(lowers[i]+":"+cur, upperDir, workDir, stageDir); err != nil {
			return "", fmt.Errorf("mounting layer %d overlay: %s", i, err)
		}

		next := filepath.Join(scratch, fmt.Sprintf(".materialized-%d", i))
		if err := os.RemoveAll(next); err != nil {
			unmountOverlay(stageDir)
			return "", err
		}
		if err := fileutils.CopyDirectory(stageDir, next); err != nil {
			unmountOverlay(stageDir)
			return "", fmt.Errorf("materializing layer %d: %s", i, err)
		}
		if err := unmountOverlay(stageDir); err != nil {
			return "", err
		}
		os.RemoveAll(cur)
		os.RemoveAll(upperDir)
		os.RemoveAll(workDir)
		os.RemoveAll(stageDir)
		cur = next
	}
	return cur, nil
}

func mountOverlay(lowerdir, upperdir, workdir, mountPoint string) error {
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lowerdir, upperdir, workdir)
	return syscall.Mount("overlay", mountPoint, "overlay", 0, opts)
}

func unmountOverlay(mountPoint string) error {
	err := syscall.Unmount(mountPoint, 0)
	if err == syscall.EINVAL || err == syscall.ENOENT {
		return nil
	}
	return err
}
