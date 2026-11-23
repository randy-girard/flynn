package backup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/randy-girard/flynn/controller/client"
)

// TenantDumpNotice is printed for every cluster backup. Tenant dumps live in
// the blobstore and are not part of the tarball.
const TenantDumpNotice = "Tenant database dumps are in the blobstore under backups/ and are not applied by cluster restore. Tenant volumes are not in this tarball."

// WriteFile creates a cluster backup at path (stdout when path is empty).
// The tarball holds the cluster root secrets, so the file is owner-only: it
// is written to a 0600 temp file beside path, synced and closed with the
// errors checked, then renamed into place. A failed backup never replaces an
// existing file. progress may be nil.
func WriteFile(client controller.Client, path string, stderr io.Writer, progress ProgressBar) error {
	fmt.Fprintln(stderr, "Creating cluster backup...")
	fmt.Fprintln(stderr, TenantDumpNotice)
	var err error
	if path == "" {
		err = Run(client, os.Stdout, progress)
	} else {
		err = WriteFileAtomic(path, func(w io.Writer) error {
			return Run(client, w, progress)
		})
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(stderr, "Backup complete.")
	return nil
}

// WriteFileAtomic writes a file that may hold secrets (a cluster backup, an
// app export). write gets a 0600 temp file beside path; the temp file is then
// synced, closed and renamed over path, with every error returned. When write
// or any later step fails, the temp file is removed and an existing file at
// path is left as it was. A path that names a device, a pipe, or an open fd
// (/dev/stdout, /dev/fd/N, /proc/self/fd/N) is written in place instead.
func WriteFileAtomic(path string, write func(io.Writer) error) error {
	// A device or pipe (for example /dev/stdout) cannot be renamed over, and
	// an fd path (/dev/stdout redirected to a file) resolves to a regular file
	// whose directory is /dev. Both are written in place, as os.Create did.
	if fi, err := os.Stat(path); err == nil && (!fi.Mode().IsRegular() || isFdPath(path)) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			return err
		}
		if err := write(f); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}

	// CreateTemp opens with O_CREATE|O_EXCL and mode 0600.
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	done := false
	defer func() {
		if !done {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := write(f); err != nil {
		return err
	}
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	done = true
	return nil
}

// isFdPath reports whether path names an already open file through /dev
// (/dev/stdout, /dev/fd/N) or /proc (/proc/self/fd/N).
func isFdPath(path string) bool {
	clean := filepath.Clean(path)
	return strings.HasPrefix(clean, "/dev/") || strings.HasPrefix(clean, "/proc/")
}
