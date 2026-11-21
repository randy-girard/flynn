package postgresql

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/flynn/go-check"
	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/pkg/attempt"
	"github.com/randy-girard/flynn/pkg/sirenia/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

// shortStallWatch shrinks a process's stalled-standby budget so a test sees
// the watchdog act in seconds instead of the production 45s.
func shortStallWatch(p *Process) {
	p.stallTimeout = 3 * time.Second
	p.stallCheckInterval = 200 * time.Millisecond
}

func onlinePrimaryStatus() *client.Status {
	return &client.Status{
		Peer: &state.PeerInfo{Role: state.RolePrimary},
		Database: &client.DatabaseInfo{
			Config:     &state.Config{Role: state.RolePrimary},
			Running:    true,
			XLog:       "0/3000000",
			UserExists: true,
		},
	}
}

func systemIdentifier(c *C, p *Process) string {
	conn := connect(c, p, "postgres")
	defer conn.Close()
	var id string
	c.Assert(conn.QueryRow("SELECT system_identifier::text FROM pg_control_system()").Scan(&id), IsNil)
	return id
}

// TestStandbyReseedsWhenRemasterNeverStreams reproduces CORE-25-F1-F3. A
// running sync is remastered to a new primary while that primary's sirenia
// API does not answer yet, so the reused-standby health check is skipped. The
// standby is consistent and accepts connections, but its walreceiver can never
// stream from the new primary (here: a different system identifier; on the
// dev cluster: local WAL past the timeline switch). Nothing ever looked at it
// again, so it stayed frozen for hours. It must reseed from the new primary
// within a bounded time once that primary answers.
func (PostgresSuite) TestStandbyReseedsWhenRemasterNeverStreams(c *C) {
	oldPrimary := NewTestProcess(c, 31)
	standby := NewTestProcess(c, 32)
	newPrimary := NewTestProcess(c, 33)
	shortStallWatch(standby)

	var upstreamUp atomic.Bool
	standby.upstreamStatus = func(addr string) (*client.Status, error) {
		if !upstreamUp.Load() {
			return nil, errors.New("connection refused")
		}
		return onlinePrimaryStatus(), nil
	}

	c.Assert(oldPrimary.Reconfigure(pgConfig(state.RolePrimary, nil, standby)), IsNil)
	c.Assert(oldPrimary.Start(), IsNil)
	defer oldPrimary.Stop()
	c.Assert(standby.Reconfigure(pgConfig(state.RoleSync, oldPrimary, nil)), IsNil)
	c.Assert(standby.Start(), IsNil)
	defer standby.Stop()
	waitReplSync(c, oldPrimary, 32)

	c.Assert(newPrimary.Reconfigure(pgConfig(state.RolePrimary, nil, nil)), IsNil)
	c.Assert(newPrimary.Start(), IsNil)
	defer newPrimary.Stop()
	want := systemIdentifier(c, newPrimary)
	c.Assert(systemIdentifier(c, standby) == want, Equals, false)

	// Remaster while the new primary's status endpoint is down.
	c.Assert(standby.Reconfigure(pgConfig(state.RoleSync, newPrimary, nil)), IsNil)
	c.Assert(standby.Running(), Equals, true)
	upstreamUp.Store(true)

	err := attempt.Strategy{Total: 30 * time.Second, Delay: 500 * time.Millisecond}.Run(func() error {
		if !standby.Running() {
			return errors.New("standby not running")
		}
		// The standby restarts during the reseed; connect() would assert.
		port, _ := strconv.Atoi(standby.port)
		conn, err := pgx.Connect(pgx.ConnConfig{Host: "127.0.0.1", Port: uint16(port), User: "flynn", Password: "password", Database: "postgres"})
		if err != nil {
			return err
		}
		defer conn.Close()
		var id string
		if err := conn.QueryRow("SELECT system_identifier::text FROM pg_control_system()").Scan(&id); err != nil {
			return err
		}
		if id != want {
			return errors.New("standby still holds the old primary's data")
		}
		var status string
		if err := conn.QueryRow("SELECT COALESCE((SELECT status FROM pg_stat_wal_receiver LIMIT 1), '')").Scan(&status); err != nil {
			return err
		}
		if status != "streaming" {
			return errors.New("walreceiver is " + status)
		}
		return nil
	})
	c.Assert(err, IsNil, Commentf("stalled standby was not reseeded from its new primary"))
}

// TestStandbyProgressWatchKeepsHealthyStandby: an idle primary sends no WAL,
// so the standby's replay LSN does not move. A live walreceiver is progress
// and the watchdog must never replace that data directory.
func (PostgresSuite) TestStandbyProgressWatchKeepsHealthyStandby(c *C) {
	primary := NewTestProcess(c, 34)
	standby := NewTestProcess(c, 35)
	shortStallWatch(standby)
	standby.upstreamStatus = func(string) (*client.Status, error) { return onlinePrimaryStatus(), nil }

	c.Assert(primary.Reconfigure(pgConfig(state.RolePrimary, nil, standby)), IsNil)
	c.Assert(primary.Start(), IsNil)
	defer primary.Stop()
	c.Assert(standby.Reconfigure(pgConfig(state.RoleSync, primary, nil)), IsNil)
	c.Assert(standby.Start(), IsNil)
	defer standby.Stop()
	waitReplSync(c, primary, 35)

	sentinel := filepath.Join(standby.dataDir, "healthy-sentinel")
	c.Assert(os.WriteFile(sentinel, []byte("keep"), 0644), IsNil)
	chownPostgresPath(c, sentinel)

	time.Sleep(3 * standby.stallTimeout)

	_, err := os.Stat(sentinel)
	c.Assert(err, IsNil, Commentf("a streaming standby was reseeded"))
	info, _ := standby.Info()
	c.Assert(info.StandbyStalled, Equals, false)
}

func TestStandbyProgressTracker(t *testing.T) {
	const budget = 45 * time.Second
	t0 := time.Unix(1000, 0)
	type sample struct {
		at         time.Duration
		receiver   string
		lsn        string
		upstreamOK bool
	}
	for _, tc := range []struct {
		name    string
		samples []sample
		stuck   bool
	}{
		{
			// postgres.7469: no walreceiver, replay frozen at 0/E16ADD00,
			// new primary online.
			name:  "frozen replay, no receiver, upstream online",
			stuck: true,
			samples: []sample{
				{0, "", "0/E16ADD00", true}, {20 * time.Second, "", "0/E16ADD00", true}, {45 * time.Second, "", "0/E16ADD00", true},
			},
		},
		{
			name:  "budget not used up yet",
			stuck: false,
			samples: []sample{
				{0, "", "0/E16ADD00", true}, {44 * time.Second, "", "0/E16ADD00", true},
			},
		},
		{
			name:  "idle primary: receiver streaming, lsn constant",
			stuck: false,
			samples: []sample{
				{0, "streaming", "0/300", true}, {30 * time.Second, "streaming", "0/300", true}, {90 * time.Second, "streaming", "0/300", true},
			},
		},
		{
			name:  "local replay advancing without receiver",
			stuck: false,
			samples: []sample{
				{0, "", "0/100", true}, {30 * time.Second, "", "0/200", true}, {60 * time.Second, "", "0/300", true},
			},
		},
		{
			name:  "upstream down the whole time",
			stuck: false,
			samples: []sample{
				{0, "", "0/100", false}, {60 * time.Second, "", "0/100", false}, {120 * time.Second, "", "0/100", false},
			},
		},
		{
			// An upstream restart resets the clock: a full budget of the
			// upstream being online is needed after it comes back.
			name:  "upstream came back 10s ago",
			stuck: false,
			samples: []sample{
				{0, "", "0/100", false}, {60 * time.Second, "", "0/100", false}, {110 * time.Second, "", "0/100", true}, {120 * time.Second, "", "0/100", true},
			},
		},
		{
			name:  "upstream back for the full budget",
			stuck: true,
			samples: []sample{
				{0, "", "0/100", false}, {60 * time.Second, "", "0/100", true}, {105 * time.Second, "", "0/100", true},
			},
		},
		{
			// Postgres restarted into "starting up" after a crash: queries fail.
			name:  "queries fail",
			stuck: true,
			samples: []sample{
				{0, "", "", true}, {45 * time.Second, "", "", true},
			},
		},
		{
			name:  "streaming stopped 45s ago",
			stuck: true,
			samples: []sample{
				{0, "streaming", "0/100", true}, {10 * time.Second, "streaming", "0/100", true}, {30 * time.Second, "", "0/100", true}, {55 * time.Second, "", "0/100", true},
			},
		},
	} {
		tr := newStandbyProgressTracker(budget)
		var got bool
		for _, s := range tc.samples {
			got = tr.observe(t0.Add(s.at), s.receiver, s.lsn, s.upstreamOK)
		}
		if got != tc.stuck {
			t.Errorf("%s: stuck=%v, want %v", tc.name, got, tc.stuck)
		}
	}
}

func TestStandbyStallTimeoutBounded(t *testing.T) {
	if standbyStallTimeout <= 0 || standbyStallTimeout > 2*time.Minute {
		t.Fatalf("standbyStallTimeout = %s, want a bounded budget", standbyStallTimeout)
	}
	if standbyStallCheckInterval <= 0 || standbyStallCheckInterval >= standbyStallTimeout {
		t.Fatalf("standbyStallCheckInterval = %s must be shorter than the budget", standbyStallCheckInterval)
	}
}

func TestUpstreamOnline(t *testing.T) {
	ok := onlinePrimaryStatus()
	stalled := onlinePrimaryStatus()
	stalled.Peer.Role = state.RoleSync
	stalled.Database.Config.Role = state.RoleSync
	stalled.Database.StandbyStalled = true
	streamingSync := onlinePrimaryStatus()
	streamingSync.Peer.Role = state.RoleSync
	copying := onlinePrimaryStatus()
	copying.Database.Basebackup = &client.BasebackupProgress{Percent: 10}
	down := onlinePrimaryStatus()
	down.Database.Running = false
	noXLog := onlinePrimaryStatus()
	noXLog.Database.XLog = ""
	for _, tc := range []struct {
		name string
		st   *client.Status
		err  error
		want bool
	}{
		{"primary", ok, nil, true},
		{"streaming sync", streamingSync, nil, true},
		{"stalled sync", stalled, nil, false},
		{"copying basebackup", copying, nil, false},
		{"not running", down, nil, false},
		{"no xlog", noXLog, nil, false},
		{"error", nil, errors.New("refused"), false},
		{"nil database", &client.Status{}, nil, false},
	} {
		if got, reason := upstreamOnline(tc.st, tc.err); got != tc.want {
			t.Errorf("%s: online=%v (%s), want %v", tc.name, got, reason, tc.want)
		}
	}
}

// TestWaitForUpstreamSkipsStalledStandby: a new async must not copy a
// basebackup from a sync that is consistent but stuck (the async churn in
// CORE-25-F1-F3). It waits instead, and goes ahead once the sync recovered.
func TestWaitForUpstreamSkipsStalledStandby(t *testing.T) {
	p := NewProcess(Config{ID: "async", DataDir: t.TempDir()})
	var calls atomic.Int32
	p.upstreamStatus = func(string) (*client.Status, error) {
		st := onlinePrimaryStatus()
		st.Peer.Role = state.RoleSync
		st.Database.StandbyStalled = calls.Add(1) <= 5
		return st, nil
	}
	if err := p.waitForUpstream(peerInst("127.0.0.1:5432", "sync")); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n <= 5 {
		t.Fatalf("waitForUpstream accepted a stalled upstream after %d polls", n)
	}
}

func TestInfoReportsStandbyStalled(t *testing.T) {
	p := NewProcess(Config{ID: "sync", DataDir: t.TempDir()})
	p.stalledVal.Store(true)
	info, _ := p.Info()
	if !info.StandbyStalled {
		t.Fatal("Info does not report standby_stalled")
	}
}

func TestSameStandbyTarget(t *testing.T) {
	up := peerInst("10.0.0.1:5432", "p1")
	a := &state.Config{Role: state.RoleSync, Upstream: up, Downstream: peerInst("10.0.0.3:5432", "a1")}
	sameNewDown := &state.Config{Role: state.RoleSync, Upstream: peerInst("10.0.0.1:5432", "p1"), Downstream: peerInst("10.0.0.4:5432", "a2")}
	moved := &state.Config{Role: state.RoleSync, Upstream: peerInst("10.0.0.9:5432", "p1")}
	newPrimary := &state.Config{Role: state.RoleSync, Upstream: peerInst("10.0.0.1:5432", "p2")}
	async := &state.Config{Role: state.RoleAsync, Upstream: up}
	if !sameStandbyTarget(a, sameNewDown) {
		t.Fatal("a new downstream or a re-applied config must keep the stall budget")
	}
	for name, b := range map[string]*state.Config{"moved upstream": moved, "new primary": newPrimary, "role change": async, "nil": nil} {
		if sameStandbyTarget(a, b) {
			t.Fatalf("%s must restart the stall budget", name)
		}
	}
}
