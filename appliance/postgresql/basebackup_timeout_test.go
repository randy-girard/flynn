package postgresql

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

// hangingBasebackup prints one progress report, then blocks like
// pg_basebackup reading from an upstream that vanished without a RST. The
// background sleep stands in for the forked WAL receiver: it holds stderr
// open, so killing only the parent leaves cmd.Wait blocked.
const hangingBasebackup = `echo "  100/9769845 kB (0%), 0/1 tablespace" >&2
sleep 3600 &
wait
`

func initializedStandbyDataDir(t *testing.T) string {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "db")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "PG_VERSION"), []byte("16\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dataDir
}

func withBasebackupStallTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := basebackupStallTimeout
	basebackupStallTimeout = d
	t.Cleanup(func() { basebackupStallTimeout = prev })
}

// TestPullBasebackupStalledUpstreamTimesOut covers NEW-38: the sync peer's
// pg_basebackup kept reading from a primary whose host rebooted mid-copy. The
// TCP connection never saw a RST and the reseed held p.mtx for 31 minutes.
func TestPullBasebackupStalledUpstreamTimesOut(t *testing.T) {
	withBasebackupStallTimeout(t, 500*time.Millisecond)
	dataDir := initializedStandbyDataDir(t)
	p := NewProcess(Config{ID: "sync-1", BinDir: writeFakeBasebackup(t, hangingBasebackup), DataDir: dataDir, Port: "6509"})
	up := &discoverd.Instance{Addr: "100.100.89.14:5432", Meta: map[string]string{IDKey: "dead-primary"}}

	errCh := make(chan error, 1)
	go func() { errCh <- p.pullBasebackup(p.log, up) }()
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "no progress") {
			t.Fatalf("expected a stalled basebackup error, got %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("pg_basebackup against an unreachable upstream never timed out")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "PG_VERSION")); err != nil {
		t.Fatal("a timed-out basebackup must keep the existing data directory")
	}
	if _, err := os.Stat(p.basebackupStagingDir()); !os.IsNotExist(err) {
		t.Fatalf("staging directory must be removed after a timeout, stat err=%v", err)
	}
	if p.basebackup.Load() != nil {
		t.Fatal("basebackup progress must be cleared after a timeout")
	}
}

// TestBasebackupStallTimeoutStopsAtFullProgress keeps a slow final fsync of a
// large data directory from being mistaken for a dead upstream.
func TestBasebackupStallTimeoutStopsAtFullProgress(t *testing.T) {
	withBasebackupStallTimeout(t, 300*time.Millisecond)
	script := `echo "  9769845/9769845 kB (100%), 1/1 tablespace" >&2
sleep 1
mkdir -p "$2" && echo 16 > "$2/PG_VERSION"
`
	p := NewProcess(Config{ID: "sync-1", BinDir: writeFakeBasebackup(t, script), DataDir: initializedStandbyDataDir(t), Port: "6510"})
	up := &discoverd.Instance{Addr: "127.0.0.1:1", Meta: map[string]string{IDKey: "primary"}}
	if err := p.pullBasebackup(p.log, up); err != nil {
		t.Fatalf("a basebackup that has received every byte must not be killed while it syncs: %v", err)
	}
}

// TestBasebackupConnInfoDetectsDeadUpstream checks the libpq settings that
// make pg_basebackup itself fail fast when the upstream host disappears.
func TestBasebackupConnInfoDetectsDeadUpstream(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	script := `for a in "$@"; do echo "$a"; done > ` + argsFile + `
mkdir -p "$2" && echo 16 > "$2/PG_VERSION"
`
	p := NewProcess(Config{ID: "sync-1", BinDir: writeFakeBasebackup(t, script), DataDir: initializedStandbyDataDir(t), Port: "6511"})
	up := &discoverd.Instance{Addr: "127.0.0.1:1", Meta: map[string]string{IDKey: "primary"}}
	if err := p.pullBasebackup(p.log, up); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	var dbname string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "host=") {
			dbname = line
		}
	}
	for _, want := range []string{"connect_timeout=", "keepalives=1", "keepalives_idle=", "keepalives_interval=", "keepalives_count=", "tcp_user_timeout="} {
		if !strings.Contains(dbname, want) {
			t.Errorf("pg_basebackup conninfo is missing %q", want)
		}
	}
}

// TestReconfigureCancelsBasebackupFromSupersededUpstream covers the other half
// of NEW-38: sirenia had already moved the sync to the new primary, but
// assumeSync/applyConfig waited on p.mtx behind a reseed from the dead one.
func TestReconfigureCancelsBasebackupFromSupersededUpstream(t *testing.T) {
	withBasebackupStallTimeout(t, time.Hour)
	p := NewProcess(Config{ID: "sync-1", BinDir: writeFakeBasebackup(t, hangingBasebackup), DataDir: initializedStandbyDataDir(t), Port: "6512"})
	dead := peerInst("100.100.89.14:5432", "old-primary")
	live := peerInst("100.100.80.97:5432", "new-primary")
	p.setConfig(&state.Config{Role: state.RoleSync, Upstream: dead})
	p.setNeedsReseed(true)

	reseedDone := make(chan struct{})
	go func() {
		p.reseedAfterWALRecycle()
		close(reseedDone)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for p.basebackup.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("basebackup never started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	next := &state.Config{Role: state.RoleSync, Upstream: live}
	errCh := make(chan error, 1)
	go func() { errCh <- p.Reconfigure(next) }()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("reconfigure: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Reconfigure to a new upstream stayed blocked behind a basebackup from the old one")
	}
	select {
	case <-reseedDone:
	case <-time.After(5 * time.Second):
		t.Fatal("reseed from the superseded upstream did not return")
	}
	if got := p.config(); got == nil || got.Upstream == nil || got.Upstream.Addr != live.Addr {
		t.Fatalf("expected the new upstream to be configured, got %+v", got)
	}
	if !p.needsReseed() {
		t.Fatal("a canceled reseed must leave needsReseed set so the next start reseeds from the new upstream")
	}
}

func TestBasebackupSuperseded(t *testing.T) {
	up := peerInst("100.100.89.14:5432", "primary")
	moved := peerInst("100.100.89.19:5432", "primary")
	other := peerInst("100.100.80.97:5432", "other")
	for _, tc := range []struct {
		name string
		next *state.Config
		want bool
	}{
		{"nil config", nil, false},
		{"same upstream", &state.Config{Role: state.RoleSync, Upstream: peerInst("100.100.89.14:5432", "primary")}, false},
		{"async keeps upstream", &state.Config{Role: state.RoleAsync, Upstream: up}, false},
		{"standby without upstream is rejected later", &state.Config{Role: state.RoleSync}, false},
		{"different peer", &state.Config{Role: state.RoleSync, Upstream: other}, true},
		{"same id at new address", &state.Config{Role: state.RoleSync, Upstream: moved}, true},
		{"promoted", &state.Config{Role: state.RolePrimary}, true},
		{"deposed", &state.Config{Role: state.RoleNone}, true},
	} {
		if got := basebackupSuperseded(up, tc.next); got != tc.want {
			t.Errorf("%s: basebackupSuperseded = %v, want %v", tc.name, got, tc.want)
		}
	}
	if basebackupSuperseded(nil, &state.Config{Role: state.RolePrimary}) {
		t.Error("no basebackup in flight is never superseded")
	}
}
