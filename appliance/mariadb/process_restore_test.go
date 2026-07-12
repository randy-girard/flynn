package mariadb

import (
	"os"
	"path/filepath"
	"strings"
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
