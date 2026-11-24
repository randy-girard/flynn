//go:build !windows
// +build !windows

// Package archive unpacks Docker layer tars.
//
// Every entry is confined to the destination directory (EDGE-106). Paths
// in the tar are resolved the way the kernel resolves them in a container
// whose root is the destination: an absolute symlink target starts again
// at the destination and ".." stops there, so x -> /some/dir followed by
// x/file writes dest/some/dir/file. Symlinks are kept as they are in the
// layer (/bin/sh -> /bin/busybox, or a link that points above the root):
// a link is data and only matters when something resolves it, and the
// unpack resolves every path inside the destination. Code that reads the
// unpacked tree afterwards must resolve paths in it the same way (see
// pkg/dockerimage resolveInRoot), never with the host's own lookup.
//
// An entry replaces an existing file or symlink (it is removed first and
// the new node is created with O_EXCL, never written through), owner, mode
// and times are set without following a symlink, and a hardlink must name
// a file inside the destination. Device nodes, FIFOs and setuid bits are
// kept (base images have them; mknod is skipped when not permitted).
package archive

import (
	"archive/tar"
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func createTarFile(r *root, d *dirHandle, base string, hdr *tar.Header, reader io.Reader, lchown bool) error {
	// the permission bits with setuid, setgid and sticky
	mode := uint32(hdr.Mode & 07777)

	switch hdr.Typeflag {
	case tar.TypeDir:
		// Create directory unless it exists as a directory already.
		// In that case we just want to merge the two
		var st unix.Stat_t
		if err := unix.Fstatat(d.fd, base, &st, unix.AT_SYMLINK_NOFOLLOW); !(err == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR) {
			if err := unix.Mkdirat(d.fd, base, mode); err != nil {
				if isPermission(err) {
					err = unix.Mkdirat(d.fd, base, 0755)
				}
				if err != nil {
					return &os.PathError{Op: "mkdir", Path: hdr.Name, Err: err}
				}
			}
		}

	case tar.TypeReg, tar.TypeRegA:
		// any old node was removed: O_EXCL with O_NOFOLLOW never opens
		// an existing file or symlink
		fd, err := unix.Openat(d.fd, base, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode&0777)
		if err != nil {
			return &os.PathError{Op: "open", Path: hdr.Name, Err: err}
		}
		file := os.NewFile(uintptr(fd), hdr.Name)
		if _, err := io.Copy(file, reader); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}

	case tar.TypeBlock, tar.TypeChar, tar.TypeFifo:
		created, err := handleTarTypeBlockCharFifo(hdr, d, base)
		if err != nil {
			return err
		}
		if !created {
			return nil
		}

	case tar.TypeLink:
		// the target is an earlier entry of the archive, looked up inside
		// the root like any other path; the link itself is not followed
		target, err := entryName(hdr.Linkname, r.path)
		if err != nil || target == "." {
			return fmt.Errorf("archive: invalid hardlink %q -> %q", hdr.Name, hdr.Linkname)
		}
		td, err := r.lookup(filepath.Dir(target), false)
		if err != nil {
			return fmt.Errorf("archive: hardlink %q -> %q: %w", hdr.Name, hdr.Linkname, err)
		}
		err = unix.Linkat(td.fd, filepath.Base(target), d.fd, base, 0)
		td.close()
		if err != nil {
			return fmt.Errorf("archive: hardlink %q -> %q: %w", hdr.Name, hdr.Linkname, err)
		}

	case tar.TypeSymlink:
		// kept as it is, whatever it points to: every lookup through it is
		// resolved inside the root
		if err := unix.Symlinkat(hdr.Linkname, d.fd, base); err != nil {
			return &os.PathError{Op: "symlink", Path: hdr.Name, Err: err}
		}

	default:
		return fmt.Errorf("unhandled tar header type %d", hdr.Typeflag)
	}

	// On macOS and in user namespaces, changing ownership to the values in
	// the layer tar may not be permitted.
	if lchown {
		if err := unix.Fchownat(d.fd, base, hdr.Uid, hdr.Gid, unix.AT_SYMLINK_NOFOLLOW); err != nil && !isPermission(err) {
			return &os.PathError{Op: "lchown", Path: hdr.Name, Err: err}
		}
	}

	for key, value := range hdr.Xattrs {
		// d.path has no symlinks below the root and lsetxattr does not
		// follow the last component
		if err := unix.Lsetxattr(d.join(base), key, []byte(value), 0); err != nil {
			if err == syscall.ENOTSUP || err == syscall.EPERM {
				// We ignore errors here because not all graphdrivers support
				// xattrs *cough* old versions of AUFS *cough*. However only
				// ENOTSUP should be emitted in that case, otherwise we still
				// bail.
				// EPERM occurs if modifying xattrs is not allowed. This can
				// happen when running in userns with restrictions (ChromeOS).
				continue
			}
			return err
		}
	}

	if hdr.Typeflag == tar.TypeLink {
		// the inode already has the mode and times of the entry it was
		// made from (the old code skipped them too)
		return nil
	}

	// There is no lchmod on Linux, so a symlink keeps its mode. This must
	// happen after chown, as that can modify the mode.
	var st unix.Stat_t
	if err := unix.Fstatat(d.fd, base, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &os.PathError{Op: "lstat", Path: hdr.Name, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFLNK {
		// not a symlink, so this does not follow one
		if err := unix.Fchmodat(d.fd, base, mode, 0); err != nil && !isPermission(err) {
			return &os.PathError{Op: "chmod", Path: hdr.Name, Err: err}
		}
	}

	aTime := hdr.AccessTime
	if aTime.Before(hdr.ModTime) {
		// Last access time should never be before last modified time.
		aTime = hdr.ModTime
	}
	return setTimes(d, base, aTime, hdr.ModTime)
}

// setTimes sets the times of name in d without following a symlink.
func setTimes(d *dirHandle, name string, atime, mtime time.Time) error {
	ts := []unix.Timespec{timespec(atime), timespec(mtime)}
	if err := unix.UtimesNanoAt(d.fd, name, ts, unix.AT_SYMLINK_NOFOLLOW); err != nil && !isPermission(err) && err != unix.ENOSYS {
		return &os.PathError{Op: "utimes", Path: d.join(name), Err: err}
	}
	return nil
}

func Unpack(decompressedArchive io.Reader, dest string, lchown bool) error {
	return unpack(decompressedArchive, dest, lchown, overlayWhiteoutConverter{})
}

// UnpackFlat extracts a Docker layer tar into dest, merging it with any
// existing files. Whiteout entries delete files from dest instead of creating
// overlay character devices (which requires privileges unavailable on macOS).
func UnpackFlat(decompressedArchive io.Reader, dest string, lchown bool) error {
	return unpack(decompressedArchive, dest, lchown, flatWhiteoutConverter{})
}

func unpack(decompressedArchive io.Reader, dest string, lchown bool, whiteoutConverter whiteoutConverter) error {
	dest = filepath.Clean(dest)
	if resolved, err := filepath.EvalSymlinks(dest); err == nil {
		dest = resolved
	}
	r, err := openRoot(dest)
	if err != nil {
		return err
	}
	defer r.close()

	tr := tar.NewReader(decompressedArchive)
	trBuf := bufio.NewReaderSize(nil, 32*1024)

	var dirs []*tar.Header

	// Iterate through the files in the archive.
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			// end of tar archive
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}

		name, err := entryName(hdr.Name, dest)
		if err != nil {
			return err
		}
		if name == "." {
			// the root itself stays as it is
			continue
		}
		hdr.Name = name
		base := path.Base(name)

		// the parent directory, resolved (and created if missing) inside
		// the root
		d, err := r.dir(path.Dir(name))
		if err != nil {
			return fmt.Errorf("archive: %s: %w", name, err)
		}

		// If path exits we almost always just want to remove and replace it
		// The only exception is when it is a directory *and* the file from
		// the layer is also a directory. Then we want to merge them (i.e.
		// just apply the metadata from the layer).
		var st unix.Stat_t
		if err := unix.Fstatat(d.fd, base, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
			if !(st.Mode&unix.S_IFMT == unix.S_IFDIR && hdr.Typeflag == tar.TypeDir) {
				if err := removeAllAt(d.fd, base); err != nil {
					return &os.PathError{Op: "remove", Path: name, Err: err}
				}
			}
		}
		trBuf.Reset(tr)

		writeFile, err := whiteoutConverter.ConvertRead(hdr, d, base)
		if err != nil {
			return err
		}
		if !writeFile {
			continue
		}

		if err := createTarFile(r, d, base, hdr, trBuf, lchown); err != nil {
			return err
		}

		// Directory mtimes must be handled at the end to avoid further
		// file creation in them to modify the directory mtime
		if hdr.Typeflag == tar.TypeDir {
			dirs = append(dirs, hdr)
		}
	}

	for _, hdr := range dirs {
		// looked up again: a later entry may have replaced the directory
		// (with a symlink, say), and times are never set through a link
		d, err := r.lookup(path.Dir(hdr.Name), false)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("archive: %s: %w", hdr.Name, err)
		}
		err = setTimes(d, path.Base(hdr.Name), hdr.AccessTime, hdr.ModTime)
		d.close()
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
	}
	return nil
}
