# Vagrant multi-node cluster debugging (agent playbook)

Operational guide for investigating Flynn cluster issues on the local
Vagrant node1/node2/node3 setup. Written from postgres startup investigations (2026-07-10): an overlay
connectivity failure on first install, then a successful rebuild with slow
bootstrap timing and a `synchronous_standby_names` config reload bug.

## Environment layout

| Resource | Location / access |
|----------|-------------------|
| Host logs (synced from VMs) | `./flynn-logs/node1`, `node2`, `node3` |
| SSH to nodes | `vagrant ssh node1` (also node2, node3) |
| Discoverd (host IP) | `http://192.168.56.20:1111` (node1), `.21` (node2), `.22` (node3) |
| Flynn repo in VM | `/root/go/src/github.com/flynn/flynn` (synced) |
| Overlay subnets (example) | node1 `100.100.68.0/24`, node2 `100.100.3.0/24`, node3 `100.100.93.0/24` |

Log files under `flynn-logs/` are named by logaggregator app ID (UUID). Common
patterns:

- `flynn-host.log` — host daemon on each node
- `3cb1fde5-…` — discoverd app logs (all nodes)
- `8d39d117-…` — flannel app logs (all nodes)
- `df98658c-…` / `6d642df4-…` — postgres app logs (all nodes; UUID changes per install)

## Quick health checks

```bash
# Postgres sirenia meta (roles, generation)
curl -s http://192.168.56.20:1111/services/postgres/meta | python3 -m json.tool

# Postgres instances (overlay addresses)
curl -s http://192.168.56.20:1111/services/postgres/instances | python3 -m json.tool

# Primary status (status port = postgres port + 1, e.g. 5433)
curl -s http://100.100.3.2:5433/status | python3 -m json.tool

# Per-node bridge / flannel
vagrant ssh node2 -c 'ip addr show flynnbr0; ip addr show flannel.1; ip route | grep 100.100'

# Cross-node overlay TCP (from node1 to node2 primary)
vagrant ssh node1 -c 'nc -zv -w3 100.100.3.2 5433'
```

Healthy postgres sirenia cluster:

- discoverd meta has one `primary`, one `sync`, optional `async` peers
- primary `/status` shows `"running": true` and eventually `"read_write": true`
- sync/async peers reach upstream status on port+1

## Postgres startup failure pattern (observed)

### Symptoms

- Cluster install completes but postgres never becomes read-write
- `flynn-logs/node*/df98658c-*.log` shows sync/async peers stuck in
  `waitForUpstream` with repeated:
  `error getting upstream status … dial tcp …:5433: i/o timeout`
- Primary log (e.g. node2) shows `waiting for downstream replication to catch up`
  and endless `no replication status` (sync never connected)

### Example timeline (healthy vs broken)

1. node2 leader creates cluster state → `assuming primary role`
2. node1/node3 assigned `sync` / `async` → `starting up as standby`
3. **Broken:** standby cannot reach `http://<primary-ip>:5433/status`
4. Primary stays `read_write: false` until sync replicates

### Log grep shortcuts

```bash
# Errors across postgres logs
rg -i 'lvl=eror|upstream|waitForUpstream|read_write' flynn-logs/node*/df98658c*.log

# Host bootstrap order (network vs discoverd)
rg 'ConfigureNetworking|ConfigureDiscoverd|configuring network' flynn-logs/node*/flynn-host.log

# Flannel subnet leases
rg 'Subnet lease|Subnet added' flynn-logs/node*/8d39d117*.log
```

## Bootstrap ordering (flynn-host)

Fresh install sequence on each node (from `flynn-host.log`):

1. `POST /host/jobs/…` — discoverd job
2. `ConfigureDiscoverd` (URL only, `dns=` empty)
3. flannel job starts (blocks on `networkConfigured` unless host-network)
4. `ConfigureNetworking` — creates `flynnbr0`, iptables NAT, closes `networkConfigured`
5. `ConfigureDiscoverd` again — URL + `dns=<bridge-ip>:53`, then `ConnectLocal`

Jobs without `HostNetwork` wait on `networkConfigured` then `discoverdConfigured`
before obtaining an overlay IP (`host/libcontainer_backend.go` `Run`).

**Important:** `discoverdConfigured` must only close after discoverd is actually
usable (`ConnectLocal` in `host/discoverd.go` calls `SetDefaultEnv("DISCOVERD")`).
Closing it earlier lets jobs proceed before the bridge exists.

## Regression in local changes (2026-07-10)

### Root cause: premature `discoverdConfigured` (fixed)

`host/http.go` had been changed to call `SetDefaultEnv("DISCOVERD", config.URL)`
inside `ConfigureDiscoverd`. That closes `discoverdConfigured` on the **first**
discoverd notify (URL only, before DNS and before `ConfigureNetworking`).

Previously (and restored behaviour):

- `SetDefaultEnv("DISCOVERD")` runs only in `ConnectLocal` after **both** URL
  and DNS are set — i.e. after overlay networking exists.

This ordering bug can leave the cluster in a state where sirenia peers register
in discoverd with overlay IPs but cross-node connectivity to the postgres
status API (`:5433`) fails during initial replication setup.

**Fix:** remove `SetDefaultEnv` from `ConfigureDiscoverd`; keep it in
`ConnectLocal` only.

### Other local changes — not implicated in fresh install

| Change | Verdict |
|--------|---------|
| `host/libcontainer_backend.go` `UnmarshalState` | Restart/recovery only; fresh install logs show `no stored global backend config` |
| `pkg/sirenia/state` `refreshPrimaryDownstream` | Recovery after sync replacement; unit tests pass |
| `appliance/postgresql/process.go` `--checkpoint=fast` | Speeds pg_basebackup after upstream reachable; not bootstrap delay |
| `appliance/postgresql/process.go` sync name quoting | Pre-existing UUID quoting bug; fixed in template (see below) |
| `pkg/postgres/postgres.go` `sireniaMetaReady` | Client wait helper; not postgres appliance startup |
| `controller/worker/deployment/sirenia.go` sync timeout | Longer deploy wait; does not break connectivity |
| `host/fixer/*`, `pkg/cluster/host.go` | Recovery / tooling; not bootstrap path |

Run regression tests after host/sirenia edits:

```bash
go test ./pkg/sirenia/state/... ./pkg/postgres/... ./host/fixer/...
```

## Rebuild and reinstall after host fix

```bash
# From repo root on builder/host — follow your usual install path, e.g.:
script/build-host  # or your local equivalent
# Re-run cluster install on node1/2/3
```

Then confirm postgres meta, cross-node `nc` to `:5433`, and `read_write: true`
on the primary status endpoint.

## Successful install — bootstrap timing (2026-07-10, app `6d642df4-…`)

After fixing the `ConfigureDiscoverd` ordering bug, a fresh 3-node install
completed with working overlay connectivity and postgres replication. Bootstrap
felt slow (~1 minute to a 3-node postgres cluster); the logs break that down into
expected phases rather than a sirenia/postgres regression.

### Timeline (UTC, from `flynn-host.log` + postgres app logs)

| Phase | node2 (primary) | node3 (sync) | node1 (async) |
|-------|-----------------|--------------|---------------|
| Host schedules postgres job | 21:37:58 | 21:38:25 | 21:38:50 |
| Container running (after layer materialization) | 21:38:25 | 21:38:50 | 21:39:16 |
| Sirenia peers visible | peers=1 @ 21:38:25 | peers=2 @ 21:38:50 | peers=3 @ 21:39:16 |
| Cluster action | init cluster @ 21:38:50 | assume sync, basebackup | assume async, basebackup |
| Replication caught up | sync in ~102ms @ 21:38:51 | — | basebackup ~350ms |

End-to-end: first postgres container at **21:38:25** → async peer replicating
at **21:39:17** (~52s). Controller/web traffic on postgres from **21:39:33**.

### Why it feels slow (not a local-code regression)

1. **Image layer materialization (~17–27s per postgres job)** — postgres image has
   3 squashfs layers; `flynn-host.log` shows `materializing image layers` from
   job start until `mounting root overlay` (e.g. node2: 21:37:58 → 21:38:24).
   First install on a node pays full ZFS volume + squashfs mount cost; later jobs
   on the same node reuse cached layers and start faster.

2. **Staggered controller deploy (~27s between nodes)** — postgres jobs are
   scheduled node2 → node3 → node1, not in parallel. Each waits on the previous
   host accepting the job plus its own materialization.

3. **Sirenia waits for ≥2 peers before `startInitialSetup`** — node2 sat at
   `peers=1` from 21:38:25 until node3 registered at 21:38:50 (~25s). This is
   normal: `evalClusterState` only calls `startInitialSetup` when the leader sees
   `len(peers) > 1` (`pkg/sirenia/state/state.go`).

4. **Async is third** — the async replica cannot start until all three postgres
   peers are in discoverd (~26s after sync).

Actual postgres init (`initdb`, primary start, pg_basebackup, sync catch-up) took
**~1 second** once two peers were present. The `--checkpoint=fast` change in
local `process.go` helps basebackup latency; it is not the cause of the long wait.

The **15-minute** deploy sync timeout change in `controller/worker/deployment/sirenia.go`
only affects how long the deploy worker waits when stuck; it does not slow a
healthy bootstrap.

### Other log noise (benign)

- **`error getting postgres info` / `postgres is not running`** during initdb on
  the primary — status polls while postgres is not yet up; expected.
- **`job_volumes_volume_id_fkey`** errors in controller logs during parallel job
  scheduling — transient FK races; cluster continued starting apps.

## `synchronous_standby_names` SIGHUP error (pre-existing bug, fixed)

### Symptom (node2 primary log)

After sync downstream caught up, postgres logged:

```
received SIGHUP, reloading configuration files
parameter "default_transaction_read_only" removed from configuration file, reset to default
invalid value for parameter "synchronous_standby_names": "0ed77f6c-a2d1-461e-8f7d-09fd96fbeb3a"
DETAIL: syntax error at or near "ed77f6c"
configuration file contains errors; unaffected changes were applied
```

### Cause

When sync catches up, `waitForSync` with `enableWrites=true` rewrites
`postgresql.conf` and sends SIGHUP (`appliance/postgresql/process.go`). The
template had:

```
synchronous_standby_names = '{{.Sync}}'
```

PostgreSQL GUC parsing treats a UUID (hyphens, leading digit) as invalid unless
the standby name is **double-quoted** inside the single-quoted value, e.g.:

```
synchronous_standby_names = '"0ed77f6c-a2d1-461e-8f7d-09fd96fbeb3a"'
```

This is intermittent (~1/16 installs when the sync peer UUID starts with `0`) and
**not introduced by recent uncommitted changes** (only `--checkpoint=fast` was
added locally; the template line was unchanged).

### Impact on this install

- `default_transaction_read_only` **was** cleared → primary became read-write
  (`isReadWrite()` checks `SHOW default_transaction_read_only`).
- `synchronous_standby_names` **was not** applied → `synchronous_commit =
  remote_write` may not actually wait for the named sync standby until config is
  fixed and reloaded.
- Replication and client connections still worked; overlay paths were healthy.

**Fix:** quote sync UUIDs in the config template (see `process.go`
`configTemplate`).

## Related docs

- [cluster-recovery.md](./cluster-recovery.md) — sirenia/discoverd recovery after
  restarts and subnet changes
