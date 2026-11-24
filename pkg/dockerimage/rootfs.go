package dockerimage

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// maxSymlinkHops is how many symlinks resolveInRoot follows for one path,
// the limit Linux applies to a lookup (ELOOP after 40).
const maxSymlinkHops = 40

// resolveInRoot returns the host path of imagePath inside the image rootfs
// at root, following symlinks the way the kernel does in a container whose
// root is root: an absolute target starts again at root, ".." stops at root,
// and a link in any directory on the way is followed too. Nothing is
// resolved against the host's own filesystem, so an image link such as
// /bin/sh -> /etc/passwd names root/etc/passwd. The last component is
// resolved as well; the returned path is not a symlink. A missing component
// is an os.IsNotExist error, more than maxSymlinkHops links ELOOP.
//
// root itself must be a trusted host path; the result is only meaningful
// while nothing else changes the tree.
func resolveInRoot(root, imagePath string) (string, os.FileInfo, error) {
	var resolved []string // symlink-free components under root
	pending := splitImagePath(imagePath)
	hops := 0
	var fi os.FileInfo
	for len(pending) > 0 {
		c := pending[0]
		pending = pending[1:]
		if fi != nil && !fi.IsDir() {
			// a file used as a directory
			return "", nil, &os.PathError{Op: "resolve", Path: imagePath, Err: syscall.ENOTDIR}
		}
		switch c {
		case ".":
			continue
		case "..":
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
			// the parent of a resolved directory is a directory
			fi = nil
			continue
		}
		host := filepath.Join(root, filepath.Join(append(resolved, c)...))
		st, err := os.Lstat(host)
		if err != nil {
			return "", nil, err
		}
		if st.Mode()&os.ModeSymlink == 0 {
			resolved = append(resolved, c)
			fi = st
			continue
		}
		if hops++; hops > maxSymlinkHops {
			return "", nil, &os.PathError{Op: "resolve", Path: imagePath, Err: syscall.ELOOP}
		}
		target, err := os.Readlink(host)
		if err != nil {
			return "", nil, err
		}
		if strings.HasPrefix(target, "/") {
			resolved = resolved[:0]
		}
		// the link's parent is the last resolved directory
		fi = nil
		pending = append(splitImagePath(target), pending...)
	}
	host := filepath.Join(root, filepath.Join(resolved...))
	if fi == nil {
		st, err := os.Lstat(host)
		if err != nil {
			return "", nil, err
		}
		fi = st
	}
	return host, fi, nil
}

// splitImagePath splits a path (or symlink target) inside an image into its
// components. Image paths always use "/".
func splitImagePath(p string) []string {
	var out []string
	for _, c := range strings.Split(p, "/") {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}
