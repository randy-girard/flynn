package archive

import (
	"os"
	"strconv"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// openat2Off is set once openat2 is found missing (Linux before 5.6) or
// refused (a seccomp profile that does not know it); the walk is used from
// then on. Tests set it to run the walk on Linux too.
var openat2Off atomic.Bool

// openat2 opens the existing directory name inside the root with one
// openat2(RESOLVE_IN_ROOT): the kernel resolves absolute symlink targets
// and ".." against the root, the same as the walk. ok is false when the
// walk has to do it instead (the directory is missing, or openat2 is not
// available).
func (r *root) openat2(name string) (*dirHandle, bool) {
	if openat2Off.Load() {
		return nil, false
	}
	fd, err := unix.Openat2(r.fd, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		switch err {
		case unix.ENOSYS, unix.EPERM, unix.E2BIG:
			openat2Off.Store(true)
		}
		return nil, false
	}
	// the host path, for the calls that take one
	p, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil || !within(r.path, p) {
		unix.Close(fd)
		return nil, false
	}
	return &dirHandle{fd: fd, path: p}, true
}

// openat2Works reports whether openat2 is used (for tests).
func openat2Works() bool {
	if openat2Off.Load() {
		return false
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, "/", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_IN_ROOT})
	if err != nil {
		return false
	}
	unix.Close(fd)
	return true
}

func mknodAt(d *dirHandle, name string, mode uint32, dev uint64) error {
	return unix.Mknodat(d.fd, name, mode, int(dev))
}
