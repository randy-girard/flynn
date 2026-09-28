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

The **builder** VM runs `setup.sh` (Go 1.24, Docker, ZFS, PostgreSQL/MariaDB/MongoDB/Redis test packages) and mounts the source tree at `/root/go/src/github.com/flynn/flynn`.

```text
vagrant up builder
vagrant ssh builder
```

Inside the VM:

```text
cd /root/go/src/github.com/flynn/flynn
make
script/bootstrap-flynn
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

`script/vagrant.sh` is the laptop loop (the default Vagrant script). Smoke keeps the `-smoke` suffix so both can be up without sharing running VMs. The laptop env uses `.vagrant-dev`, **dev-builder** at `192.168.57.10`, and **dev-node1** at `192.168.57.20` by default (`FLYNN_DEV_NODES=1`). Set `FLYNN_DEV_NODES=0` for builder-only, or `N` for `dev-node1` … `dev-nodeN` on `192.168.57.(19+N)`. Do not run a bare `vagrant up` for this loop either; that still boots smoke.

```text
make vagrant-setup           # first time: boot, build images if needed, bootstrap, connect
make vagrant-up              # boot dev-builder and dev-node1
make vagrant-reload          # reboot VMs and start flynn-host again
make vagrant-stop            # halt VMs (disks stay)
make vagrant-destroy         # delete VMs; ./build-dev stays
make vagrant-status
make vagrant-ssh
make vagrant-build           # boot builder if needed and build images (works before setup)
make vagrant-cli             # build the laptop flynn CLI and install it to /usr/local/bin
make vagrant-bootstrap       # first cluster on dev-builder (script/bootstrap-flynn)
make vagrant-update          # flynn-host update from the new tarball
make vagrant                 # help
# same commands: script/vagrant.sh setup|up|reload|stop|destroy|status|ssh|build|cli|bootstrap|update
```

`reload` / `restart` run `vagrant reload --no-provision` on every machine already in `.vagrant-dev` (or the names you pass, e.g. `make vagrant-reload VM=dev-node1`). They do not create missing VMs. Nested `script/bootstrap-flynn` on **dev-builder** does not install a boot-time systemd unit, so after a reboot the script starts `flynn-host` again if the cluster was already bootstrapped.

`setup` bootstraps from the layer cache. If there is no tarball and the cache is empty, it builds images instead of failing with “run build then setup again.” `build` can run first on a fresh builder (it boots `dev-builder` if needed). After a cluster exists, `build` sets `FLYNN_KEEP_CLUSTER=1` so `build.sh cluster` does not `stop-all` / `install-flynn --remove`. Without that, discoverd on `192.0.2.200:1111` is gone and `flynn-host ps` fails with connection refused. `update` restarts the start-stop-daemon flynn-host (this VM has no `flynn-host.service`). If a previous `build` already tore the cluster down, `setup` treats leftover `/etc/flynn/host.json` as not bootstrapped and runs `bootstrap-flynn` again. Laptop `cluster:add` reads the controller job `AUTH_KEY` (`flynn-host cli-add-command`), not a stale `host.json` secret; `GET /ca-cert` does not check the key, so a leftover key used to look like success and then `GET /apps` returned 401.

`stop` / `halt` run `vagrant halt`. `destroy` / `teardown` run `vagrant destroy -f`. Both stay in `.vagrant-dev` and do not touch smoke. Destroy does not delete `./build-dev` or `./flynn-logs`.

The source tree and `ubuntu_ports_cache` are shared, but Flynn **build outputs** and running VMs are not. Inside the VM they still look like `build/`; on the laptop they land in `./build-dev` (binaries, `images.json`, release tarballs). Smoke keeps `./build`. After this mount is added, run `make vagrant-reload` so the overlay attaches. If you already built on the shared `./build`, copy what you need (`cp -a build/. build-dev/`) or run `make vagrant-build` again.

See [Development](../development.html.md).

## Demo directory

`demo/Vagrantfile` still references the historical `flynn-base` box from `dl.flynn.io`. Do not use it. Use the Vagrantfile at the repository root.
