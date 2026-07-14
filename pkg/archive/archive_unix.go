// +build !windows

package archive

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

type overlayWhiteoutConverter struct{}

func (c overlayWhiteoutConverter) ConvertRead(hdr *tar.Header, path string) (bool, error) {
	base := filepath.Base(path)
	dir := filepath.Dir(path)

	// if a directory is marked as opaque by the AUFS special file, we need to translate that to overlay
	if base == whiteoutOpaqueDir {
		err := unix.Setxattr(dir, "trusted.overlay.opaque", []byte{'y'}, 0)
		if err != nil {
			return false, errors.Wrapf(err, "setxattr(%q, trusted.overlay.opaque=y)", dir)
		}
		// don't write the file itself
		return false, err
	}

	// if a file was deleted and we are using overlay, we need to create a character device
	if strings.HasPrefix(base, whiteoutPrefix) {
		originalBase := base[len(whiteoutPrefix):]
		originalPath := filepath.Join(dir, originalBase)

		if err := unix.Mknod(originalPath, unix.S_IFCHR, 0); err != nil {
			return false, errors.Wrapf(err, "failed to mknod(%q, S_IFCHR, 0)", originalPath)
		}
		if err := os.Chown(originalPath, hdr.Uid, hdr.Gid); err != nil && !os.IsPermission(err) {
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

func (flatWhiteoutConverter) ConvertRead(hdr *tar.Header, path string) (bool, error) {
	base := filepath.Base(path)
	dir := filepath.Dir(path)

	if base == whiteoutOpaqueDir {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, err
		}
		for _, entry := range entries {
			if entry.Name() == base {
				continue
			}
			if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
				return false, err
			}
		}
		return false, nil
	}

	if strings.HasPrefix(base, whiteoutMetaPrefix) {
		return false, nil
	}

	if strings.HasPrefix(base, whiteoutPrefix) {
		originalPath := filepath.Join(dir, base[len(whiteoutPrefix):])
		if err := os.RemoveAll(originalPath); err != nil && !os.IsNotExist(err) {
			return false, err
		}
		return false, nil
	}

	return true, nil
}

// handleTarTypeBlockCharFifo is an OS-specific helper function used by
// createTarFile to handle the following types of header: Block; Char; Fifo
func handleTarTypeBlockCharFifo(hdr *tar.Header, path string) error {
	mode := uint32(hdr.Mode & 07777)
	switch hdr.Typeflag {
	case tar.TypeBlock:
		mode |= unix.S_IFBLK
	case tar.TypeChar:
		mode |= unix.S_IFCHR
	case tar.TypeFifo:
		mode |= unix.S_IFIFO
	}

	if err := unix.Mknod(path, mode, int(unix.Mkdev(uint32(hdr.Devmajor), uint32(hdr.Devminor)))); err != nil {
		if os.IsPermission(err) {
			return nil
		}
		return err
	}
	return nil
}

func handleLChmod(hdr *tar.Header, path string, hdrInfo os.FileInfo) error {
	if hdr.Typeflag == tar.TypeLink {
		if fi, err := os.Lstat(hdr.Linkname); err == nil && (fi.Mode()&os.ModeSymlink == 0) {
			if err := os.Chmod(path, hdrInfo.Mode()); err != nil && !os.IsPermission(err) {
				return err
			}
		}
	} else if hdr.Typeflag != tar.TypeSymlink {
		if err := os.Chmod(path, hdrInfo.Mode()); err != nil && !os.IsPermission(err) {
			return err
		}
	}
	return nil
}
