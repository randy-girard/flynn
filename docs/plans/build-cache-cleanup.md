# Build/Image Cache Cleanup Plan

Scope: tighten the existing apt + curl caching strategy in `build.sh`,
`builder/build.go`, `builder/ubuntu-setup.sh`, `builder/img/*.sh`, and the
per-service `*/img/packages.sh` files. The build pipeline is Noble-only — all
references to bionic/trusty/xenial/cedar-14/heroku-18 have been removed,
including the bootstrap migration paths, dead tests, and host/dev tooling.
The architecture is sound; this plan fixes inconsistencies and one real
cache-correctness risk.

## Group 0 — Remove legacy stack support (done)

`builder/manifest.json` only references `ubuntu-noble`, `heroku-24`,
`heroku-24-build`, `slugbuilder-24`, and `slugrunner-24`.

Pre-Noble image scripts removed:

- [x] `builder/img/ubuntu-bionic.sh`
- [x] `builder/img/ubuntu-trusty.sh`
- [x] `builder/img/ubuntu-xenial.sh`
- [x] `builder/img/cedar-14.sh`
- [x] `builder/img/heroku-18.sh`
- [x] `builder/img/heroku-18-build.sh`
- [x] `builder/build.go` — `Builder.baseLayer` comment now references
  the ubuntu-noble image.

Bootstrap / migration code removed:

- [x] `host/cli/bootstrap.go` — slugbuilder/slugrunner -14/-18 artifact
  creation + update SQL blocks dropped.
- [x] `host/cli/bootstrap.go` — `cedar-14` default stack tag migration
  dropped.
- [x] `host/cli/bootstrap.go` — `migrateSlugs`/`migrateDocker` artifact
  scan + `slug-migrator` invocation dropped (along with the unused
  `bufio` import).
- [x] `slugbuilder/migrator/` — deleted (only invoked from the removed
  bootstrap branch).
- [x] `builder/manifest.json` — `slugbuilder/migrator` gobuild entry
  removed.
- [x] `cli/export.go` — env-var lookups now require `SLUGBUILDER_24_IMAGE_ID`
  / `SLUGRUNNER_24_IMAGE_ID`; pre-Heroku-stack fallbacks dropped.
- [x] `controller/examples/examples.go` — `SLUGRUNNER_18_IMAGE_ID/URI`
  references updated to the `_24_` variants.

Tests:

- [x] `test/test_backup.go` — deleted (only exercised legacy backup
  tarballs and asserted `cedar-14` / `heroku-18` stack tags).
- [x] `test/test_gitreceive.go` — `slugrunner.stack` assertion is now
  `heroku-24`.
- [x] `test/test_release.go` — `build/image/slugbuilder-18.json` →
  `build/image/slugbuilder-24.json`.

Host / dev tooling:

- [x] `test/rootfs/build.sh` — base image switched to `ubuntu-base-24.04`.
- [x] `test/rootfs/setup.sh` — xenial repo URLs replaced with noble
  equivalents; `apt-key` calls replaced with `gpg --dearmor` into
  `/etc/apt/keyrings`; pinned `linux-image-4.13` replaced with
  `linux-image-generic`; `btrfs-tools` → `btrfs-progs`;
  postgres/mariadb/redis pulled from the noble archive; mongodb served
  from the official `mongodb-org/8.0` noble repo.
- [x] `host/img/packages.sh` — dead commented-out xenial kernel 4.13
  install block removed.
- [x] `builder/ubuntu-setup.sh` — trusty Dockerfile URL replaced with a
  generic upstream reference.
- [x] `Vagrantfile` — commented `ubuntu/xenial64` line removed.
- [x] `prereq.sh` — deleted (no callers; trusty/xenial-era vagrant
  prereq).
- [x] `util/packer/` — deleted (xenial Packer config superseded by the
  Vagrantfile pointing at `bento/ubuntu-24.04` directly).

## Group 1 — Apt-list cleanup parity (layer size; cache unaffected)

Append the standard cleanup tail to each script below, matching the form
already used in `builder/img/heroku-24.sh` (lines 167-171):

```
if ! mountpoint -q /var/cache/apt/archives 2>/dev/null; then
  rm -rf /var/cache/apt/archives/* "/var/cache/apt/archives/partial"/*
fi
rm -rf /var/lib/apt/lists/*
```

Files (each currently does a conditional `apt-get clean` but leaves
`/var/lib/apt/lists/*` in place):

- [ ] `builder/img/go.sh` — after `gobin-noenv` build
- [ ] `builder/img/protoc.sh` — after the two `go install` lines
- [ ] `host/img/packages.sh` — after `systemctl enable systemd-networkd.service`
- [ ] `test/img/packages.sh` — after `git config` block
- [ ] `taffy/img/packages.sh` — after the conditional `apt-get clean`
- [ ] `gitreceive/img/packages.sh` — after the conditional `apt-get clean`

Verification: rebuild each affected image, confirm
`/var/lib/flynn/layer-cache/<id>.squashfs` shrinks (apt lists are typically
20-40 MB per Ubuntu series) and `unsquashfs -l` shows no `/var/lib/apt/lists`
entries.

## Group 2 — Fix `builder/img/busybox.sh` apt invocation

`builder/img/busybox.sh:5` currently runs `apt install busybox-static`:

- [ ] Replace with: `apt-get update && apt-get install -y --no-install-recommends busybox-static`
- [ ] Add the standard cleanup tail (Group 1 form) before the `mksquashfs`
  call so re-runs without the host cache mount do not leave stray .debs in the
  build env.

No behaviour change in the produced busybox layer (the squashfs is assembled
from `${TMP}/root`, not `/`), but the script becomes consistent and survives
stricter apt versions.

## Group 3 — flynn-curl shim cache-correctness for rolling URLs (real risk)

`builder/img/flynn-curl.sh` writes `${ROOT}/<sha256(url)>.blob` and treats it
as immortal. URLs whose body rotates under a stable path silently return
stale content. Affected callers (Noble only, since the pre-Noble scripts are
gone):

- `builder/img/ubuntu-noble.sh:23-29` — `${BASE_URL}/SHA256SUMS`
- `builder/img/ubuntu-noble.sh:39-45` — `ubuntu-24.04-server-cloudimg-${arch}-root.tar.xz`

Changes:

- [ ] `builder/img/flynn-curl.sh`: before the cache-hit branch (around line
  160), bypass the cache when either of the following is true:
  - the URL path contains `/current/` or ends with `SHA256SUMS`/`SHA256SUMS.gpg`;
  - the env var `FLYNN_HTTP_CACHE_SKIP_PATTERNS` is set to a `:`-separated
    list of substrings, and the URL contains any of them.
- [ ] `builder/build.go`: extend the `flynn-builder build` usage block (around
  the existing APT cache documentation, ~line 105) to document
  `FLYNN_HTTP_CACHE_SKIP_PATTERNS` and `FLYNN_NO_HTTP_CACHE=1` (the latter
  being a one-line guard at the top of the shim that `exec_real`s immediately).
- [ ] `builder/img/flynn-curl.sh`: honour `FLYNN_NO_HTTP_CACHE=1` as the first
  check after `ROOT` validation.

Verification: unit-test the shim by setting `FLYNN_HTTP_CACHE_ROOT` to a temp
dir and calling `bin/curl https://example.com/foo/current/x.tar.xz -o /tmp/x`
twice — second invocation should hit the network (not the cache).

## Out of scope (intentionally not changed)

- `--no-install-recommends` rollout to non-Heroku scripts: layer-size only,
  unrelated to caching, and may pull in packages those services depend on
  implicitly. Track separately.
- Flock granularity (per-apt-call instead of per-layer): would require
  restructuring layer scripts; current coarse lock is correct.
- `apt-get upgrade` in `heroku-24.sh`: layer ID hashing intentionally ignores
  upstream package versions; the APT cache absorbs the cost in steady state.

## Suggested ordering

1. Group 1 (mechanical, low-risk).
2. Group 2 (single file, low-risk).
3. Group 3 (real cache-correctness fix; ship before next base-image bump).
