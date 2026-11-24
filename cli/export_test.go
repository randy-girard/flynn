package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	router "github.com/randy-girard/flynn/router/types"
)

// exportClient serves an app with no release, so the export holds only
// app.json and routes.json. getAppErr fails the export before any entry.
type exportClient struct {
	controller.Client
	getAppErr error
}

func (c exportClient) GetApp(name string) (*ct.App, error) {
	if c.getAppErr != nil {
		return nil, c.getAppErr
	}
	return &ct.App{ID: "app-id", Name: name}, nil
}

func (c exportClient) AppRouteList(string) ([]*router.Route, error) { return nil, nil }

func (c exportClient) GetAppRelease(string) (*ct.Release, error) {
	return nil, controller.ErrNotFound
}

func withExportApp(t *testing.T, name string) {
	prev := flagApp
	flagApp = name
	t.Cleanup(func() { flagApp = prev })
}

func exportArgs(file string) *docopt.Args {
	return &docopt.Args{
		String: map[string]string{"--file": file},
		Bool:   map[string]bool{"--quiet": true},
	}
}

func tarEntries(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var names []string
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatalf("export is not a complete tar: %s", err)
		}
		names = append(names, h.Name)
	}
}

func TestExportFileIsOwnerOnly(t *testing.T) {
	old := syscall.Umask(022)
	t.Cleanup(func() { syscall.Umask(old) })
	withExportApp(t, "demo")

	dir := t.TempDir()
	path := filepath.Join(dir, "app.tar")
	// An older export left world-readable must not keep its mode.
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runExport(exportArgs(path), exportClient{}); err != nil {
		t.Fatalf("export: %s", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0600 {
		t.Fatalf("export file mode = %o, want 0600", mode)
	}
	names := tarEntries(t, path)
	if strings.Join(names, ",") != "demo/app.json,demo/routes.json" {
		t.Fatalf("entries = %v", names)
	}
	assertNoExportTemp(t, dir)
}

func TestExportFailureKeepsExistingFile(t *testing.T) {
	withExportApp(t, "demo")
	dir := t.TempDir()
	path := filepath.Join(dir, "app.tar")
	if err := os.WriteFile(path, []byte("previous export"), 0600); err != nil {
		t.Fatal(err)
	}
	err := runExport(exportArgs(path), exportClient{getAppErr: errors.New("boom")})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the GetApp error", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "previous export" {
		t.Fatalf("failed export replaced the existing file: %q", data)
	}
	assertNoExportTemp(t, dir)
}

func assertNoExportTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "app.tar" {
			t.Fatalf("leftover file %q", e.Name())
		}
	}
}

// trailerFails accepts every write except the tar trailer (a full 512-byte
// zero block), like a disk that fills on the final flush.
type trailerFails struct{ bytes.Buffer }

var errTrailer = errors.New("no space left on device")

func (w *trailerFails) Write(p []byte) (int, error) {
	if len(p) == 512 && bytes.Count(p, []byte{0}) == 512 {
		return 0, errTrailer
	}
	return w.Buffer.Write(p)
}

func TestExportReturnsCloseError(t *testing.T) {
	withExportApp(t, "demo")
	w := &trailerFails{}
	err := exportApp(exportClient{}, w, nil, exportOptions{})
	if !errors.Is(err, errTrailer) {
		t.Fatalf("err = %v, want the tar close error", err)
	}
	if w.Len() == 0 {
		t.Fatal("expected the entries to be written before the trailer")
	}
}
