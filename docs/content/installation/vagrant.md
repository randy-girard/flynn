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

`script/vagrant-smoke.sh` is the acceptance suite (`--item quick`, and so on). The same entrypoint also has VM lifecycle commands for the smoke env (`.vagrant`, **builder** + **nodeN**). They do not touch the laptop loop:

```text
script/vagrant-smoke.sh status
script/vagrant-smoke.sh ssh              # builder; or: ssh node1
script/vagrant-smoke.sh up               # boot VMs already in .vagrant
script/vagrant-smoke.sh reload           # alias: restart
script/vagrant-smoke.sh stop             # alias: halt
script/vagrant-smoke.sh destroy          # alias: teardown; ./build stays
```

## Laptop development cluster

`script/vagrant-dev.sh` is a second Vagrant environment, kept apart from smoke so both can be up. It uses `.vagrant-dev`, a VM named **dev-builder** at `192.168.57.10`, and optional extra hosts `dev-node1` … `dev-nodeN` on `192.168.57.(19+N)` (`FLYNN_DEV_NODES=N`). Do not run a bare `vagrant up` for this loop either; that still boots smoke.

```text
script/vagrant-dev.sh setup      # first time: boot, bootstrap, connect this laptop
script/vagrant-dev.sh up         # boot only dev-builder
script/vagrant-dev.sh reload     # reboot VMs and start flynn-host again
script/vagrant-dev.sh restart    # same as reload
script/vagrant-dev.sh stop       # halt VMs (disks stay)
script/vagrant-dev.sh destroy    # delete VMs (alias: teardown); ./build-dev stays
script/vagrant-dev.sh status
script/vagrant-dev.sh ssh
```

`reload` / `restart` run `vagrant reload --no-provision` on every machine already in `.vagrant-dev` (or the names you pass, e.g. `script/vagrant-dev.sh reload dev-node1`). They do not create missing VMs. Nested `script/bootstrap-flynn` on **dev-builder** does not install a boot-time systemd unit, so after a reboot the script starts `flynn-host` again if the cluster was already bootstrapped.

`stop` / `halt` run `vagrant halt`. `destroy` / `teardown` run `vagrant destroy -f`. Both stay in `.vagrant-dev` and do not touch smoke. Destroy does not delete `./build-dev` or `./flynn-logs`.

The source tree is shared, but Flynn **build outputs** are not. Inside the VM they still look like `build/`; on the laptop they land in `./build-dev` (binaries, `images.json`, release tarballs). Smoke keeps `./build`. After this mount is added, run `script/vagrant-dev.sh reload` so the overlay attaches. If you already built on the shared `./build`, copy what you need (`cp -a build/. build-dev/`) or run `script/vagrant-dev.sh build` again.

See [Development](../development.html.md).

## Demo directory

`demo/Vagrantfile` still references the historical `flynn-base` box from `dl.flynn.io`. Do not use it. Use the Vagrantfile at the repository root.
