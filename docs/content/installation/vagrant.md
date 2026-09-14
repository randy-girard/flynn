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

Set `FLYNN_MAX_NODES` if you need a larger (or singleton) topology. HTTP/HTTPS on each node is forwarded to the host (`9079+i` / `9442+i`).

Provisioning a cluster from these VMs is the same as [manual installation](manual.md): install `flynn-host` on each node, `flynn-host init` with `--peer-ips` or a discovery token, then `flynn-host bootstrap`.

## Demo directory

`demo/Vagrantfile` still references the historical `flynn-base` box from `dl.flynn.io`. Do not use it. Use the Vagrantfile at the repository root.
