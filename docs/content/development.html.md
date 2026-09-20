---
title: Development
layout: docs
toc_min_level: 2
---

# Development

This guide covers how this fork is built, tested, and released.

* Edit and build Flynn
* Run unit, integration, and Vagrant smoke tests
* Cut a GitHub release

## Development environment

The supported loop is an **Ubuntu 24.04** Vagrant VM named **builder**, plus
optional cluster VMs (`node1` … `nodeN`). The box is `bento/ubuntu-24.04`.
Install [Vagrant](https://www.vagrantup.com/) (≥ 1.9), [VirtualBox](https://www.virtualbox.org/),
and the `vagrant-disksize` plugin.

Do **not** run a bare `vagrant up`. That boots the builder and every cluster
node (heavy). See [Vagrant](installation/vagrant.md).

Clone this fork, then start only the builder:

```
$ git clone https://github.com/randy-girard/flynn.git
$ cd flynn
$ vagrant up builder
$ vagrant ssh builder
```

Inside the VM:

```
$ cd /root/go/src/github.com/flynn/flynn
```

The host repo is synced there. `setup.sh` (first provision) installs Go **1.24**,
Docker, ZFS, `ipset`, PostgreSQL for Flynn unit tests, and MariaDB/MongoDB/Redis
so Vagrant smoke can install those engines as plugins. GitHub Actions unit tests
only start PostgreSQL.

You can also work on a native Ubuntu 24.04 machine with the same packages.
macOS is fine for editing and for **Docker-wrapped unit tests**; it cannot run
ZFS, `flynn-host`, or the Vagrant smoke cluster.

Optional plugins live in sibling repos next to this checkout (`../flynn-plugin-redis`,
`../flynn-plugin-dashboard`, `../flynn-plugin-discovery`, `../flynn-plugin-www`,
`../flynn-plugin-otel`, `../flynn-plugin-scheduler`, …). Install them on a cluster host with
`flynn-host plugin:install` after bootstrap. See [Plugins](plugins.md).

Go builds use vendored modules (`GOFLAGS=-mod=vendor`). Match `gofmt -s`.
GitHub Actions, `script/run-unit-tests`, and Vagrant smoke all run
`util/commit-validator/validate-gofmt` and fail if changed Go is not formatted.

## Building

### Host binaries

For day-to-day Go work on the builder (or Linux):

```
$ make
```

That runs `script/build-flynn`. Binaries land in `build/bin`, image manifests in
`build/image`. `make clean` wipes them. `make release` stamps a git-derived
version. `flynn-test` / `flynn-test-file-server` are omitted unless you pass
`--test-binaries` or set `FLYNN_BUILD_TEST_BINARIES=1`.

### Cluster images

A full production platform build (squashfs layers for system apps and the CLI)
is `build.sh` on the builder. Layer squashfs is written on the VM’s local disk
(`/mnt` is a bind of a host temp dir); `build/log` and the release tarball
still land in the Vagrant share so you can inspect them from the laptop. It does **not** build cluster-test images
(`test`, `test-apps` including MinIO, `controller-examples`). Those are only
for the `test/` integration suite; add `./build.sh test` if you need them.
First time, or after Ubuntu/base-package changes:

```
$ ./build.sh --version vYYYYMMDD.N
```

That runs **base** (debootstrap + base squashfs) then **cluster**. If the base
layer already exists:

```
$ ./build.sh cluster
$ ./build.sh --version vYYYYMMDD.N cluster
```

Phases, in order: `prep` → `binaries` → `start` → `toolchain` → `apps` → `stop`.
CI splits those across jobs; locally they must stay on the same machine so
`/var/lib/flynn/layer-cache` and `build/images.json` carry forward.

`script/build-flynn` is the host-binary step inside that pipeline. It is not a
substitute for `build.sh` when you need installable cluster images or a smoke
tarball.

## Running a local cluster

After a binary (and, for a real cluster, image) build:

```
$ script/bootstrap-flynn
```

That stops any previous `flynn-host`, starts it again, and bootstraps Layer 1.
`--size N` creates extra virtual interfaces on one machine for a multi-node
layout. See `script/bootstrap-flynn -h`.

The last bootstrap lines include `flynn cluster:add …`. On Vagrant, host daemon
logs are `/var/log/flynn/flynn-host.log`, synced to `./flynn-logs/builder` on
the laptop.

## Debugging

```
$ less /var/log/flynn/flynn-host.log
$ journalctl -u flynn-host
$ flynn-host ps
$ flynn-host ps -a
$ flynn-host log $JOBID
$ flynn-host log dashboard
$ flynn-host inspect $JOBID
$ flynn-host stop $JOBID
```

Stopping jobs while the scheduler is up will respawn formations. Stop the
scheduler first if you need a quiet cluster.

For help on Discord, collect logs:

```
$ flynn-host collect-debug-info --tarball
```

`--tarball` writes a local archive. The default gist upload is optional.

## Tests

There are several layers. CI on pull requests to `main` runs
**gofmt**, **bats**, and the **Linux unit suite**. Integration tests and Vagrant
smoke are local (or a dedicated machine). They are the right gate for scheduler,
network, datastore, upgrade, and CLI behavior.

### gofmt

```
$ util/commit-validator/validate-gofmt
```

CI, `make test-unit` / `script/run-unit-tests`, and
`script/vagrant-upgrade-smoke.sh` all run this check. It compares against the
PR base (or `origin/main` locally) so you do not fail on unrelated
historical drift. `FLYNN_TEST_SKIP_CHECKS=1` skips bats only; gofmt still runs.

Install the same check as **pre-commit** and **pre-push** hooks so unformatted
Go never leaves the clone:

```
$ make install-git-hooks
```

That copies `script/githooks/gofmt-check` into `.git/hooks/` (no `git config`
changes). Git does not enable committed hooks automatically; run the installer
once per clone.

### bats (shell)

```
$ bats script/test
```

Covers installer/git/curl/release helper scripts. CI installs `bats` and runs
this before `go test`.

### Unit tests (Go)

```
$ make test-unit
```

`script/run-unit-tests` chooses the runner:

| Host | What runs |
| --- | --- |
| Linux (builder, CI) | Native `make test-unit-root-native`: all packages with `-race`, then `sudo go test ./host/volume/...` (ZFS) |
| macOS / Windows | Docker image `flynn-unit-tests:24.04` with the Linux appliance deps |

Force Docker on Linux with `FLYNN_TEST_DOCKER=1`. Skip ZFS volume tests with
`FLYNN_SKIP_VOLUME_TESTS=1`. Extra `go test` flags: `FLYNN_GO_TEST_FLAGS`.

Unit tests write Go coverage under **`coverage/`** (gitignored): `coverage.out`,
an overview at `coverage/index.html` grouped by package area, and one HTML
page per source file under `coverage/files/`. Open `coverage/index.html` in a
browser. Set `FLYNN_SKIP_COVERAGE=1` to skip the report.

On the builder, MariaDB/MongoDB/Redis stay installed for plugin smoke, not for
Flynn `go test`. Package-level:

```
$ go test -mod=vendor ./router
$ go test -mod=vendor ./controller/...
```

Do not use Docker Desktop as the Linux ZFS gate. Nested Docker cannot load ZFS;
the builder VM (or CI’s Ubuntu runner) can.

`make test` is `test-unit` plus `test-integration`. Skip the second with
`SKIP_INTEGRATION_TESTS=1`.

### Smoke script regressions

These are fast host checks. They do **not** boot VMs. They assert that
`script/vagrant-upgrade-smoke.sh` still contains the behaviors we care about
(datastores, overlay isolation, dockerbuilder, membership, image-slim, …):

```
$ bash script/test-vagrant-smoke-cli-functions.sh
# or all of them:
$ for s in script/test-vagrant-smoke-*.sh; do bash "$s" || exit 1; done
```

Related one-off script tests: `script/test-apt-retry.sh`,
`script/test-release-notes.sh`. The Vagrant smoke driver runs every
`script/test-vagrant-smoke-*.sh` before it starts VirtualBox.

### Integration tests

Full-stack Go tests live in `test/` (not `tests/`). They need a cluster.

```
$ script/run-integration-tests
```

That builds Flynn (including `flynn-test` host binaries), bootstraps a cluster
(`script/bootstrap-flynn`), and runs `bin/flynn-test`. Filter:

```
$ script/run-integration-tests -f 'RouterSuite\\.TestAdditionalHttpPorts'
$ script/run-single-integration-test.sh …
```

`--size` / `-n` boots more than one nested host. This path expects Linux with
nested containers/KVM and is much slower than `make test-unit`. Details:
[test/README.md](../../test/README.md).

### Vagrant upgrade smoke

This is the cluster acceptance suite used for upgrades, datastores, Docker
deploys, overlay isolation, and node join/drain. Run it from the **repo root on
the laptop** (it drives Vagrant):

```
$ script/vagrant-upgrade-smoke.sh
```

Default flow:

1. **Host gate** — all `script/test-vagrant-smoke-*.sh`, then a set of
   Darwin-safe `go test` packages (`./cli/`, `./pkg/netpolicy/`, sirenia,
   updater, …) plus `test/apps/upgrade-smoke`. Failures stop before VMs.
2. **Builder gate** — `vagrant up builder`, then the full native Linux unit
   suite via `script/run-unit-tests` on the VM (Redis, Postgres, MariaDB,
   MongoDB, ZFS). Failures stop before cluster nodes.
3. **Build** — cluster images on the builder, tarball in `build/release/`.
4. **Topologies** — default `SMOKE_TOPOLOGIES=1,3` (singleton, then 3-node HA).
   Size `2` is invalid. Named topologies: `add` (join `node4` then upgrade),
   `remove` (drain `node3` while HTTP and DBs keep working), and `discovery`
   (singleton, install the discovery plugin, join `node2`+`node3` via the
   in-cluster discovery API, wait for postgres/MariaDB/MongoDB to promote to
   HA, then deploy). After upgrades, every topology including `discovery`
   takes a cluster backup and restores with `--from-backup`.
5. On each topology: install the tarball with `--peer-ips` (or `--discovery`
   on extra nodes in the `discovery` topology), bootstrap with `/etc/hosts` for `CLUSTER_DOMAIN`,
   install every first-party plugin (`PLUGIN_SMOKE_APPS`: redis, mysql,
   mongodb, kafka, clickhouse, dashboard, www, discovery, otel, scheduler), start a dummy
   OTLP/HTTP sink on the host and confirm the otel plugin POSTs `/v1/metrics`,
   deploy
   `test/apps/upgrade-smoke` against every datastore provider, `git push`
   `test/apps/upgrade-smoke-docker` on the **container** stack, `flynn
   docker:push` a pre-built image of the same Dockerfile, probe HTTP and
   rows, exercise `flynn` / `flynn-host`, create a persistent volume, write a
   file, read it after a job restart, and delete the volume, then `flynn-host update --all-nodes
   --tarball --force` twice and re-verify. After that, `flynn-host backup`,
   wipe Flynn (`install --clean`), `flynn-host bootstrap --from-backup`, and
   re-verify the slug/Dockerfile git-push/docker-push apps plus postgres/mysql/mongodb data. Installed
   plugins restore with postgres (`plugins.json` is the inventory; do not
   `plugin:install` again). Redis, Kafka, and ClickHouse volume data is not
   in the cluster backup; those engines must come back empty.

Logs: `./flynn-logs/{builder,node*}`. Cleared at start unless `KEEP_LOGS=1`.

Useful environment:

| Variable | Meaning |
| --- | --- |
| `SMOKE_TOPOLOGIES=1,3,5,add,remove,discovery` | Which layouts to run |
| `SKIP_UNIT_TESTS=1` | Skip host + builder unit gates |
| `SKIP_BUILD=1` | Reuse `build/release/flynn-${BUILD_VERSION}.tar.gz` |
| `SKIP_UPGRADE=1` | Install and verify only |
| `SKIP_BACKUP=1` | Skip cluster backup + `--from-backup` restore |
| `SKIP_CLI=1` | Skip live CLI probes |
| `KEEP_VMS=1` / `KEEP_VMS_ON_FAIL=1` | Leave VMs up |
| `SMOKE_DETAIL=1` | Stream command output |
| `RESUME_AT=bootstrap` or `upgrade` | Continue a partial run |
| `PLUGIN_SMOKE_APPS` | Plugins to install after bootstrap (default: redis mysql mongodb kafka clickhouse dashboard www discovery otel scheduler) |
| `VAGRANT_MEMORY` / `BUILDER_MEMORY` | VM RAM (MB) |

The smoke header in `script/vagrant-upgrade-smoke.sh` lists the rest.

## CI

* **[Unit tests](https://github.com/randy-girard/flynn/actions/workflows/unit-tests.yml)**
  — `push` and `pull_request` to `develop` and `main`, on `ubuntu-24.04`.
  Builds host binaries, `validate-gofmt`, `bats script/test`,
  `make test-unit-root-native`.
* **[Build and Release](https://github.com/randy-girard/flynn/actions/workflows/release.yml)**
  — manual `workflow_dispatch` only. Builds base + production cluster images in
  phases and publishes GitHub Release assets. Version tags look like
  `vYYYYMMDD.N` (UTC date, then `.0`, `.1`, … for that day). Leave **version**
  empty to pick the next unused tag; fill it in only to override. Omits `test`,
  `test-apps`, and `controller-examples`. Release files are uploaded one at a
  time onto a draft (with retries) so the job logs progress and can resume
  after a cancelled run; the release is published only after every asset is
  present unless you asked for a draft. **draft** is off by default.
  Packaging copies only squashfs layers listed in `images.json` and **fails**
  if any listed layer is missing from `/var/lib/flynn/layer-cache` (so a
  metadata-only builder cache hit cannot ship a GitHub Release that 404s on
  `flynn-host update`).
  **dispatch_plugins** is on by default so plugin builds are queued after the
  Flynn release exists (`GITHUB_TOKEN` cannot start other workflows from
  `release` events). Uncheck it to skip fan-out.
* **[Dispatch plugin releases](https://github.com/randy-girard/flynn/actions/workflows/plugin-releases.yml)**
  — runs from Build and Release when **dispatch_plugins** is on, when a
  Flynn GitHub Release is **published** from the GitHub UI, or via its own
  `workflow_dispatch`. It queues each plugin repo’s `Build and Release`
  workflow with plugin version `{Flynn tag}.0` and `flynn_version` set to the
  Flynn tag so ubuntu-noble matches Flynn. Plugin-only rebuilds use
  `vYYYYMMDD.N.P` (last number increments) without a new Flynn release.
  Plugin jobs publish only the overlay delta; hosts fetch Flynn ubuntu-noble
  from the Flynn GitHub Release (`flynn.plugin.base`). A published plugin
  release is skipped on re-dispatch; drafts and failed uploads are retried.
  Plugin names for CI dispatch are not hardcoded: set Actions variable
  `PLUGIN_RELEASE_REPOS` (`owner/repo` per line) and secret
  `PLUGIN_RELEASE_TOKEN` (Actions: write + Contents: read on those repos).
  `flynn-host plugin:install` short names come from
  `pkg/plugin/official-plugins.json`. Empty variable skips dispatch.

## Pull requests

Target **`main`**. Use Conventional Commit subjects and plain `git commit` (no
DCO sign-off). Include tests, or explain why not.

* Pure Go / CLI: `make test-unit` (and gofmt) is the minimum
* Scripts under `script/`: bats and/or the matching `script/test-*.sh`
* Cluster, overlay, datastores, dockerbuilder, upgrades, membership, backup/restore: run
  `script/vagrant-upgrade-smoke.sh` (narrow with `SMOKE_TOPOLOGIES` if needed)

See [Contributing](contributing.md).

## Releasing

Build images on the builder (or via the release workflow), then package:

```
$ ./build.sh --version vYYYYMMDD.N cluster
$ ./script/release --version vYYYYMMDD.N --target tarball
$ ./script/release --version vYYYYMMDD.N --target github
```

Cluster-test images are not in that pipeline. Build them only when running `test/`:

```
$ ./build.sh --version vYYYYMMDD.N test
```

`script/release` defaults to a local tarball. GitHub needs `gh` authenticated
against `randy-girard/flynn`. Prefer the Actions workflow for production
assets; it already splits toolchain vs app image builds.

Release notes list **only the commits between the previous `v*` tag and this
one**, by CalVer order, not `git log` on the current branch. Topic-branch
commits that are not in that tag range stay out of the GitHub release body.

Install a built release with
[manual installation](installation/manual.md) or:

```
$ curl -fsSL https://github.com/randy-girard/flynn/releases/latest/download/install-flynn | sudo bash
```
