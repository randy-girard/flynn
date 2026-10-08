---
title: Vagrant
layout: docs
toc_min_level: 2
---

# Vagrant

The repository `Vagrantfile` boots Ubuntu 24.04 (`bento/ubuntu-24.04`) VMs for building Flynn and for multi-node clusters. It replaces the old `flynn-base` demo box (that image is no longer published).

Install [Vagrant](https://www.vagrantup.com/) (≥ 1.9) and [VirtualBox](https://www.virtualbox.org/). The `vagrant-disksize` plugin is used to grow disks.

Clone this repository, then from the repo root:

## Builder VM

The **builder** VM runs `setup.sh` (Go 1.24, Docker, ZFS, PostgreSQL/MariaDB/MongoDB/Redis test packages) and mounts the source tree at `/root/go/src/github.com/flynn/flynn`. It starts Flynn only so `flynn-builder` can compile cluster images. It is not the live development cluster.

```text
vagrant up builder
vagrant ssh builder
```

Inside the VM:

```text
cd /root/go/src/github.com/flynn/flynn
make
```

See [Development](../development.html.md) for build, test, and release details.

The builder is sized for compiling cluster images (default 30 GB RAM / 8 CPUs, overridable with `VAGRANT_MEMORY` and `VAGRANT_CPUS`). Shrink those if you are only iterating on a single component.

## Cluster nodes

`node1` … `nodeN` are cluster members on a host-only network (`192.168.56.20+`). Flannel VXLAN needs promiscuous mode on that NIC (the Vagrantfile sets it).

```text
# default: three nodes
vagrant up node1 node2 node3
```

Set `FLYNN_MAX_NODES` if you need a larger (or singleton) topology.

Reach cluster services on the host-only addresses, not Vagrant NAT port forwards: `http://192.168.56.20/` is node1 HTTP, `.21` is node2, and so on. SSH is still `vagrant ssh` (Vagrant's default NAT forward for guest 22). The private network does not filter ports; Flynn's host firewall does. `22`/`80`/`443` are installer-owned; any other TCP port needs `sudo flynn-host firewall:expose PORT` before a client on the Vagrant host can connect to `nodeIP:PORT`.

Provisioning a cluster from these VMs is the same as [manual installation](manual.md): install `flynn-host` on each node, `flynn-host init` with `--peer-ips` or a discovery token, then `flynn-host bootstrap`.

`script/vagrant-smoke.sh` (or `make vagrant-smoke`) is the acceptance suite (`--item quick`, `--item datastores`, and so on). The same entrypoint also has VM lifecycle commands for the smoke env (`.vagrant`, **builder** + **nodeN**). They do not touch the laptop loop:

```text
make vagrant-smoke-status
make vagrant-smoke-ssh               # builder; or: make vagrant-smoke-ssh VM=node1
make vagrant-smoke-up                # boot VMs already in .vagrant
make vagrant-smoke-reload            # alias: restart
make vagrant-smoke-stop              # alias: halt
make vagrant-smoke-destroy           # alias: teardown; ./build stays
# same commands: script/vagrant-smoke.sh status|ssh|up|reload|stop|destroy
```

Scripts live under `script/vagrant/` (shared `lib/`, smoke lifecycle in `smoke-env.sh`). `script/vagrant/suite.sh` is still the suite.

## Laptop development cluster

`script/vagrant.sh` is the laptop loop (the default Vagrant script). Smoke keeps the `-smoke` suffix so both can be up without sharing running VMs. The laptop env uses `.vagrant-dev`, **dev-builder** at `192.168.57.10` (compile only), and **dev-node1** at `192.168.57.20` as the live cluster by default (`FLYNN_DEV_NODES=1`). Set `FLYNN_DEV_NODES=0` for builder-only, or `N` for `dev-node1` … `dev-nodeN` on `192.168.57.(19+N)`. Do not run a bare `vagrant up` for this loop either; that still boots smoke.

On **aarch64** VMs (Apple Silicon), cluster nodes install `qemu-user-static` **9+** during install, reload, and update so x86_64 Heroku buildpack binaries (including Node 24) can run inside slugbuilder. Ubuntu 24.04's qemu 8.2 crashes Node (`QEMU internal SIGSEGV {code=MAPERR, addr=0x20}`). The heroku-24 image on that architecture also installs amd64 glibc. Rebuild images after pulling the glibc change, then `script/vagrant.sh update` (or `bootstrap` on a new cluster). After a qemu-user upgrade, re-register binfmt on every node (`ensure-qemu-binfmt.sh`); `git push` prints a qemu-mode notice on arm64 hosts.

Laptop `git push` authenticates with `flynn git-credentials`. `cluster:add` records a **native** Flynn CLI (`/usr/local/bin/flynn` after `make vagrant-cli`). Do not point git at `build-dev/bin/flynn`: Vagrant image builds replace that path with a Linux `flynn-linux-*` symlink, which macOS cannot execute.

```text
make vagrant-setup           # first time: boot, build images if needed, bootstrap cluster nodes, connect
make vagrant-up              # boot dev-builder and dev-node1
make vagrant-reload          # rolling node-failure reboot; remaining hosts recover, node rejoins
make vagrant-stop            # halt VMs (disks stay)
make vagrant-destroy         # delete VMs; ./build-dev stays
make vagrant-status
make vagrant-ssh             # builder; or: make vagrant-ssh VM=dev-node1
make vagrant-build           # boot builder if needed and build images (works before setup)
make vagrant-cli             # build the laptop flynn CLI and install it to /usr/local/bin
make vagrant-bootstrap       # install the tarball and flynn-host bootstrap on cluster nodes
make vagrant-update          # build on the builder if Flynn source changed, then flynn-host update --all-nodes on running cluster nodes
make vagrant                 # help
# same commands: script/vagrant.sh setup|up|reload|stop|destroy|status|ssh|build|cli|bootstrap|update
```

`reload` / `restart` reboot one VM at a time (`vagrant reload --no-provision`) for every machine already in `.vagrant-dev` (or the names you pass, e.g. `make vagrant-reload VM=dev-node1`). They do not create missing VMs. A reboot is treated as a node failure: jobs are not drained first. Remaining hosts (and cluster-monitor) recover scheduler/controller/tenant HTTP; when the VM is back, `flynn-host.service` starts and the node rejoins via `--peer-ips`. Tenant HTTP is waited for after Flynn is stable. A single-node cluster is down until that VM is back. Cluster nodes use `flynn-host.service` (from `install-flynn`); after a reboot the script starts that unit if the cluster was already bootstrapped. The builder is not started as an operator cluster.

`setup` bootstraps from the layer cache. If there is no tarball and the cache is empty, it builds images on **dev-builder** instead of failing with “run build then setup again.” `build` can run first on a fresh builder (it boots `dev-builder` if needed). Builder Flynn is only the compile toolchain (`build.sh` start-all / stop-all around flynn-builder). The live cluster is `install-flynn` + `flynn-host init --peer-ips` + `flynn-host bootstrap` on **dev-nodeN**, the same path smoke tests. `update` rebuilds cluster images on the builder when Flynn source (not docs) is newer than the last tarball, then runs `flynn-host update --all-nodes --tarball --force` on the running cluster nodes so you exercise a real rolling update. Laptop `cluster:add` talks to **dev-node1** (`192.168.57.20`); it reads the controller job `AUTH_KEY` (`flynn-host cli-add-command`), not a stale `host.json` secret.

`stop` / `halt` run `vagrant halt`. `destroy` / `teardown` run `vagrant destroy -f`. Both stay in `.vagrant-dev` and do not touch smoke. Destroy does not delete `./build-dev` or `./flynn-logs`.

The source tree and `ubuntu_ports_cache` are shared, but Flynn **build outputs** and running VMs are not. Inside the VM they still look like `build/`; on the laptop they land in `./build-dev` (binaries, `images.json`, release tarballs). Smoke keeps `./build`. After this mount is added, run `make vagrant-reload` so the overlay attaches. If you already built on the shared `./build`, copy what you need (`cp -a build/. build-dev/`) or run `make vagrant-build` again.

If an older laptop loop already bootstrapped Flynn on **dev-builder**, run `make vagrant-bootstrap YES=1` (or `setup`) so the operator cluster moves onto **dev-node1**. Do not keep using the builder as `flynn -c local`.

Non-interactive (no sudo password prompt): `make vagrant-setup YES=1`, `make vagrant-update YES=1 FORCE_BUILD=1`, `make vagrant-destroy YES=1`. That passes `--yes` so `sudo -n` is used; if this laptop cannot write `/etc/hosts` without a password, setup still bootstraps the VMs and `make vagrant-probe` checks the cluster from **dev-node1**. Three-node cluster: `make vagrant-setup YES=1 NODES=3`.

`flynn-host plugin:install` / `plugin:update --rebuild` for a sibling checkout compiles on the cluster node (not the builder). `setup` installs the same pinned Go as the builder at `/usr/local/go` and sets `FLYNN_ROOT=/root/go/src/github.com/flynn/flynn` in `/etc/profile.d/flynn-root.sh`, `/etc/environment`, and `/etc/flynn/source-root` so plugin-build finds this Flynn even when `flynn-host` is `/usr/bin`. On an already-running node: `sudo bash script/vagrant/guest/ensure-go.sh` (or `sudo bash script/vagrant/guest/ensure-flynn-root.sh` if Go is already installed).

See [Development](../development.html.md).

## Demo directory

`demo/Vagrantfile` still references the historical `flynn-base` box from `dl.flynn.io`. Do not use it. Use the Vagrantfile at the repository root.
