# Flynn environment reference

## Workspace

`/Users/rgirard/Documents/FlynnWorkspace` is not a git repo. Child directories with `.git` are independent. GitHub repos use `origin` = `github.com/randy-girard/<name>`, branch `main`.

| Path | What it is |
| --- | --- |
| `flynn/` | PaaS. Go 1.24, `-mod=vendor`. Ubuntu 24.04 is the host OS the code targets. |
| `flynn-plugin-*` | One plugin per repo. Go 1.24, `-mod=mod`. Do not `replace` Flynn. |
| `flynn-enterprise-license` | Private key issuer. No GitHub Actions. |
| `flynn-resource-demo` | App pushed to the cluster git, not GitHub. |
| `flynn-cursor-agent-test` | App pushed to the Linode Flynn git, not GitHub. |
| `.plans/` | Local plans. Not committed. |
| `.worktrees/` | The only place for extra checkouts. |

## Dev cluster commands

From `flynn/`:

```bash
script/vagrant.sh --yes status
script/vagrant.sh --yes --force-build update
script/vagrant.sh --yes plugin postgres
script/vagrant.sh --yes probe
script/vagrant.sh --yes chaos
make vagrant-setup YES=1 NODES=3
```

`update` builds on `dev-builder` when source changed, halts the builder, then updates running nodes. Plugin images are a separate `plugin` subcommand.

`update` returns only when the cluster is at full strength: postgres peers caught up and every service at its formation count (`UPDATE_STABLE_TRIES`, default 180 five-second tries), then the full probe. `reload` refuses to reboot any cluster node unless the same check passes first (`RELOAD_PREFLIGHT_TRIES`, default 60) and the tenant app list can be read. A halted or unbootstrapped cluster skips that gate. To reboot a cluster that is already broken, set `FLYNN_VAGRANT_RELOAD_SKIP_PREFLIGHT=1`.

`dev-builder` has the `flynn-default` ZFS pool and no `/dev/kvm`. Nested hardware virtualization does not change that. Do not run `script/run-integration-tests` on it. A full run spends 15 minutes per nested host and ends with `timed out waiting for hosts to start`. Filter to suites that do not call `bootCluster` if you need the builder's own cluster. `make test-integration` belongs on a Linux host that has `/dev/kvm`.

Chaos defaults: `CHAOS_ROUNDS=10`, `CHAOS_TIMEOUT=900`, `CHAOS_SETTLE=20`, `CHAOS_NODE_DOWN=60`, `CHAOS_BUDGET="tenant-job:tenants=0 datastore-replica:tenants=0"`, `CHAOS_MAX_SKIPPED` unset. `CHAOS_SETTLE_TRIES=120`. An all-skipped run, a recovery over `CHAOS_TIMEOUT`, or a broken budget fails the run. After every round and once more at the end, chaos waits for full strength with the reload/update gate (`wait_cluster_stable <label> CHAOS_SETTLE_TRIES 1`: postgres peers healthy, formation counts, hosts) and fails with `FAILED: cluster did not return to full strength after chaos: <reason>` (or `after round <n> <fault>`) when it does not get there, so the next command starts on a full-strength cluster. `datastore-replica` only stops database peers (discoverd instances of the datastore service minus the leader). Report: `flynn-logs/chaos/<UTC>/report.txt`.

Laptop CLI talks to cluster `local` (`https://controller.1.localflynn.com`). Status UI: `https://status.1.localflynn.com`.

## Smoke

```bash
script/vagrant-smoke.sh --item quick
make vagrant-smoke ITEM=quick
make test-vagrant
make test-scripts
```

`make test-vagrant` runs `script/vagrant/test/*.sh` and does not boot VMs. `make test-scripts` adds `script/test-*.sh`; GitHub Actions and `script/run-unit-tests` run it, and checks on missing `../flynn-plugin-*` siblings print `skip … not checked out`. Smoke step logs land under `$TMPDIR/flynn-upgrade-smoke.*/last-step.log`. `make vagrant-smoke-destroy` deletes smoke VMs and leaves `./build`.

## Unit tests

Flynn on macOS (no tty):

```bash
script -q /tmp/flynn-unit-script.log script/run-unit-tests
```

Flynn on Linux, or inside the container: `make test-unit` runs natively. CI runs gofmt, bats, and `make test-unit-root-native` (includes ZFS).

Plugins:

```bash
./script/run-unit-tests
```

CI for plugins is `.github/workflows/ci.yml`: gofmt, release notes, compose contract, `go test ./...`, and for dashboard also `npm ci && npm test && npm run build` in `web/`. A coverage-badge job then commits `ci: update coverage badge [skip ci]`.

## Plugin e2e

Needs a cluster that already has the plugin installed.

```bash
cd flynn && go build -mod=vendor -o /tmp/flynn-cli ./cli
cd ../flynn-plugin-postgres && FLYNN=/tmp/flynn-cli cluster/run.sh --cli-only
```

`--web-only` is Playwright. Do not guess `DASHBOARD_PASSWORD`.

## GitHub Actions

```bash
gh run list --branch main --limit 3
gh run watch <run-id> --exit-status
```

Push rejection after someone else (or the badge bot) moved `main`:

```bash
git pull --rebase origin main
git push origin HEAD
```

Never `git push --force`.

## Project board

User `randy-girard`, project number 4, node `PVT_kwHOAAFnDs4BkGcp`. Status field `PVTSSF_lAHOAAFnDs4BkGcpzhi4RP8`.

| Status | Option id |
| --- | --- |
| Todo | `f75ad846` |
| In Progress | `47fc9ee4` |
| Done | `98236657` |

Re-read the field with `gh` if a mutation says the option id is unknown.

## Plugin task auth

Appliance admin routes require `CONTROLLER_KEY`. Isolated database releases must not receive it. The CLI copies `CONTROLLER_KEY` and `AUTH_KEY` from the plugin app release into a job only when the argv is a `flynn-*-api task`. `psql` and `redis-cli` do not get the key. Do not route those tasks onto the plugin app: the controller refuses `flynn run` there without host auth.
