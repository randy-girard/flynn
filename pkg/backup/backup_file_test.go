package backup

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

	"github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/httpclient"
)

// fakeAttach is an attach stream that immediately reports exit status 0.
type fakeAttach struct{ r *bytes.Reader }

func newFakeAttach() *fakeAttach {
	return &fakeAttach{r: bytes.NewReader([]byte{host.AttachExit, 0, 0, 0, 0})}
}
func (f *fakeAttach) Read(p []byte) (int, error)  { return f.r.Read(p) }
func (f *fakeAttach) Write(p []byte) (int, error) { return len(p), nil }
func (f *fakeAttach) Close() error                { return nil }
func (f *fakeAttach) CloseWrite() error           { return nil }

type fakeClient struct {
	controller.Client
	backupStub
}

func (c fakeClient) GetApp(n string) (*ct.App, error) { return c.backupStub.GetApp(n) }
func (c fakeClient) GetAppRelease(i string) (*ct.Release, error) {
	return c.backupStub.GetAppRelease(i)
}
func (c fakeClient) GetFormation(a, r string) (*ct.Formation, error) {
	return c.backupStub.GetFormation(a, r)
}
func (c fakeClient) FormationList(a string) ([]*ct.Formation, error) {
	return c.backupStub.FormationList(a)
}
func (c fakeClient) GetExpandedFormation(a, r string) (*ct.ExpandedFormation, error) {
	return c.backupStub.GetExpandedFormation(a, r)
}
func (c fakeClient) AppList() ([]*ct.App, error) { return nil, nil }
func (c fakeClient) RunJobAttached(string, *ct.NewJob) (httpclient.ReadWriteCloser, error) {
	return newFakeAttach(), nil
}

func newFakeClient() fakeClient { return fakeClient{backupStub: requiredBackupApps()} }

// failAfter fails every write once n bytes have been written.
type failAfter struct {
	n   int
	err error
}

func (w *failAfter) Write(p []byte) (int, error) {
	if w.n <= 0 {
		return 0, w.err
	}
	if len(p) > w.n {
		w.n = 0
		return 0, w.err
	}
	w.n -= len(p)
	return len(p), nil
}

func withUmask(t *testing.T, m int) {
	old := syscall.Umask(m)
	t.Cleanup(func() { syscall.Umask(old) })
}

func TestCreateBackupFileIsOwnerOnly(t *testing.T) {
	withUmask(t, 022)
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.tar")
	var stderr bytes.Buffer
	if err := WriteFile(newFakeClient(), path, &stderr, nil); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	if !strings.Contains(stderr.String(), TenantDumpNotice) {
		t.Fatalf("notice missing: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Backup complete.") {
		t.Fatalf("completion missing: %q", stderr.String())
	}
	// no temp files left behind
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestWriteFileTightensExistingWorldReadableFile(t *testing.T) {
	withUmask(t, 022)
	path := filepath.Join(t.TempDir(), "backup.tar")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(newFakeClient(), path, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestWriteFileFailureKeepsExistingFileAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.tar")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	c := newFakeClient()
	delete(c.backupStub.apps, "postgres") // Run fails: required app missing
	if err := WriteFile(c, path, io.Discard, nil); err == nil {
		t.Fatal("expected error")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "old" {
		t.Fatalf("existing backup clobbered: %q", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestTarHeaderModeIsOwnerOnly(t *testing.T) {
	var buf bytes.Buffer
	tw := NewTarWriter("x", &buf, nil)
	if err := tw.WriteJSON("flynn.json", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	hdr, err := tar.NewReader(&buf).Next()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Mode != 0600 {
		t.Fatalf("tar mode = %o, want 600", hdr.Mode)
	}
}

func TestRunReturnsCloseError(t *testing.T) {
	// Count the bytes of a successful run, then fail only the trailer write.
	var ok bytes.Buffer
	if err := Run(newFakeClient(), &ok, nil); err != nil {
		t.Fatal(err)
	}
	want := errors.New("disk full")
	// Run builds names from the clock; the size is stable across names of equal length.
	w := &failAfter{n: ok.Len() - 1024, err: want}
	if err := Run(newFakeClient(), w, nil); !errors.Is(err, want) {
		t.Fatalf("Run err = %v, want %v", err, want)
	}
}

func TestTenantDumpNotice(t *testing.T) {
	if !strings.Contains(TenantDumpNotice, "backups/") || !strings.Contains(TenantDumpNotice, "cluster restore") || !strings.Contains(TenantDumpNotice, "Tenant volumes") {
		t.Fatal(TenantDumpNotice)
	}
	if strings.Contains(strings.ToLower(TenantDumpNotice), "large object") {
		t.Fatal("the appliance dump already includes large objects")
	}
}
