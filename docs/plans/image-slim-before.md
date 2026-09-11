# Image size before report

Snapshot of Flynn cluster/toolchain images **before** the slim-cluster-images
changes, from `develop` at `ece2f0fd`.

Measured 2026-09-11 by checking out `develop` and running
`./build.sh --version v20260911.0-develop all` on the builder VM:

```text
script/report-image-sizes.sh build/images.json
```

## Measured squashfs sizes (`develop`)

Unique layers (shared bases counted once): **54 layers, 3 953 811 456 bytes (3.7 GiB)**.

`layer_sum` is the sum of that image's layers (shared bases appear in many rows).

| image | layers | layer_sum |
|---|---:|---:|
| ubuntu-noble | 1 | 298.2 MiB |
| busybox | 1 | 1.9 MiB |
| blobstore | 2 | 349.9 MiB |
| builder | 3 | 497.3 MiB |
| dockerbuilder-24 | 5 | 641.5 MiB |
| controller | 3 | 52.0 MiB |
| postgres | 3 | 709.0 MiB |
| mongodb | 3 | 576.3 MiB |
| mariadb | 3 | 381.5 MiB |
| redis | 3 | 330.0 MiB |
| kafka | 3 | 513.7 MiB |
| clickhouse | 3 | 539.3 MiB |
| host | 3 | 911.0 MiB |
| gitreceive | 3 | 345.1 MiB |
| taffy | 3 | 337.7 MiB |
| tarreceive | 2 | 331.5 MiB |
| go | 2 | 473.7 MiB |
| heroku-24 | 2 | 379.3 MiB |
| heroku-24-build | 3 | 541.3 MiB |
| slugbuilder-24 | 5 | 601.3 MiB |
| slugrunner-24 | 3 | 379.3 MiB |
| protoc | 3 | 507.2 MiB |
| discoverd | 2 | 26.6 MiB |
| flannel | 2 | 26.8 MiB |
| router | 2 | 23.4 MiB |
| acme | 2 | 20.8 MiB |
| logaggregator | 2 | 19.2 MiB |

## Shared bases

| Image | Base | What is in it today |
|---|---|---|
| `ubuntu-noble` | Ubuntu 24.04 **server cloud image** | Full cloudimg + `squashfs-tools curl gnupg coreutils net-tools iproute2`. No cloud-init/snapd purge. `apt-get install` without `--no-install-recommends`. |
| `busybox` | scratch-like busybox root | Already small. |
| `go` | ubuntu-noble | Go 1.24.12 + `git build-essential pkg-config libseccomp-dev`. GOROOT `test/`, `api/`, `doc/` kept. |
| `heroku-24` | ubuntu-noble | Heroku-24 runtime package list (`--no-install-recommends`). Compatibility ABI. |
| `heroku-24-build` | heroku-24 | Compiler + `-dev` headers (`--no-install-recommends`). |

Squashfs: `mksquashfs … -noappend` only (default **gzip**). Layer diffs do not
exclude docs/man/apt caches. Go binaries: version `-X` only, **no `-s -w`**.

## Cluster apps (`util/release/images_template.json`)

| Image | Runtime base | Package / content issues |
|---|---|---|
| discoverd, flannel, controller, router, acme, logaggregator, status, updater | busybox | Unstripped Go binaries. Controller has a near-empty protobuild layer. |
| blobstore | **ubuntu-noble** | Pure Go + `ca-certs.pem`. Full Ubuntu rootfs for no runtime apt need. |
| builder | **go** (full toolchain) | Ships GOROOT + build-essential. Build jobs already `BuildWith: go`. |
| tarreceive | ubuntu-noble | Needs `mksquashfs` (keep Ubuntu). |
| gitreceive, taffy | ubuntu-noble | `apt-get install git` with Recommends. `taffy/img/packages.sh` unused (manifest uses gitreceive's script). |
| postgres | ubuntu-noble | No `--no-install-recommends`. Unused `software-properties-common`, `lsb-release`. Keeps PostGIS, pgRouting, Timescale (product features). Always `rm -rf /var/lib/apt/lists/*` (wipes bind-mounted cache). |
| mariadb | ubuntu-noble | Recommends on; leftover `curl gnupg lsb-release`. `sudo` required. |
| mongodb | ubuntu-noble | Metapackage `mongodb-org` (mongos + legacy shell). |
| redis | ubuntu-noble | Recommends on. `curl` required at runtime (`restore.sh`). |
| kafka | ubuntu-noble | Full tarball including `site-docs` / Windows scripts. JRE with Recommends. |
| clickhouse | ubuntu-noble | `apt-transport-https` (noop on Noble). `libcap2-bin` left after `setcap`. |
| host | ubuntu-noble | `libseccomp-dev` (headers) instead of `libseccomp2`. Kernel + qemu for nested KVM (kept). |
| dockerbuilder-24 | **heroku-24-build** | Only needs tar/curl/BuildKit; inherits the entire compiler image. |
| slugbuilder-24 / slugrunner-24 | heroku-24-build / heroku-24 | Heroku ABI; not in scope to shrink package lists. |

## Cross-cutting waste

1. Cloud image bloat on every Ubuntu-based appliance (snapd, cloud-init, landscape, ubuntu-pro, fwupd, …).
2. APT Recommends (docs, man, extra tools) on almost every appliance `packages.sh`.
3. Install-only tools (`curl`, `gnupg`, `lsb-release`, `software-properties-common`) left in runtime layers.
4. Gzip squashfs vs zstd.
5. Unstripped Go binaries (~25–40% extra).
6. Wrong bases: blobstore, builder, dockerbuilder.

## Intentionally not changing (product / ABI)

- heroku-24 / heroku-24-build package lists
- Postgres PostGIS, pgRouting, Timescale in the default image
- Host nested-KVM kernel + qemu
- ClickHouse server payload (inherently large)
- Kafka JRE (no custom jlink)
