//go:build !linux && !windows
// +build !linux,!windows

package archive

import (
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// openat2Off has no effect here: there is no openat2, the walk does every
// lookup.
var openat2Off atomic.Bool

func (r *root) openat2(name string) (*dirHandle, bool) { return nil, false }

func openat2Works() bool { return false }

// mknodAt creates name in d. There is no mknodat here: the path is d's
// host path, which has no symlinks below the root (the walk built it), and
// mknod does not follow the last component.
func mknodAt(d *dirHandle, name string, mode uint32, dev uint64) error {
	return unix.Mknod(d.join(name), mode, int(dev))
}
