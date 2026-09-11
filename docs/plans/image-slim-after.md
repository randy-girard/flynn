# Image size after report

Changes on branch `slim-cluster-images`. Compare with
`docs/plans/image-slim-before.md`.

Measured after a successful `script/vagrant-upgrade-smoke.sh` builder step
(`./build.sh --version v20260911.0-smoke`, 2026-09-11). Reproduce with:

```text
script/report-image-sizes.sh build/images.json
```

Smoke also tees this to `build/image-size-report.txt` on the builder.

## Measured squashfs sizes

Unique layers (shared bases counted once): **53 layers, 2 954 657 792 bytes (2.8 GiB)**.
Release tarball: `flynn-v20260911.0-smoke.tar.gz` **2.8G**.

`layer_sum` is the sum of that image's layers (shared bases appear in many rows).

| image | layers | layer_sum | notes vs before |
|---|---:|---:|---|
| ubuntu-noble | 1 | 199.6 MiB | cloud packages purged; zstd; man/doc/info dirs kept for dpkg |
| busybox | 1 | 1.8 MiB | zstd |
| blobstore | 2 | 41.2 MiB | **busybox** runtime (was full ubuntu-noble) |
| builder | 2 | 19.2 MiB | **busybox** runtime (was full go image) |
| dockerbuilder-24 | 3 | 288.5 MiB | **ubuntu-noble** (was heroku-24-build ~405 MiB stack) |
| controller | 2 | 35.6 MiB | proto+go merged into one layer |
| postgres | 3 | 537.3 MiB | GIS/Timescale still dominate; `timescaledb-tools` kept |
| mongodb | 3 | 412.5 MiB | server + tools + mongosh (no mongos metapackage) |
| mariadb | 3 | 263.4 MiB | `--no-install-recommends` |
| redis | 3 | 221.2 MiB | `--no-install-recommends`; curl kept |
| kafka | 3 | 390.9 MiB | site-docs / `*.bat` stripped |
| clickhouse | 3 | 398.7 MiB | server payload unchanged |
| host | 3 | 514.7 MiB | `libseccomp2`; nested KVM kept |
| gitreceive | 3 | 231.4 MiB | git without Recommends |
| taffy | 3 | 228.1 MiB | git without Recommends |
| tarreceive | 2 | 224.7 MiB | still needs ubuntu-noble + mksquashfs |
| go | 2 | 341.6 MiB | dropped GOROOT test/api/doc/blog |
| heroku-24 | 2 | 271.8 MiB | ABI unchanged |
| heroku-24-build | 3 | 405.4 MiB | ABI unchanged |
| slugbuilder-24 | 5 | 459.1 MiB | ABI unchanged |
| slugrunner-24 | 3 | 271.8 MiB | ABI unchanged |
| protoc | 3 | 369.9 MiB | unzip purged after extract |

## What changed

### Cross-cutting
- Squashfs layers use **zstd level 15** (`pkg/squashfs`, builder run, ubuntu-noble, busybox, debootstrap base, slugbuilder, tarreceive).
- Layer diffs exclude docs/man/apt caches (`squashfs.DefaultExcludes`). The **ubuntu-noble rootfs** itself does **not** exclude `/usr/share/{man,doc,info}` — dpkg/`update-alternatives`/`install-info` need those directories.
- Go binaries strip DWARF (`-s -w`) and still embed `pkg/version.version`.
- Shared `builder/img/apt-slim-finish.sh`: autoremove; delete doc/man/info **files** but `mkdir -p` the directory skeleton; do **not** wipe bind-mounted apt caches or `/tmp`.
- Appliance/host/git `apt-get install` uses `--no-install-recommends`.
- `GOFLAGS=-buildvcs=false` on builder host `go build` (vboxsf git status 128).

### Bases
| Image | Before | After |
|---|---|---|
| blobstore | ubuntu-noble | **busybox** |
| builder | go (full toolchain) | **busybox** (still *built with* go) |
| dockerbuilder-24 | heroku-24-build | **ubuntu-noble** |
| controller | busybox + empty proto layer | busybox, proto+go in **one** layer |

### ubuntu-noble
- `--no-install-recommends` for squashfs-tools/curl/gnupg/coreutils/iproute2/ca-certificates.
- Dropped `net-tools`.
- Purge cloud-image packages: snapd, cloud-init, landscape, pollinate, ubuntu-pro, command-not-found, fwupd, unattended-upgrades, open-vm-tools, plymouth, lxd-installer.

### Appliances
- **postgres:** dropped `software-properties-common` and `lsb-release`; purge curl/gnupg after keys. **Kept** PostGIS, pgRouting, Timescale, **timescaledb-tools** (`timescaledb-tune` is not pulled as a Recommends), sudo, less.
- **mariadb:** `--no-install-recommends`; purge curl/gnupg. Kept sudo + mariabackup.
- **mongodb:** `mongodb-org-server` + `mongodb-database-tools` + `mongodb-mongosh` (no metapackage/mongos). Added sudo for `start.sh`.
- **redis:** `--no-install-recommends`; curl kept for restore.
- **kafka:** strip `site-docs` and Windows `*.bat`; purge curl after download.
- **clickhouse:** drop dummy `apt-transport-https`; purge `libcap2-bin` after `setcap`.
- **host:** `libseccomp2` instead of `libseccomp-dev`; dropped net-tools. Nested KVM kernel/qemu **kept**.
- **gitreceive/taffy:** git with `--no-install-recommends`.

### Toolchain
- **go:** drop `$GOROOT/{test,api,doc,blog}`. `go.sh` sources apt-slim-finish only when `/mnt/out` exists (image job, not host bootstrap).
- **protoc:** purge `unzip` after extract.

## Intentionally unchanged
- heroku-24 / heroku-24-build package lists (slug ABI)
- Postgres GIS/Timescale in the default image
- Host nested-KVM stack
- Kafka JRE / ClickHouse server payload

## Tests added so this cannot regress silently
- `script/test-vagrant-smoke-image-slim.sh` (host unit gate) locks bases, zstd, strip, apt flags, mongodb packages, postgres extensions including `timescaledb-tools`, host libseccomp2.
- Live smoke: `pg_available_extensions`, `flynn mongodb dump`, `curl blobstore.discoverd/.well-known/status` (retried after upgrade), `flynn-host version`.
- `go test ./pkg/squashfs/` on the Darwin host gate; `go test ./builder/` on the Linux builder gate.

## Smoke

`script/vagrant-upgrade-smoke.sh` (topologies `1,3`, two `--force` tarball updates each): **PASS**
(OVERALL 1861s on the reuse-tarball run; 196 app/CLI/DB checks; host + builder unit gates).
Unique squashfs layers after the cluster build: **2.8 GiB**.
