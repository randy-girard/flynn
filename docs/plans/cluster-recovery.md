# Cluster recovery after host restart / flannel subnet change

This documents operational recovery patterns validated on a multi-node Flynn
cluster after `flynn-host` restarts, flannel subnet changes, and sirenia state
drift.

## Symptoms

- Status dashboard shows databases unhealthy (not read-write)
- `controller-scheduler` / `controller-worker` / `tarreceive` missing from discoverd
- Jobs appear `running` on the host but are absent from discoverd
- Containers cannot resolve `*.discoverd` or reach `controller.discoverd`
- Sirenia sync peers stuck `unassigned` with stale upstream addresses

## Root causes

1. **Empty `job_id` in persisted network/discoverd config** — after `flynn-host`
   restart, jobs block until `ConfigureNetworking` / `ConfigureDiscoverd` run.
   Fix: `host/fixer/host_backend.go` (`FixHostBackend`) and
   `host/libcontainer_backend.go` (`UnmarshalState` re-applies when subnet/url
   present).

2. **Discoverd DNS bound to old bridge IP** — after a flannel subnet change,
   discoverd may keep listening on the previous `flynnbr0` address (e.g.
   `100.100.38.1`) while the bridge moved (e.g. `100.100.83.1`). Containers on
   that host then fail to resolve `controller.discoverd`. Fix: restart discoverd
   on affected hosts, or run `flynn-host fix` (with `FixHostBackend` DNS
   mismatch detection).

   **Discoverd peer URL missing `http://`** — persisted host state on some nodes
   stores `192.168.56.20:1111,...` without a scheme. `FixHostBackend` now
   normalizes URLs; `ConfigureDiscoverd` also refreshes the `DISCOVERD` default
   env for new jobs.

3. **Stale overlay IPs** — jobs keep host status `running` at old flannel
   addresses and never re-register in discoverd. Fix: restart the job with a new
   ID on the same data volume; update discoverd sirenia meta; bump `generation`.

4. **Sirenia role drift** — discoverd meta is correct but peers retain stale
   `database.config` (wrong sync/async). Fix order:
   - Align meta: one primary + one sync, `async=[]`, `deposed=[]`
   - Bump `generation` in meta (forces `evalInitClusterState`)
   - Restart primary if downstream is still stale
   - Restart sync with a fresh data volume if it assumed the wrong role

5. **Duplicate schedulers** — recovery spawns overlapping schedulers. Fix:
   `ensureSingleScheduler` in `host/fixer/controller.go`.

## Recovery checklist

```bash
# 1. Verify DNS on each node (must match flynnbr0 gateway)
vagrant ssh nodeN -c 'ip addr show flynnbr0 | grep inet; ss -ulnp | grep :53'

# 2. Run fix (do not run concurrently with manual sirenia surgery)
flynn-host fix -n 3

# 3. For a broken sirenia database (postgres/mariadb/mongodb):
#    - GET instances + meta from discoverd
#    - PUT meta with live primary/sync, clear async/deposed, increment generation
#    - Verify RW via sirenia status (postgres: port+1, mariadb: +1, mongodb: +1)

# 4. Verify cluster
curl -u "$AUTH_KEY:" http://<router-ip>/status
```

## Code regressions prevented by tests

| Area | Test |
|------|------|
| Primary downstream refresh after sync replacement | `TestPrimaryRefreshDownstreamOnSyncReplacement` in `pkg/sirenia/state` |
| Discoverd DNS derivation from subnet | `TestDNSFromSubnet` in `host/fixer` |
| Discoverd URL scheme normalization | `TestNormalizeDiscoverdURL` in `host/fixer` |
| Stale overlay IP detection | `TestJobIPOnSubnet` in `host/fixer` |
| Unassigned peer assumes recorded sync | `evalClusterState` change in `pkg/sirenia/state/state.go` |

## Manual meta update pattern (discoverd)

```python
# Follow redirects on PUT; include meta index for CAS
state["primary"] = <live primary instance>
state["sync"] = <live sync instance>
state["async"] = []
state["deposed"] = []
state["generation"] += 1
PUT /services/<db>/meta {"index": meta_index, "data": state}
```

**Important:** meta PUT may 307-redirect to the discoverd leader; use `curl -L`.
Update meta *before* starting the replacement primary job so sirenia does not
mark it `force_stop`. Stop rogue mongodb jobs on other nodes (especially node3
spurious instances) before starting a node2 primary with the data volume.

## MongoDB on node2 (container egress)

If mongodb jobs on node2 exit with `context deadline exceeded` registering in
discoverd (`Put http://192.168.56.x:1111/services/mongodb`), the host may reach
discoverd but containers on `flynnbr0` cannot. Check:

```bash
# On node2 — NAT must match current subnet (not the old 100.100.38.0/24)
sudo iptables -t nat -S POSTROUTING | grep flynnbr0
ss -ulnp | grep discoverd   # DNS should be on 100.100.83.1:53

# discoverd URL must include http://
curl -u "$AUTH_KEY:" http://127.0.0.1:1113/host/status | jq .discoverd
```

## Known limitations

- `flynn-host v20260709.3` must not be deployed; it breaks new container starts
  (`getting pipe fds for pid 0`).
