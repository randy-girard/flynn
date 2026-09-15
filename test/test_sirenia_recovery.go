package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	ct "github.com/flynn/flynn/controller/types"
	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/mysqlurl"
	sc "github.com/flynn/flynn/pkg/sirenia/client"
	"github.com/flynn/flynn/pkg/sirenia/state"
	c "github.com/flynn/go-check"
	_ "github.com/go-sql-driver/mysql"
)

type SireniaRecoverySuite struct {
	Helper
}

var _ = c.ConcurrentSuite(&SireniaRecoverySuite{})

// TestPostgresSyncTakeoverAfterProcessRestart stops the postgres primary and
// sync jobs within a short window. The sync's replacement must start from its
// initialized data directory without a live upstream and take over so the
// cluster returns read-write (regression for the primary-loss + sync-restart
// deadlock fixed by postgres data-dir reuse).
func (s *SireniaRecoverySuite) TestPostgresSyncTakeoverAfterProcessRestart(t *c.C) {
	x := s.bootCluster(t, 3)
	defer x.Destroy()

	h := newClusterUpdateHelpers(t, x)
	before := h.waitSireniaHA("postgres", 3, 5*time.Minute)
	t.Assert(before.Generation > 0, c.Equals, true)
	primaryJob := h.jobID(before.Primary)
	syncJob := h.jobID(before.Sync)
	t.Assert(primaryJob, c.Not(c.Equals), "")
	t.Assert(syncJob, c.Not(c.Equals), "")
	debugf(t, "postgres generation=%d primary_job=%s sync_job=%s", before.Generation, primaryJob, syncJob)

	r := s.newGitRepo(t, "empty")
	r.cluster = x
	t.Assert(r.flynn("create", "pg-takeover-probe"), Succeeds)
	t.Assert(r.flynn("resource", "add", "postgres"), Succeeds)
	t.Assert(r.flynn("pg", "psql", "--", "-c",
		"CREATE TABLE takeover_probe (id serial PRIMARY KEY); INSERT INTO takeover_probe DEFAULT VALUES;"),
		Succeeds)

	h.stopJob(primaryJob)
	time.Sleep(500 * time.Millisecond)
	h.stopJob(syncJob)

	deadline := time.Now().Add(5 * time.Minute)
	var after *state.State
	for time.Now().Before(deadline) {
		st, err := h.sireniaState("postgres")
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		if st.Generation > before.Generation && st.Primary != nil {
			status, err := sc.NewClient(st.Primary.Addr).Status()
			if err == nil && status.Database != nil && status.Database.Running && status.Database.ReadWrite {
				after = st
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	if after == nil {
		t.Fatal("postgres did not declare a new generation and become read-write within 5m after primary+sync restart")
	}
	debugf(t, "postgres recovered generation=%d primary=%s", after.Generation, after.Primary.Addr)

	t.Assert(r.flynn("pg", "psql", "--", "-c", "SELECT COUNT(*) FROM takeover_probe"), Succeeds)
	t.Assert(r.flynn("pg", "psql", "--", "-c", "INSERT INTO takeover_probe DEFAULT VALUES"), Succeeds)
}

// TestMariaDBLaggingReplicaNotReseeded builds a 3-peer mariadb cluster, writes
// a large payload on the primary while the async stays up, and asserts the
// async catches up without leaving Running=false (a full reseed) or changing
// peer identity. Lagging-but-advancing must be treated as healthy.
func (s *SireniaRecoverySuite) TestMariaDBLaggingReplicaNotReseeded(t *c.C) {
	s.skipUnlessProvider(t, "mysql")
	client := s.controllerClient(t)
	disc := s.discoverdClient(t)

	app := &ct.App{Name: "mariadb-lag-probe", Strategy: "sirenia", DeployTimeout: 900}
	t.Assert(client.CreateApp(app), c.IsNil)
	defer client.DeleteApp(app.ID)

	release, err := client.GetAppRelease("mariadb")
	t.Assert(err, c.IsNil)
	release.ID = ""
	release.Env["FLYNN_MYSQL"] = app.Name
	release.Env["MYSQL_HOST"] = fmt.Sprintf("leader.%s.discoverd", app.Name)
	delete(release.Env, "SINGLETON")
	procName := release.Env["SIRENIA_PROCESS"]
	proc := release.Processes[procName]
	proc.Service = app.Name
	release.Processes[procName] = proc
	t.Assert(client.CreateRelease(app.ID, release), c.IsNil)
	t.Assert(client.SetAppRelease(app.ID, release.ID), c.IsNil)

	t.Assert(client.PutFormation(&ct.Formation{
		AppID:     app.ID,
		ReleaseID: release.ID,
		Processes: map[string]int{procName: 3, "web": 1},
	}), c.IsNil)

	st := waitSireniaServiceHA(t, disc, app.Name, 3, 10*time.Minute)
	asyncInst := st.Async[0]
	asyncAddr := asyncInst.Addr
	primaryAddr := st.Primary.Addr
	asyncJobID := asyncInst.Meta["FLYNN_JOB_ID"]
	asyncPeerID := asyncInst.Meta["MARIADB_ID"]
	t.Assert(asyncJobID, c.Not(c.Equals), "")
	debugf(t, "mariadb primary=%s async=%s job=%s peer=%s", primaryAddr, asyncAddr, asyncJobID, asyncPeerID)

	password := release.Env["MYSQL_PWD"]
	primaryDSN := &mysqlurl.DSN{
		Host:     primaryAddr,
		User:     "flynn",
		Password: password,
		Database: "mysql",
		Timeout:  30 * time.Second,
	}
	primaryDB, err := sql.Open("mysql", primaryDSN.String())
	t.Assert(err, c.IsNil)
	defer primaryDB.Close()

	_, err = primaryDB.Exec(`CREATE DATABASE IF NOT EXISTS lag_probe`)
	t.Assert(err, c.IsNil)
	_, err = primaryDB.Exec(`CREATE TABLE IF NOT EXISTS lag_probe.t (id INT PRIMARY KEY, blob LONGBLOB)`)
	t.Assert(err, c.IsNil)

	status, err := sc.NewClient(asyncAddr).Status()
	t.Assert(err, c.IsNil)
	t.Assert(status.Database.Running, c.Equals, true)
	xlogBefore := status.Database.XLog

	blob := make([]byte, 256*1024) // 256KiB × 200 ≈ 50MiB
	for i := range blob {
		blob[i] = byte(i)
	}
	for i := 0; i < 200; i++ {
		_, err = primaryDB.Exec(`INSERT INTO lag_probe.t (id, blob) VALUES (?, ?)`, i, blob)
		t.Assert(err, c.IsNil)

		// While lagging, the async must stay Running and keep the same peer.
		status, err = sc.NewClient(asyncAddr).Status()
		t.Assert(err, c.IsNil)
		t.Assert(status.Database.Running, c.Equals, true)

		meta, err := disc.Service(app.Name).GetMeta()
		t.Assert(err, c.IsNil)
		var cur state.State
		t.Assert(json.Unmarshal(meta.Data, &cur), c.IsNil)
		found := false
		for _, a := range cur.Async {
			if a.Meta["FLYNN_JOB_ID"] == asyncJobID {
				found = true
				t.Assert(a.Meta["MARIADB_ID"], c.Equals, asyncPeerID)
				break
			}
		}
		t.Assert(found, c.Equals, true)
	}

	asyncDSN := &mysqlurl.DSN{
		Host:     asyncAddr,
		User:     "flynn",
		Password: password,
		Database: "mysql",
		Timeout:  30 * time.Second,
	}
	asyncDB, err := sql.Open("mysql", asyncDSN.String())
	t.Assert(err, c.IsNil)
	defer asyncDB.Close()

	deadline := time.Now().Add(5 * time.Minute)
	caughtUp := false
	for time.Now().Before(deadline) {
		status, err = sc.NewClient(asyncAddr).Status()
		t.Assert(err, c.IsNil)
		t.Assert(status.Database.Running, c.Equals, true)

		var n int
		if err := asyncDB.QueryRow(`SELECT COUNT(*) FROM lag_probe.t`).Scan(&n); err == nil && n >= 200 {
			caughtUp = true
			break
		}
		if status.Database.XLog != "" && status.Database.XLog != xlogBefore {
			debugf(t, "async xlog advancing %s -> %s", xlogBefore, status.Database.XLog)
		}
		time.Sleep(2 * time.Second)
	}
	t.Assert(caughtUp, c.Equals, true)

	// Peer identity must be unchanged after catch-up (no wipe+reseed restart).
	meta, err := disc.Service(app.Name).GetMeta()
	t.Assert(err, c.IsNil)
	var after state.State
	t.Assert(json.Unmarshal(meta.Data, &after), c.IsNil)
	samePeer := false
	for _, a := range after.Async {
		if a.Meta["FLYNN_JOB_ID"] == asyncJobID && a.Meta["MARIADB_ID"] == asyncPeerID {
			samePeer = true
			break
		}
	}
	t.Assert(samePeer, c.Equals, true)
	debug(t, "mariadb async caught up without reseed (Running stayed true, job/peer id unchanged)")
}

func waitSireniaServiceHA(t *c.C, disc *discoverd.Client, service string, expectedPeers int, timeout time.Duration) *state.State {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		meta, err := disc.Service(service).GetMeta()
		if err != nil || meta == nil || len(meta.Data) == 0 {
			lastErr = err
			time.Sleep(2 * time.Second)
			continue
		}
		var st state.State
		if err := json.Unmarshal(meta.Data, &st); err != nil {
			lastErr = err
			time.Sleep(2 * time.Second)
			continue
		}
		if st.Primary != nil && st.Sync != nil && 2+len(st.Async) == expectedPeers {
			if status, err := sc.NewClient(st.Primary.Addr).Status(); err == nil &&
				status.Database != nil && status.Database.Running {
				return &st
			}
		}
		lastErr = fmt.Errorf("asyncs=%d", len(st.Async))
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for %s HA (%d peers): %v", service, expectedPeers, lastErr)
	return nil
}
