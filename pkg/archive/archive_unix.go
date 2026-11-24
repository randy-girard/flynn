//go:build !windows
// +build !windows

package archive

import (
	"archive/tar"
	"os"
	"strings"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

type whiteoutConverter interface {
	ConvertRead(hdr *tar.Header, d *dirHandle, base string) (bool, error)
}

// overlayWhiteoutConverter turns Docker whiteouts into overlayfs ones (a
// 0:0 character device, the opaque xattr), for layers mounted as overlay
// lower directories.
type overlayWhiteoutConverter struct{}

func (c overlayWhiteoutConverter) ConvertRead(hdr *tar.Header, d *dirHandle, base string) (bool, error) {
	// if a directory is marked as opaque by the AUFS special file, we need to translate that to overlay
	if base == whiteoutOpaqueDir {
		err := unix.Fsetxattr(d.fd, "trusted.overlay.opaque", []byte{'y'}, 0)
		if err != nil {
			return false, errors.Wrapf(err, "setxattr(%q, trusted.overlay.opaque=y)", d.path)
		}
		// don't write the file itself
		return false, err
	}

	// if a file was deleted and we are using overlay, we need to create a character device
	if strings.HasPrefix(base, whiteoutPrefix) {
		originalBase := base[len(whiteoutPrefix):]

		if err := mknodAt(d, originalBase, unix.S_IFCHR, 0); err != nil {
			return false, errors.Wrapf(err, "failed to mknod(%q, S_IFCHR, 0)", d.join(originalBase))
		}
		if err := unix.Fchownat(d.fd, originalBase, hdr.Uid, hdr.Gid, unix.AT_SYMLINK_NOFOLLOW); err != nil && !isPermission(err) {
			return false, err
		}

		// don't write the file itself
		return false, nil
	}

	return true, nil
}

// flatWhiteoutConverter applies Docker layer whiteouts when merging layers into
// a plain directory tree (as opposed to preparing an overlay mount).
type flatWhiteoutConverter struct{}

func (flatWhiteoutConverter) ConvertRead(hdr *tar.Header, d *dirHandle, base string) (bool, error) {
	if base == whiteoutOpaqueDir {
		names, err := readDirNames(d)
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, err
		}
		for _, name := range names {
			if name == base {
				continue
			}
			if err := removeAllAt(d.fd, name); err != nil {
				return false, &os.PathError{Op: "remove", Path: d.join(name), Err: err}
			}
		}
		return false, nil
	}

	if strings.HasPrefix(base, whiteoutMetaPrefix) {
		return false, nil
	}

	if strings.HasPrefix(base, whiteoutPrefix) {
		original := base[len(whiteoutPrefix):]
		if err := removeAllAt(d.fd, original); err != nil {
			return false, &os.PathError{Op: "remove", Path: d.join(original), Err: err}
		}
		return false, nil
	}

	return true, nil
}

// handleTarTypeBlockCharFifo is an OS-specific helper function used by
// createTarFile to handle the following types of header: Block; Char; Fifo.
// created is false when mknod was not permitted (an unprivileged unpack
// skips device nodes).
func handleTarTypeBlockCharFifo(hdr *tar.Header, d *dirHandle, base string) (created bool, err error) {
	mode := uint32(hdr.Mode & 07777)
	switch hdr.Typeflag {
	case tar.TypeBlock:
		mode |= unix.S_IFBLK
	case tar.TypeChar:
		mode |= unix.S_IFCHR
	case tar.TypeFifo:
		mode |= unix.S_IFIFO
	}

	if err := mknodAt(d, base, mode, unix.Mkdev(uint32(hdr.Devmajor), uint32(hdr.Devminor))); err != nil {
		if isPermission(err) {
			return false, nil
		}
		return false, &os.PathError{Op: "mknod", Path: hdr.Name, Err: err}
	}
	return true, nil
}
