//go:build !windows
// +build !windows

package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// maxSymlinks is how many symlinks one lookup follows, the limit Linux
// applies (ELOOP after 40).
const maxSymlinks = 40

// root is the directory a tar is unpacked into. Every path of the tar is
// looked up inside it the way the kernel looks it up in a container whose
// root is this directory: an absolute symlink target starts again at the
// root, ".." stops at the root, and nothing on the host outside the root is
// ever opened, created, changed or removed (EDGE-106).
//
// Lookups go through directory file descriptors: each directory is opened
// with O_NOFOLLOW relative to its parent, a symlink is read with readlinkat
// and its target is walked the same way. On Linux 5.6 and later the lookup
// of an existing directory is a single openat2 with RESOLVE_IN_ROOT, which
// the kernel confines the same way.
type root struct {
	path string // the root on the host, symlinks resolved
	fd   int

	// the parent directory of the last entry, reused while entries stay in
	// the same directory. An entry only changes its own name inside that
	// directory, which cannot change how the directory itself resolves.
	lastName string
	last     *dirHandle
}

// dirHandle is an open directory inside the root.
type dirHandle struct {
	fd int
	// path is the directory's host path without symlinks under the root;
	// it is only used for the few calls that have no *at form (lsetxattr,
	// and mknod on macOS).
	path string
}

func (d *dirHandle) close() { unix.Close(d.fd) }

// join is the host path of name in d.
func (d *dirHandle) join(name string) string { return d.path + "/" + name }

func openRoot(path string) (*root, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return &root{path: path, fd: fd}, nil
}

func (r *root) close() {
	r.dropCache()
	unix.Close(r.fd)
}

func (r *root) dropCache() {
	if r.last != nil {
		r.last.close()
		r.last, r.lastName = nil, ""
	}
}

// dir returns the directory name (a clean path relative to the root),
// creating missing directories inside the root. The result belongs to r and
// stays valid until the next call.
func (r *root) dir(name string) (*dirHandle, error) {
	if r.last != nil && r.lastName == name {
		return r.last, nil
	}
	r.dropCache()
	d, err := r.lookup(name, true)
	if err != nil {
		return nil, err
	}
	r.last, r.lastName = d, name
	return d, nil
}

// lookup opens the directory name inside the root. With create, missing
// directories are made (mode 0777 less the umask, as os.MkdirAll). The
// caller closes the result.
func (r *root) lookup(name string, create bool) (*dirHandle, error) {
	if d, ok := r.openat2(name); ok {
		return d, nil
	}
	return r.walk(name, create)
}

type walkLevel struct {
	fd   int
	name string
}

// walk is the portable lookup: one component at a time from the root
// directory's descriptor, never opening "..".
func (r *root) walk(name string, create bool) (*dirHandle, error) {
	var stack []walkLevel // the directories below the root, outermost first
	closeAll := func() {
		for _, l := range stack {
			unix.Close(l.fd)
		}
		stack = nil
	}
	fail := func(op string, err error) (*dirHandle, error) {
		closeAll()
		return nil, &os.PathError{Op: op, Path: name, Err: err}
	}
	pending := splitPath(name)
	links := 0
	justMade := false
	for len(pending) > 0 {
		c := pending[0]
		pending = pending[1:]
		switch c {
		case ".":
			continue
		case "..":
			if n := len(stack); n > 0 {
				unix.Close(stack[n-1].fd)
				stack = stack[:n-1]
			}
			continue
		}
		cur := r.fd
		if n := len(stack); n > 0 {
			cur = stack[n-1].fd
		}
		fd, err := unix.Openat(cur, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err == nil {
			stack = append(stack, walkLevel{fd, c})
			justMade = false
			continue
		}
		var st unix.Stat_t
		if serr := unix.Fstatat(cur, c, &st, unix.AT_SYMLINK_NOFOLLOW); serr != nil {
			if serr == unix.ENOENT && create && !justMade {
				if err := unix.Mkdirat(cur, c, 0777); err != nil && err != unix.EEXIST {
					return fail("mkdir", err)
				}
				justMade = true
				pending = append([]string{c}, pending...)
				continue
			}
			return fail("open", serr)
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFLNK:
			if links++; links > maxSymlinks {
				return fail("open", unix.ELOOP)
			}
			target, err := readlinkat(cur, c)
			if err != nil {
				return fail("readlink", err)
			}
			if strings.HasPrefix(target, "/") {
				closeAll()
			}
			pending = append(splitPath(target), pending...)
		case unix.S_IFDIR:
			// a directory that cannot be opened (permissions)
			return fail("open", err)
		default:
			return fail("open", unix.ENOTDIR)
		}
	}
	names := make([]string, 0, len(stack))
	for _, l := range stack {
		names = append(names, l.name)
	}
	if len(stack) == 0 {
		fd, err := unix.FcntlInt(uintptr(r.fd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return fail("dup", err)
		}
		return &dirHandle{fd: fd, path: r.path}, nil
	}
	top := stack[len(stack)-1].fd
	for _, l := range stack[:len(stack)-1] {
		unix.Close(l.fd)
	}
	return &dirHandle{fd: top, path: r.path + "/" + strings.Join(names, "/")}, nil
}

// within reports whether p is root or below it.
func within(root, p string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

func readlinkat(dirfd int, name string) (string, error) {
	for size := 256; ; size *= 2 {
		buf := make([]byte, size)
		n, err := unix.Readlinkat(dirfd, name, buf)
		if err != nil {
			return "", err
		}
		if n < size {
			return string(buf[:n]), nil
		}
	}
}

// splitPath splits a path inside the root (or a symlink target) into its
// components.
func splitPath(p string) []string {
	var out []string
	for _, c := range strings.Split(p, "/") {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// entryName turns a tar entry name into a clean path relative to the root:
// an absolute name is taken from the root ("/etc/x" is etc/x), a name that
// leaves the root with ".." is refused. The root itself is ".".
func entryName(name, dest string) (string, error) {
	n := filepath.Clean(name)
	if filepath.IsAbs(n) {
		// Clean already dropped any ".." right after the "/"
		n = strings.TrimLeft(n, "/")
		if n == "" {
			n = "."
		}
	} else if n == ".." || strings.HasPrefix(n, "../") {
		return "", fmt.Errorf("archive: %q is outside of %q", name, dest)
	}
	return n, nil
}

// removeAllAt removes name in dirfd and, for a directory, everything below
// it, without following symlinks. A missing name is not an error.
func removeAllAt(dirfd int, name string) error {
	err := unix.Unlinkat(dirfd, name, 0)
	if err == nil || err == unix.ENOENT {
		return nil
	}
	var st unix.Stat_t
	if serr := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); serr != nil {
		if serr == unix.ENOENT {
			return nil
		}
		return serr
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return err
	}
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := removeAllAt(fd, n); err != nil {
			return err
		}
	}
	if err := unix.Unlinkat(dirfd, name, unix.AT_REMOVEDIR); err != nil && err != unix.ENOENT {
		return err
	}
	return nil
}

// readDirNames lists the directory d with a descriptor of its own (a dup
// would share d's directory offset).
func readDirNames(d *dirHandle) ([]string, error) {
	fd, err := unix.Openat(d.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), d.path)
	defer f.Close()
	return f.Readdirnames(-1)
}

func timespec(t time.Time) unix.Timespec {
	if t.IsZero() {
		return unix.Timespec{Sec: 0, Nsec: utimeOmit}
	}
	return unix.NsecToTimespec(t.UnixNano())
}

// isPermission is true for the errors the unpack ignores when it is not
// allowed to set an owner, mode or time (macOS, user namespaces).
func isPermission(err error) bool {
	return err == unix.EPERM || err == unix.EACCES
}
