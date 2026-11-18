package backup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

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
		err = writeFileAtomic(path, func(w io.Writer) error {
			return Run(client, w, progress)
		})
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(stderr, "Backup complete.")
	return nil
}

func writeFileAtomic(path string, write func(io.Writer) error) error {
	// A device or pipe (for example /dev/stdout) cannot be renamed over.
	if fi, err := os.Stat(path); err == nil && !fi.Mode().IsRegular() {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
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
