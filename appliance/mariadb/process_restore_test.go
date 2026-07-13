package mariadb

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testRestoreProcess(t *testing.T) *Process {
	t.Helper()
	p := NewProcess()
	p.DataDir = t.TempDir()
	p.Port = "3307"
	p.ServerID = 42
	p.ID = "test-node"
	return p
}

// TestClearDataDirRemovesMyCnf documents why Restore must rewrite my.cnf after
// extracting a backup: clearDataDir intentionally wipes the entire data
// directory, including any config written before restore begins.
func TestClearDataDirRemovesMyCnf(t *testing.T) {
	p := testRestoreProcess(t)
	if err := p.writeConfig(configData{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.ConfigPath()); err != nil {
		t.Fatalf("expected my.cnf before clearDataDir: %v", err)
	}
	if err := p.clearDataDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.ConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("expected my.cnf removed by clearDataDir, stat err=%v", err)
	}
}

// TestRestoreFinalizeWritesReadOnlyMyCnf guards the post-restore contract:
// after backup files are on disk, my.cnf must exist with read_only set so
// mariadbd can start as a standby.
func TestRestoreFinalizeWritesReadOnlyMyCnf(t *testing.T) {
	p := testRestoreProcess(t)
	if err := p.clearDataDir(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.DataDir, "ibdata1"), []byte("restored"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.writeConfig(configData{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(data)
	if !strings.Contains(cfg, "read_only = 1") {
		t.Fatalf("standby my.cnf missing read_only:\n%s", cfg)
	}
	if !strings.Contains(cfg, "datadir = "+p.DataDir) {
		t.Fatalf("standby my.cnf missing datadir:\n%s", cfg)
	}
}

func TestClearDataDirCreatesMissingDirectory(t *testing.T) {
	p := testRestoreProcess(t)
	if err := os.RemoveAll(p.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := p.clearDataDir(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("clearDataDir should create missing data directory")
	}
}

func TestDataDirInitialized(t *testing.T) {
	p := testRestoreProcess(t)
	if p.dataDirInitialized() {
		t.Fatal("expected empty data dir to be uninitialized")
	}
	if err := os.WriteFile(filepath.Join(p.DataDir, "ibdata1"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if !p.dataDirInitialized() {
		t.Fatal("expected ibdata1 to mark data dir initialized")
	}
}

func TestDataDirInitializedMySQLDir(t *testing.T) {
	p := testRestoreProcess(t)
	mysqlDir := filepath.Join(p.DataDir, "mysql")
	if err := os.MkdirAll(mysqlDir, 0700); err != nil {
		t.Fatal(err)
	}
	if !p.dataDirInitialized() {
		t.Fatal("expected mysql system dir to mark data dir initialized")
	}
}

// newTestBackupReadCloser builds a backupReadCloser wired exactly like
// Process.Backup: backupMtx is held and unlock is backupMtx.Unlock. The wrapped
// command exits 0 and writes the expected "completed OK!" trailer to stderr.
func newTestBackupReadCloser(t *testing.T, mtx *sync.Mutex) *backupReadCloser {
	t.Helper()
	cmd := exec.Command("sh", "-c", "echo 'completed OK!' 1>&2")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	r := &backupReadCloser{cmd: cmd, stdout: stdout, unlock: mtx.Unlock}
	cmd.Stderr = &r.stderr
	mtx.Lock()
	if err := cmd.Start(); err != nil {
		mtx.Unlock()
		t.Fatal(err)
	}
	return r
}

// TestBackupReadCloserCloseIdempotent guards against a fatal
// "sync: unlock of unlocked mutex" crash: handleGetBackup calls Close more than
// once, so Close must release backupMtx exactly once and be safe to call again.
func TestBackupReadCloserCloseIdempotent(t *testing.T) {
	var mtx sync.Mutex
	r := newTestBackupReadCloser(t, &mtx)

	// Calling Close repeatedly (as the HTTP handler does) must not panic and
	// must return the same nil result each time.
	for i := 0; i < 3; i++ {
		if err := r.Close(); err != nil {
			t.Fatalf("Close call %d returned error: %v", i, err)
		}
	}

	// The mutex must have been unlocked exactly once. A second unlock would
	// have already crashed the process; TryLock confirms it is currently
	// unlocked (i.e. released precisely one time).
	if !mtx.TryLock() {
		t.Fatal("backupMtx still locked after Close; unlock did not run")
	}
	mtx.Unlock()
}

// TestBackupReadCloserCloseRunsSideEffectsOnce ensures the wrapped command is
// waited on only once even when Close is invoked multiple times.
func TestBackupReadCloserCloseRunsSideEffectsOnce(t *testing.T) {
	var mtx sync.Mutex
	r := newTestBackupReadCloser(t, &mtx)

	if err := r.Close(); err != nil {
		t.Fatalf("first Close returned error: %v", err)
	}
	// A second Wait on the same Cmd would return an error; a guarded Close must
	// not re-run it and must keep returning the cached (nil) result.
	if err := r.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}
}
