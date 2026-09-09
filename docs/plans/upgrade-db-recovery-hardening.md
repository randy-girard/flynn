# Upgrade / DB recovery hardening — implementation plan

Status: **IN PROGRESS** on branch `upgrade-db-recovery-hardening` (item 1 landed in working tree).

Branch reviewed: `ci-tests-and-release` (commits `32c39ba9` … `164267a5`), merged to `main`.

## Background

Historically, upgrading a multi-node cluster that already had running sirenia
databases (postgres/mariadb/mongodb) sometimes left peers unable to come back
up. The recent commits fix several real deadlocks and misorderings:

- sync peers can start without a live upstream so takeover isn't blocked
  (`32c39ba9`, MariaDB only)
- `applyConfig` / `startTakeoverWithPeer` gate on `db.Running()` rather than
  intent (`32c39ba9`)
- deployer tolerates gone/unreachable peers and waits for the successor to be
  the recorded sync before stopping the primary (`1a517ef4`)
- async→sync promotion now enables semi-sync master and downstream tracking
  instead of being a no-op (`746c011a`)
- rolling `flynn-host update` settles between hosts and fails hard on
  undiscoverable remotes (`79b488c4`)

The review found that the new *repair heuristics* in the updater equate
"database not running" with "peer stuck". That is exactly the state a large
postgres standby is in while it re-seeds after a restart, so the updater can
now kill healthy-but-slow peers and extend the outage it is meant to shorten.
This document lists the concrete changes and tests to close those gaps.

Priority order: **1, 2, 3** are the ones that can reproduce the historical
symptom; **4–7** are hardening.

---

## 1. Stop `RepairSireniaClusterQuorum` from killing re-seeding peers

**Files:** `pkg/updaterdeploy/repair_sirenia_quorum.go`,
`pkg/updaterdeploy/repair_sirenia_quorum_test.go`

### Problem

`sireniaInstanceHealthy` (`repair_sirenia_quorum.go:211-220`) returns
`status.Database.Running`. A sirenia peer registers in discoverd at process
start (`appliance/postgresql/cmd/flynn-postgres/main.go:55`), long before the
database is running. A postgres standby that restarts *always* runs a fresh
`pg_basebackup` (`appliance/postgresql/process.go:462-486`), so for the whole
seed window the job is `up`, registered, and `Running=false`.

`jobsToRestartForSireniaQuorum` (`:253-278`) includes `up` and `starting`
jobs, so it `DeleteJob`s that peer and restarts the seed from zero.
`waitForSireniaAsyncQuorum` (`:222-241`) then passes immediately because it
only checks the async *count* in discoverd meta, so the repair reports success
while the replacement is again mid-seed.

### Change

1. Redefine "unhealthy" as *stuck*, not *not running*. A peer is stuck when its
   sirenia HTTP API is reachable and **any** of:
   - `status.Peer.RetryPending != nil` and older than a threshold
     (`sireniaRepairRetryPendingGrace`, default **5m**) — `applyConfig` has
     been failing repeatedly;
   - `status.Peer.Role` is `unassigned`/`unknown` while the peer *is* listed as
     primary/sync/async in meta (role never converged);
   - `status.Database.Running == false` **and** the job is older than
     `sireniaRepairSeedGrace` (default **30m**, overridable via env
     `SIRENIA_REPAIR_SEED_GRACE`) — long enough to seed a large DB.

   A peer whose API is unreachable (`Status()` error) stays "unhealthy" as
   today; that is the zombie case the repair exists for.

2. Give `jobsToRestartForSireniaQuorum` access to job age. Use
   `ct.Job.CreatedAt` (falls back to `UpdatedAt`) and skip `starting`/`up`
   jobs younger than `sireniaRepairSeedGrace` when the only failing signal is
   `Running == false`.

3. Change the health probe signature so the extra inputs are testable:

   ```go
   type sireniaPeerProbe func(inst *discoverd.Instance) (*sireniaclient.Status, error)
   var probeSireniaInstance sireniaPeerProbe = ...
   func sireniaInstanceStuck(status *sireniaclient.Status, err error, jobAge time.Duration, now time.Time) bool
   ```

   Keep `checkSireniaInstanceHealthy` as a thin wrapper for existing tests.

4. Make `waitForSireniaAsyncQuorum` also require `sireniaMetaPeersHealthy`
   (with the new definition) before declaring success, otherwise the wait is
   meaningless after a restart.

5. Log the decision per job with the signal that triggered it
   (`reason=retry_pending|role_mismatch|not_running_past_grace|unreachable`).

### Tests

- `TestSireniaInstanceStuck_SeedingPeerWithinGraceIsHealthy` — `Running=false`,
  `RetryPending=nil`, job age 2m → not stuck.
- `TestSireniaInstanceStuck_NotRunningPastGraceIsStuck` — same, job age 45m →
  stuck.
- `TestSireniaInstanceStuck_RetryPendingOld` — `RetryPending` 10m ago → stuck
  regardless of age.
- `TestSireniaInstanceStuck_UnreachableIsStuck` — probe returns error → stuck.
- `TestJobsToRestartForSireniaQuorum_SkipsYoungSeedingJobs` — mix of a young
  seeding job, an old seeding job, an unregistered job; only the latter two
  are returned.
- Update `TestRepairSireniaClusterQuorumRestartsUnregisteredAndDownJobs` and
  `TestRepairSireniaClusterQuorumNoopWhenSatisfiedAndHealthy` to the new probe
  hook.
- `TestWaitForSireniaAsyncQuorum_RequiresHealthyPeers` — meta count satisfied
  but one peer stuck → keeps waiting until probe flips.

---

## 2. Bring postgres to parity on "start standby without live upstream"

**Files:** `appliance/postgresql/process.go`,
`appliance/postgresql/process_test.go`

### Problem

`32c39ba9` added the `dataDirInitialized()` path only in MariaDB
(`appliance/mariadb/process.go:742-753`). Postgres `assumeStandby`
(`process.go:456-500`) still requires a reachable upstream
(`waitForUpstream`, 90s) and a full `pg_basebackup` whenever the process is
not already running. If the primary is gone *and* the sync's process
restarted, the sync loops on `waitForUpstream` and `startTakeoverWithPeer`
returns `ErrDatabaseOffline` forever — the exact deadlock the commit fixed for
MariaDB, but on the controller's own datastore.

Secondary effect: every postgres standby restart re-copies the full database
even when its data dir is intact, which is what makes the seed window in
issue 1 so long.

### Change

1. Add `dataDirInitialized()` to postgres: `PG_VERSION` exists in `p.dataDir`
   **and** the directory was written by a standby or primary of this cluster
   (check `standby.signal` *or* a `flynn-sirenia-id` marker file we write
   alongside `postgresql.conf` containing `p.id`). Reject reuse if the marker
   is missing or from a different id, to avoid adopting a stranger's volume.

2. In `assumeStandby`, when `!p.running()`:
   - if `dataDirInitialized()`: skip `waitForUpstream` and basebackup; write
     config + `standby.signal`; `start()`. Postgres in recovery will retry
     `primary_conninfo` on its own until the upstream (or its replacement)
     appears, which is what takeover needs.
   - else: current path (wait for upstream, basebackup).

3. After starting on a reused data dir **and** only if the upstream is
   reachable, verify replication is progressing:
   `SELECT status FROM pg_stat_wal_receiver` = `streaming` within
   `standbyReplicationHealthTimeout` (30s), or `pg_last_wal_replay_lsn()`
   advancing. If the receiver reports a timeline/history mismatch (log line
   `requested timeline ... is not a child`), stop, wipe, and basebackup
   (mirrors `reseedStandbyFromUpstream`). Same caveats as issue 3 apply: do
   **not** reseed merely because the standby is behind.

4. When basebackup is required and the data dir is non-empty, wipe it
   *before* running `pg_basebackup` instead of relying on the failure →
   `RemoveAll` → retry loop (`process.go:487-494`). This removes one full
   retry cycle from every restart.

### Tests

Postgres tests need a real `postgres` binary (already the case for
`process_test.go`; CI runs them in Docker).

- `TestAssumeStandbyReusesInitializedDataDir` — seed a standby from a running
  primary, stop the standby process, kill the primary, call `Start()` on the
  standby with the dead upstream → returns nil within a few seconds, `Running()`
  is true, `XLogPosition()` is non-empty.
- `TestAssumeStandbyRejectsForeignDataDir` — data dir with `PG_VERSION` but a
  different id marker → falls back to basebackup path (wipe + copy).
- `TestAssumeStandbyResumesStreamingOnReuse` — reuse with live upstream →
  `pg_stat_wal_receiver.status == 'streaming'` without a basebackup (assert by
  checking a sentinel file in the data dir survived).
- `TestAssumeStandbyReseedsOnTimelineMismatch` — promote the standby, write on
  it, then demote it under the original primary → detects divergence and
  reseeds.
- Extend `pkg/sirenia/state/applyconfig_test.go` (already has the MariaDB-
  style deadlock cases from `32c39ba9`) with a postgres-flavoured fake whose
  `Start()` succeeds without upstream, asserting `startTakeoverWithPeer`
  proceeds.

---

## 3. Don't full-reseed a lagging MariaDB replica

**Files:** `appliance/mariadb/process.go`,
`appliance/mariadb/replication_health_test.go`

### Problem

`standbyReplicationHealthy` (`process.go:583-616`) returns `false` unless the
standby *catches up to the upstream GTID within 30s*. A replica that was
offline through a heavy-write window has IO/SQL threads running and its GTID
advancing, but will not be caught up in 30s. `assumeStandby` then calls
`reseedStandbyFromUpstream` (stop, `mariabackup` full copy, restart). During
the copy `Running=false`, which feeds issue 1.

### Change

1. Split the return into three states:

   ```go
   type standbyReplState int
   const (
       standbyReplFatal    standbyReplState = iota // 1236 etc → reseed
       standbyReplStuck                            // threads not running / position not advancing → reseed
       standbyReplHealthy                          // caught up OR advancing
   )
   func (p *Process) standbyReplicationState(upstream *discoverd.Instance) (standbyReplState, error)
   ```

2. Within the 30s window, sample `gtid_slave_pos` at start and end. Return
   `standbyReplHealthy` if caught up **or** if the position advanced and
   `Slave_IO_Running == Slave_SQL_Running == Yes`. Return `standbyReplStuck`
   only if threads are not both running at the end of the window *or* the
   position did not move at all while the upstream position is ahead.

3. Treat non-fatal SQL-thread errors (e.g. 1062 duplicate key, 1032 row not
   found) as `standbyReplStuck` — those *are* correct reseed triggers because
   the SQL thread will not recover on its own.

4. Keep `replicationCaughtUpWithUpstream` as-is (pure, already unit tested).

### Tests

- Extend `replication_health_test.go` with a table test over
  `classifyStandbyReplication(startPos, endPos, upstreamPos, ioRunning,
  sqlRunning, lastIOErrno, lastSQLErrno)` → state. Cases: caught up; behind
  but advancing; behind and not advancing; IO stopped; SQL stopped with 1062;
  fatal 1236.
- Make the polling loop injectable (`p.replCheckInterval`, `p.replHealthTimeout`)
  so the test above runs in milliseconds without a live MariaDB.
- Integration (existing `process_test.go` harness, needs `mariadb-backup`):
  `TestAssumeStandbyKeepsLaggingReplica` — pause the replica's SQL thread,
  write 1k rows to the primary, resume, call `Reconfigure` with the same
  upstream → no `mariabackup` invocation (assert via a sentinel file in the
  data dir).

---

## 4. Limit the "skip replication health check" to sync peers

**Files:** `appliance/mariadb/process.go` (and postgres after issue 2)

### Problem

`assumeStandby` skips the health check entirely when the upstream is
unreachable (`process.go:783-785`). That is the intended unblocker for the
**sync** about to take over. For an **async** on a reused volume with
incompatible GTID state (`MASTER_USE_GTID=current_pos` includes its own binlog
GTIDs), the IO thread later dies with 1236 and stays stopped, while
`Database.Running=true` makes it look healthy to every repair helper and to
`WaitForReplSync` — which then burns the full 15-minute `syncTimeout`.

### Change

1. Pass the role into `assumeStandby` (it has `p.config().Role` available via
   the pending config; thread it explicitly to avoid reading shared state).
2. If the upstream is unreachable and role is `async`: still start locally
   (do not block), but schedule a deferred re-check goroutine
   (`p.deferredReplCheck`) that polls upstream reachability every 10s for up
   to `syncTimeout`, then runs `standbyReplicationState`; on
   fatal/stuck it triggers `reseedStandbyFromUpstream` under `p.mtx`. Cancel it
   in `cancelSyncWait()` / `stop()` / next `reconfigure`.
3. If the role is `sync`: keep today's behaviour (skip; takeover will trigger a
   fresh `assumeStandby` for asyncs on the new generation).

### Tests

- `TestAssumeStandbyAsyncSchedulesDeferredReplCheck` — unreachable upstream,
  role async → `Running()` true immediately, deferred check registered;
  simulate upstream becoming reachable with divergent GTID → reseed invoked
  (mock `reseedStandbyFromUpstream` via a func field).
- `TestAssumeStandbySyncSkipsDeferredCheck` — same but role sync → no deferred
  check.
- `TestDeferredReplCheckCancelledOnReconfigure` — reconfigure to a new upstream
  before the check fires → goroutine exits (assert with a done channel).

---

## 5. Retire or narrow `repairSireniaClusters` (raw `Deposed` clearing)

**Files:** `host/cli/github_updater.go:1985-2035`,
`host/cli/host_restart_settle.go:73`, `updater/updater.go:371`

### Problem

The primary already re-adds present deposed peers as asyncs
(`pkg/sirenia/state/state.go:727-736`), so the helper is redundant. It writes
the whole state blob via `SetMeta` from outside the state machine on **every**
host restart, races the primary's CAS write (both sides recover, but it logs
errors), and sleeps 10s per appliance.

### Change

1. Move the helper to `pkg/updaterdeploy` (single copy, unit-testable) as
   `RepairDeposedSireniaPeers`.
2. Only clear entries whose peer **is present in discoverd** *and* has been
   deposed for longer than `deposedRejoinGrace` (2m, based on meta `Index`
   age or a timestamp we add to the state). Present-and-recent means the
   primary will handle it; absent means clearing does nothing useful.
3. Replace the fixed `time.Sleep(10s)` with a bounded wait (≤ 60s) for the
   cleared peers to appear in `state.Async`.
4. Remove the call from `settleAfterHostRestart`; keep it only in
   `updateImages` and `updater/updater.go` (once, after the rolling restart).

### Tests

- `TestRepairDeposedSireniaPeers_SkipsAbsentPeers`
- `TestRepairDeposedSireniaPeers_SkipsRecentlyDeposed`
- `TestRepairDeposedSireniaPeers_ClearsStalePresentPeers` — uses the existing
  `discoverdNewService` hook; asserts `SetMeta` payload has `Deposed == nil`
  and other fields untouched.
- Delete the duplicated copies and update `host/cli/update_helpers_test.go`
  if it references them.

---

## 6. Align inter-host settle timeouts

**Files:** `host/cli/github_updater.go:67-69,422,483`,
`host/cli/host_restart_settle.go`, `host/cli/host_restart_settle_test.go`

### Problem

`updateRemoteBinaries` waits 3m for the remote daemon and then 3m for
discoverd host count with `FatalClusterSize: true`. The comment at `:412-416`
says rejoin "can take ~1–3 minutes". Exceeding 3m aborts the whole rollout,
leaving mixed binaries.

### Change

1. Add `updateClusterSizeTimeout = 8 * time.Minute` next to
   `updateHealthTimeout` and use it in `settleAfterHostRestart` and
   `updateImages` instead of the literal `3*time.Minute`.
2. Make `waitForRemoteDaemon` timeout `updateRemoteDaemonTimeout = 5m`.
3. Before returning a fatal cluster-size error between hosts, do one more
   `waitForClusterHealthy` pass and re-check size — a host may be in discoverd
   but the status-web cache stale.
4. On fatal abort, print a resume hint listing which hosts were updated and
   the exact command to resume (`flynn-host update --all-nodes --images-only
   --version X`).

### Tests

- Extend `host_restart_settle_test.go` guard test to assert
  `updateClusterSizeTimeout >= 2*updateInterHostDelay` and
  `updateRemoteDaemonTimeout >= 3m`.
- `TestSettleAfterHostRestart_RetriesSizeOnceBeforeFatal` — inject a fake
  cluster client whose `Hosts()` returns N-1 for the first window then N.

---

## 7. Make `RepairStaleVolumes` robust to a not-yet-ready volume manager

**Files:** `pkg/updaterdeploy/repair_volumes.go`,
`pkg/updaterdeploy/repair_volumes_test.go`, `host/volume/api` (status field)

### Problem

A controller volume is marked destroyed if the host answers `ListVolumes` but
the ID is absent. If a freshly restarted host serves its API before its
volume manager finished restoring persisted state, a sirenia data volume can
be marked destroyed and the peer gets an empty volume on its next restart
(forcing a full reseed). Exposure is small (runs after the final settle) but
the failure is silent and costly.

### Change

1. Expose a `VolumesReady bool` in the host status response
   (`host.GetStatus()`), set once the volume manager has restored state.
2. In `hostVolumeIndex`, skip hosts whose status reports `VolumesReady ==
   false` (log at warn).
3. Require the volume to be absent on **two** `ListVolumes` calls at least
   10s apart before marking destroyed.
4. Never mark destroyed a volume whose controller record has a live `JobID`
   whose job is `up`/`starting` on that host — that is a running database.

### Tests

- `TestRepairStaleVolumes_SkipsHostsWithVolumesNotReady`
- `TestRepairStaleVolumes_RequiresTwoConsecutiveMisses`
- `TestRepairStaleVolumes_NeverDestroysVolumeWithLiveJob`

---

## Integration / cluster tests to add

**File:** `test/test_cluster_update.go` (extend the existing
`TestRollingSystemctlRestartThreeNode`) and a new `test/test_sirenia_recovery.go`.

1. **`TestRollingSystemctlRestartThreeNode_WithRestartingAsync`** — same as
   the existing test, but before the rolling restart `flynn-host stop` the
   postgres async job so it is mid-`pg_basebackup` when the updater's repair
   runs. Assert: no `DeleteJob` on that job id (check controller job events),
   the async reaches `Running` within `syncTimeout`, and the update completes.
   (Covers issue 1 and, after issue 2, seeds fast enough to be observable.)

2. **`TestPostgresSyncTakeoverAfterProcessRestart`** — 3-peer postgres;
   `flynn-host stop` the primary *and* the sync within 2s of each other; wait
   for the sync's replacement job. Assert a new generation is declared and the
   cluster returns read-write within 5m. Currently expected to **fail**
   (deadlock) — this is the regression test for issue 2.

3. **`TestMariaDBLaggingReplicaNotReseeded`** — 3-peer mariadb; stop the
   async's SQL thread via `flynn mysql`... (or `flynn-host stop` the async
   job), write ~50MB on the primary, restart the async. Assert the async's data
   dir sentinel survives (no `mariabackup`) and it catches up. (Issue 3.)

4. **`TestFlynnHostUpdateAllNodesThreeNode`** — drive the real
   `flynn-host update --all-nodes` from a local tarball (the existing tarball
   path) on a 3-node cluster with postgres + mariadb resources and one
   user app. Assert: all three hosts report the new version, every sirenia
   cluster has `2+asyncs == formation count` with all peers `Running`, and the
   user app serves 200 throughout (poll in a goroutine, fail on >30s
   continuous errors). This is the end-to-end coverage the branch currently
   lacks; today's 3-node test only does raw `systemctl restart`.

5. **Chaos variant (opt-in via env, not in default CI)**: same as 4 but the
   flannel subnet lease is forced to rotate on one host so DB peers re-register
   with new IPs mid-update. Exercises `refreshPrimaryDownstream` and the
   deployer's `samePeer` id-key matching.

---

## Rollout / sequencing

1. Land **1** first — it is self-contained, pure-Go-testable, and removes the
   active harm.
2. Land **3** and **4** together (both in `mariadb/process.go`).
3. Land **2** (largest change; postgres integration tests are slow — gate them
   behind the existing Docker unit-test job).
4. **5–7** as follow-ups; each is independent.
5. Add cluster tests 1–4 as each fix lands; test 2 should be committed
   *skipped* with the deadlock fix PR referenced, then un-skipped by that PR.

## Non-goals

- Changing the sirenia state-machine takeover semantics beyond the `Running()`
  gating already merged.
- Reworking the singleton deploy path (`deploySireniaSingleton`) — no issues
  found there.
- Mongo parity for issue 2 — mongod standbys reuse their data dir already;
  verify with a quick test but no change expected.
