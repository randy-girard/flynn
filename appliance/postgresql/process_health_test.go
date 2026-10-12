package postgresql

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/sirenia/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

func TestSkipStandbyHealthWhenUpstreamDown(t *testing.T) {
	running := &client.Status{Database: &client.DatabaseInfo{Running: true}}
	stopped := &client.Status{Database: &client.DatabaseInfo{Running: false}}
	empty := &client.Status{}

	tests := []struct {
		name   string
		status *client.Status
		err    error
		want   bool
	}{
		{"dial error", running, errors.New("connection refused"), true},
		{"nil status", nil, nil, true},
		{"nil database", empty, nil, true},
		{"database not running", stopped, nil, true},
		{"database running", running, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := skipStandbyHealthWhenUpstreamDown(tc.status, tc.err)
			if got != tc.want {
				t.Fatalf("skipStandbyHealthWhenUpstreamDown() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPostgresStandbyLooksHealthy(t *testing.T) {
	tests := []struct {
		name     string
		receiver string
		start    string
		current  string
		want     bool
	}{
		{"one streaming sample is not healthy", "streaming", "0/1", "0/1", false},
		{"streaming with stuck LSN is not advancing", "streaming", "0/1", "0/1", false},
		{"advancing LSN without streaming is leftover local replay", "", "0/100", "0/200", false},
		{"advancing LSN while streaming", "streaming", "0/100", "0/200", true},
		{"stuck receiver and LSN", "", "0/100", "0/100", false},
		{"empty start then same current is not yet advancing", "", "", "0/100", false},
		{"empty both", "", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := postgresStandbyLooksHealthy(tc.receiver, tc.start, tc.current)
			if got != tc.want {
				t.Fatalf("postgresStandbyLooksHealthy(%q, %q, %q) = %v, want %v",
					tc.receiver, tc.start, tc.current, got, tc.want)
			}
		})
	}
}

func TestPostgresStandbyStreamingConfirmed(t *testing.T) {
	if postgresStandbyStreamingConfirmed(1) {
		t.Fatal("a single streaming sample must not skip reseed")
	}
	if !postgresStandbyStreamingConfirmed(2) {
		t.Fatal("two consecutive streaming samples are a live idle replica")
	}
}

func TestStandbyReplicationStableFor(t *testing.T) {
	if standbyReplicationStableFor < 5*time.Second {
		t.Fatalf("walreceiver can FATAL after a brief streaming window; stable wait got %s want >= 5s", standbyReplicationStableFor)
	}
}

func TestPostgresWALSegmentRemoved(t *testing.T) {
	msg := "could not receive data from WAL stream: ERROR:  requested WAL segment 0000001E000000050000000B has already been removed"
	if !postgresWALSegmentRemoved(msg) {
		t.Fatal("must detect the postgres FATAL that killed postgres.5052 after rolling reload")
	}
	if !postgresWALSegmentRemoved("FATAL:  requested WAL segment 000000010000000000000001 has already been removed") {
		t.Fatal("must detect the short FATAL form")
	}
	if postgresWALSegmentRemoved("LOG:  restartpoint complete") {
		t.Fatal("normal postgres logs must not look like a recycled WAL segment")
	}
	if postgresWALSegmentRemoved("LOG:  waiting for WAL to become available at 5/13002000") {
		t.Fatal("a standby whose upstream disconnected must not be treated as recycled WAL")
	}
	if postgresWALSegmentRemoved("9ce78903-8eca-4bea-984b-06188a59933e 2026-10-12 01:42:44.288 UTC LOG:  invalid resource manager ID 99 at 6/E56104B8") {
		t.Fatal("end of local WAL during standby startup must not reseed a healthy standby")
	}
	if postgresWALSegmentRemoved("LOG:  invalid record length at 6/E56104B8: expected at least 24, got 0") {
		t.Fatal("end of local WAL during standby startup must not reseed a healthy standby")
	}
	if !postgresWALSegmentRemoved(`FATAL:  could not receive timeline history file from the primary server: ERROR:  could not open file "pg_wal/00000024.history": No such file or directory`) {
		t.Fatal("a standby that cannot fetch an ancestor timeline history must reseed")
	}
}

// A sync promoted from a basebackup only had its own timeline's history, so
// a standby restarting on timeline 36 could never follow the new primary.
func TestEnsureTimelineHistory(t *testing.T) {
	dir := t.TempDir()
	newest := "1\t0/3000000\tno recovery target specified\n\n" +
		"2\t0/5000000\tno recovery target specified\n\n" +
		"4\t0/7000000\tno recovery target specified\n"
	if err := os.WriteFile(filepath.Join(dir, "00000005.history"), []byte(newest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "000000050000000000000007"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureTimelineHistory(dir); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"00000002.history": "1\t0/3000000\tno recovery target specified\n\n",
		"00000004.history": "1\t0/3000000\tno recovery target specified\n\n2\t0/5000000\tno recovery target specified\n\n",
	} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %s", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	// timeline 3 is not an ancestor of 5, so its history cannot be derived
	if _, err := os.Stat(filepath.Join(dir, "00000003.history")); !os.IsNotExist(err) {
		t.Fatalf("non-ancestor timeline history must not be written, stat err=%v", err)
	}
	if err := ensureTimelineHistory(filepath.Join(dir, "missing")); err != nil {
		t.Fatalf("missing pg_wal must be ignored: %s", err)
	}
}

func TestStandbyReuseStartupTimeout(t *testing.T) {
	if standbyReuseStartupTimeout > 60*time.Second {
		t.Fatalf("reused standby start must not wait opTimeout on recycled WAL; got %s", standbyReuseStartupTimeout)
	}
	p := NewProcess(Config{ID: "sync-1", DataDir: t.TempDir(), Port: "6502"})
	p.setConfig(&state.Config{Role: state.RoleSync, Upstream: &discoverd.Instance{Addr: "127.0.0.1:5432"}})
	if got := p.standbyStartupWait(); got != p.opTimeout {
		t.Fatalf("uninitialized data dir must use opTimeout, got %s", got)
	}
}

func TestVerifyStandbyReuseResult(t *testing.T) {
	reseed, err := verifyStandbyReuseResult(true, false, true, nil)
	if err != nil || reseed {
		t.Fatalf("healthy running replica must not reseed: reseed=%v err=%v", reseed, err)
	}
	reseed, err = verifyStandbyReuseResult(false, false, false, errors.New("postgres is not running"))
	if err != nil || !reseed {
		t.Fatalf("dead postmaster during health check must reseed, not return the error: reseed=%v err=%v", reseed, err)
	}
	reseed, err = verifyStandbyReuseResult(true, true, true, nil)
	if err != nil || !reseed {
		t.Fatalf("WAL recycle flag must reseed even if health looked streaming: reseed=%v err=%v", reseed, err)
	}
	reseed, err = verifyStandbyReuseResult(true, false, true, errors.New("requested WAL segment 0000001E000000050000000B has already been removed"))
	if err != nil || !reseed {
		t.Fatalf("WAL-recycle health error must reseed: reseed=%v err=%v", reseed, err)
	}
	reseed, err = verifyStandbyReuseResult(true, false, false, nil)
	if err != nil || !reseed {
		t.Fatalf("unhealthy replica must reseed: reseed=%v err=%v", reseed, err)
	}
	_, err = verifyStandbyReuseResult(true, false, false, errors.New("dial tcp: connection refused"))
	if err == nil {
		t.Fatal("unrelated query error while postgres is running must surface, not be swallowed as a reseed")
	}
}

func TestStartErrorNeedsReseed(t *testing.T) {
	if startErrorNeedsReseed(false, true, true) {
		t.Fatal("fresh basebackup start failure must not look like a reused-dir WAL recycle")
	}
	if !startErrorNeedsReseed(true, true, false) {
		t.Fatal("reused dir + stderr WAL recycle must reseed")
	}
	if !startErrorNeedsReseed(true, false, true) {
		t.Fatal("reused dir + needsReseed from unexpected exit must reseed")
	}
	if startErrorNeedsReseed(true, false, false) {
		t.Fatal("reused dir with no WAL recycle must return the original start error")
	}
}

func TestWalRecycleDetector(t *testing.T) {
	p := NewProcess(Config{ID: "1fa645ea-909e-4c16-8a39-a87fde4051a4", DataDir: t.TempDir(), Port: "6501"})
	d := &walRecycleDetector{p: p}
	msg := "2026-10-09 22:13:31.638 UTC FATAL:  could not receive data from WAL stream: ERROR:  requested WAL segment 0000001E000000050000000B has already been removed\n"
	if _, err := d.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	if !p.walRecycled() || !p.needsReseed() {
		t.Fatalf("detector must flag WAL recycle from postgres.5052 logs: recycled=%v reseed=%v", p.walRecycled(), p.needsReseed())
	}
}

func TestWaitForSyncResetDeadline(t *testing.T) {
	if waitForSyncResetDeadline(false, false) {
		t.Fatal("missing pg_stat_replication must not reset the catch-up timer")
	}
	if waitForSyncResetDeadline(false, true) {
		t.Fatal("no status must not reset even if flushed looks advanced")
	}
	if waitForSyncResetDeadline(true, false) {
		t.Fatal("status with no flush progress must not reset")
	}
	if !waitForSyncResetDeadline(true, true) {
		t.Fatal("flush progress must reset the timer")
	}
}

func TestWaitForSyncAppearanceTimeout(t *testing.T) {
	oneMin := time.Minute
	if got := waitForSyncAppearanceTimeout(true, oneMin); got != oneMin {
		t.Fatalf("primary appearance wait: got %s want %s", got, oneMin)
	}
	if got := waitForSyncAppearanceTimeout(false, oneMin); got != 15*time.Minute {
		t.Fatalf("sync waiting on async must cover pg_basebackup: got %s", got)
	}
}

func TestWaitForSyncTimeoutEnablesWrites(t *testing.T) {
	src, err := os.ReadFile("process.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "enablePrimaryWrites") {
		t.Fatal("waitForSync timeout must enable writes so a recycled-WAL replica cannot leave the primary ReadOnly")
	}
	if !strings.Contains(body, "ReadOnly: false") {
		t.Fatal("enablePrimaryWrites must clear default_transaction_read_only")
	}
	if !strings.Contains(body, "Keep watching") {
		t.Fatal("waitForSync timeout must keep watching so a late-streaming replica can still become SyncedDownstream")
	}
}

func TestReusedStandbyStartupBudget(t *testing.T) {
	dir := t.TempDir()
	p := NewProcess(Config{ID: "ce39ccd1-c5a4-42dd-a4fa-65565e3b3246", DataDir: dir, Port: "6503"})
	p.setConfig(&state.Config{Role: state.RoleSync, Upstream: &discoverd.Instance{Addr: "127.0.0.1:5432"}})
	if p.reusedStandbyStartup() {
		t.Fatal("empty data dir is not a reused standby")
	}
	if err := os.WriteFile(dir+"/PG_VERSION", []byte("16\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/"+sireniaIDMarker, []byte(p.id), 0644); err != nil {
		t.Fatal(err)
	}
	if !p.reusedStandbyStartup() {
		t.Fatal("initialized sync datadir must use the short startup budget")
	}
	if got := p.standbyStartupWait(); got != standbyReuseStartupTimeout {
		t.Fatalf("reused standby wait: got %s", got)
	}
	p.justBasebackup = true
	if p.reusedStandbyStartup() {
		t.Fatal("fresh basebackup must not use the reused-standby hang budget")
	}
	if got := p.standbyStartupWait(); got != p.opTimeout {
		t.Fatalf("fresh basebackup wait: got %s want opTimeout", got)
	}
}

func TestStartupTimeoutNeedsReseed(t *testing.T) {
	if !startupTimeoutNeedsReseed(true, false) {
		t.Fatal("reused standby that never accepts connections must reseed (dev-node1 postgres.6770 after node2 reboot)")
	}
	if startupTimeoutNeedsReseed(false, false) {
		t.Fatal("fresh basebackup timeout must not be classified as a reused-dir hang")
	}
	if !startupTimeoutNeedsReseed(false, true) {
		t.Fatal("WAL recycle flag must reseed even on a fresh backup start")
	}
}

func TestStandbyWALRecycleShouldReseedWhileRunning(t *testing.T) {
	up := &discoverd.Instance{Addr: "100.100.70.1:5432"}
	cfg := &state.Config{Role: state.RoleSync, Upstream: up}
	if !standbyWALRecycleShouldReseed(cfg, false, true) {
		t.Fatal("a running sync hung after start() returned must still reseed")
	}
	if standbyWALRecycleShouldReseed(cfg, true, true) {
		t.Fatal("stopped peer must not reseed")
	}
	if standbyWALRecycleShouldReseed(&state.Config{Role: state.RolePrimary, Upstream: up}, false, true) {
		t.Fatal("primary must not reseed as a standby")
	}
	if standbyWALRecycleShouldReseed(cfg, false, false) {
		t.Fatal("no WAL recycle must not reseed")
	}
}

func TestWalRecycleDetectorIgnoresReseedWhileStartHoldsLock(t *testing.T) {
	p := NewProcess(Config{ID: "ce39ccd1-c5a4-42dd-a4fa-65565e3b3246", DataDir: t.TempDir(), Port: "6504"})
	p.setConfig(&state.Config{Role: state.RoleSync, Upstream: &discoverd.Instance{Addr: "127.0.0.1:5432"}})
	p.inStart.Store(true)
	d := &walRecycleDetector{p: p}
	msg := "ce39ccd1-c5a4-42dd-a4fa-65565e3b3246 2026-10-09 23:22:48.196 UTC FATAL:  requested WAL segment 0000001E000000050000000B has already been removed\n"
	if _, err := d.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	if !p.walRecycled() || !p.needsReseed() {
		t.Fatal("detector must flag a recycled WAL segment")
	}
	if p.reseedScheduled.Load() {
		t.Fatal("detector must not schedule reseed while start() holds the process lock")
	}
}

// When a rolling deploy stopped the primary, every standby logged "waiting
// for WAL to become available", reseeded from the stopped upstream and lost
// its data directory.
func TestWalRecycleDetectorIgnoresUpstreamDisconnect(t *testing.T) {
	p := NewProcess(Config{ID: "df21e72a", DataDir: t.TempDir(), Port: "6505"})
	p.setConfig(&state.Config{Role: state.RoleSync, Upstream: &discoverd.Instance{Addr: "127.0.0.1:5432"}})
	d := &walRecycleDetector{p: p}
	lines := "FATAL:  terminating connection due to administrator command\n" +
		"LOG:  waiting for WAL to become available at 5/1B851068\n"
	if _, err := d.Write([]byte(lines)); err != nil {
		t.Fatal(err)
	}
	if p.walRecycled() || p.needsReseed() || p.reseedScheduled.Load() {
		t.Fatal("upstream disconnect must not trigger a reseed")
	}
}

// A chaos kill of the async peer restarted it on its intact datadir; replay
// logged the end of local WAL and streaming resumed, but the detector flagged
// it and an 11 GB basebackup kept the peer unhealthy for over a minute.
func TestWalRecycleDetectorIgnoresEndOfLocalWAL(t *testing.T) {
	p := NewProcess(Config{ID: "9ce78903", DataDir: t.TempDir(), Port: "6506"})
	p.setConfig(&state.Config{Role: state.RoleAsync, Upstream: &discoverd.Instance{Addr: "127.0.0.1:5432"}})
	d := &walRecycleDetector{p: p}
	lines := "LOG:  redo starts at 6/E55CD940\n" +
		"LOG:  consistent recovery state reached at 6/E56104B8\n" +
		"LOG:  invalid resource manager ID 99 at 6/E56104B8\n" +
		"LOG:  database system is ready to accept read-only connections\n" +
		"LOG:  started streaming WAL from primary at 6/E5000000 on timeline 68\n"
	if _, err := d.Write([]byte(lines)); err != nil {
		t.Fatal(err)
	}
	if p.walRecycled() || p.needsReseed() || p.reseedScheduled.Load() {
		t.Fatal("end of local WAL followed by streaming must not trigger a reseed")
	}
}

func writeFakeBasebackup(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pg_basebackup"), []byte("#!/bin/sh\n"+script), 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPullBasebackupFailureKeepsExistingDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "db")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "PG_VERSION"), []byte("16\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binDir := writeFakeBasebackup(t, "exit 1\n")
	p := NewProcess(Config{ID: "sync-1", BinDir: binDir, DataDir: dataDir, Port: "6506"})
	up := &discoverd.Instance{Addr: "127.0.0.1:1", Meta: map[string]string{IDKey: "primary"}}

	if err := p.pullBasebackup(p.log, up); err == nil {
		t.Fatal("expected basebackup error")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "PG_VERSION")); err != nil {
		t.Fatal("a failed basebackup must not wipe the existing data directory")
	}
	if _, err := os.Stat(p.basebackupStagingDir()); !os.IsNotExist(err) {
		t.Fatalf("staging directory must be removed after failure, stat err=%v", err)
	}
}

func TestPullBasebackupSuccessReplacesDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "db")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "stale"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	// Writes PG_VERSION into the directory passed via --pgdata.
	binDir := writeFakeBasebackup(t, `mkdir -p "$2" && echo 16 > "$2/PG_VERSION"`+"\n")
	p := NewProcess(Config{ID: "sync-1", BinDir: binDir, DataDir: dataDir, Port: "6507"})
	up := &discoverd.Instance{Addr: "127.0.0.1:1", Meta: map[string]string{IDKey: "primary"}}

	if err := p.pullBasebackup(p.log, up); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "PG_VERSION")); err != nil {
		t.Fatal("expected basebackup contents in the data directory")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "stale")); !os.IsNotExist(err) {
		t.Fatal("old data directory contents must be replaced")
	}
	if _, err := os.Stat(p.basebackupStagingDir()); !os.IsNotExist(err) {
		t.Fatalf("staging directory must be removed after success, stat err=%v", err)
	}
}

func TestReconfigureHAPrimaryAllowsNilDownstream(t *testing.T) {
	p := NewProcess(Config{ID: "node1", Singleton: false, DataDir: t.TempDir(), Port: "6500"})
	if err := p.Reconfigure(&state.Config{Role: state.RolePrimary}); err != nil {
		t.Fatalf("SINGLETON=false primary at scale 1 must accept nil downstream (one-node-write): %v", err)
	}
	if p.config() == nil || p.config().Role != state.RolePrimary {
		t.Fatal("expected RolePrimary config to be stored for Start")
	}
}

func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	return port
}

func TestNewLocalPoolFailedConnectReturnsNil(t *testing.T) {
	pool, err := newLocalPool(closedPort(t))
	if err == nil {
		t.Fatal("expected connect error")
	}
	if pool != nil {
		t.Fatal("failed connect must not return a zero-connection pool")
	}
}

// dev-node1 sync: start() published a zero-connection pool into p.db while
// postgres was still starting; concurrent /status XLogPosition calls waited
// forever in pgx Acquire holding dbMtx.RLock, and start() hung on dbMtx.Lock.
func TestConnectDBFailureDoesNotWedgeStatus(t *testing.T) {
	p := NewProcess(Config{ID: "sync-1", DataDir: t.TempDir(), Port: closedPort(t)})
	p.setConfig(&state.Config{Role: state.RoleSync, Upstream: &discoverd.Instance{Addr: "127.0.0.1:5432"}})
	p.setRunning(true)

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if err := p.connectDB(); err == nil {
				t.Error("expected connect error")
				return
			}
		}
	}()
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				p.XLogPosition()
			}
		}()
	}
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("XLogPosition/connectDB deadlocked on a broken pool")
	}
	p.dbMtx.RLock()
	defer p.dbMtx.RUnlock()
	if p.db != nil {
		t.Fatal("failed connect must leave p.db nil")
	}
}

// The sirenia peer cancels a standby's wait on an upstream that has left
// discoverd instead of letting it block for upstreamTimeout.
func TestCancelUpstreamWaitAbortsWaitForUpstream(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	p := NewProcess(Config{ID: "async-1", DataDir: t.TempDir(), Port: "6508"})
	errCh := make(chan error, 1)
	go func() { errCh <- p.waitForUpstream(&discoverd.Instance{Addr: addr}) }()

	time.Sleep(300 * time.Millisecond)
	p.CancelUpstreamWait()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected canceled wait to return an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForUpstream did not return after CancelUpstreamWait")
	}
	p.CancelUpstreamWait()
}
