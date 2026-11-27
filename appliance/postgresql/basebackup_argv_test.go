package postgresql

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/discoverd/client"
)

// TestBasebackupKeepsPasswordOffArgv (NEW-40): pg_basebackup must get the
// replication password from PGPASSWORD, never from the --dbname conninfo on
// argv, which every host user can read with ps or /proc/<pid>/cmdline.
func TestBasebackupKeepsPasswordOffArgv(t *testing.T) {
	rec := t.TempDir()
	script := `for a in "$@"; do echo "$a"; done > ` + rec + `/argv
printf '%s' "${PGPASSWORD:-}" > ` + rec + `/pw
mkdir -p "$2" && echo 16 > "$2/PG_VERSION"
`
	const pw = "repl-argv-secret-40"
	t.Setenv("PGPASSWORD", "stale-from-parent")
	p := NewProcess(Config{ID: "sync-1", BinDir: writeFakeBasebackup(t, script), DataDir: initializedStandbyDataDir(t), Port: "6514", Password: pw})
	up := &discoverd.Instance{Addr: "127.0.0.1:1", Meta: map[string]string{IDKey: "primary"}}
	if err := p.pullBasebackup(p.log, up); err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(filepath.Join(rec, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(argv), pw) || strings.Contains(string(argv), "password=") {
		t.Fatalf("pg_basebackup argv carries the password:\n%s", argv)
	}
	if !strings.Contains(string(argv), "user=flynn") {
		t.Fatalf("argv lost the conninfo:\n%s", argv)
	}
	got, _ := os.ReadFile(filepath.Join(rec, "pw"))
	if string(got) != pw {
		t.Fatalf("PGPASSWORD = %q, want the replication password", got)
	}
}
