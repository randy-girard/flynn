---
title: Manual Installation
layout: docs
---

# Manual Installation

Flynn is installed with the install script on **Ubuntu 24.04 LTS** amd64.

Start from a clean Ubuntu install. Each host should have at least 2 GB of RAM,
40 GB of storage, and two CPU cores. Lower specs can work for experiments; they
are not recommended.

For a highly available cluster, use **at least three nodes**. A single node is
fine for trying Flynn.

*Note: If you are installing on a provider that uses a customized kernel by
default, you may need the Ubuntu distribution kernel for ZFS. On Linode, [use
this
guide](https://www.linode.com/docs/tools-reference/custom-kernels-distros/run-a-distribution-supplied-kernel-with-kvm)
to switch.*

## Installation

Download and run the Flynn install script from this fork's GitHub Releases:

```
$ sudo bash < <(curl -fsSL https://github.com/randy-girard/flynn/releases/latest/download/install-flynn)
```

To inspect the script before running it as root:

```
$ curl -fsSL -o /tmp/install-flynn https://github.com/randy-girard/flynn/releases/latest/download/install-flynn
... take a look at the contents of /tmp/install-flynn ...
$ sudo bash /tmp/install-flynn
```

Install a specific version with `--version`. The default GitHub repository is
`randy-girard/flynn`; override it with `--repo` or `FLYNN_GITHUB_REPO`.

The installer:

1. Installs Flynn's runtime dependencies (including ZFS)
2. Downloads, verifies, and installs the `flynn-host` binary
3. Downloads and verifies filesystem images for each Flynn component
4. Installs a systemd unit for `flynn-host`

Images are large (hundreds of megabytes), so step 3 can take a while.

For production, create the ZFS pool on a dedicated disk instead of the default
sparse file:

```
$ sudo bash /tmp/install-flynn --zpool-create-device /dev/sdb --zpool-create-options "-f"
```

See [Production](../production.html.md) for moving an existing pool off the sparse
file.

## Repeat on every host

Install Flynn as above on every host that should join the cluster.

## Set up nodes

Allow all network traffic between cluster members (UDP and TCP). Open these
ports externally on every node:

* 80 (HTTP)
* 443 (HTTPS)
* 3000 to 3500 (user-defined TCP services, optional)

**A firewall with this configuration is required.** Internal Flynn APIs must not
be reachable from the internet; access to them is equivalent to root on the
cluster.

Next, start a Layer 0 cluster: run `flynn-host` on every node. The daemon uses
Raft for leader election and must know its peers.

### Discovery token

For more than one node, create a discovery token with `flynn-host init`. Skip
this on a single-node install.

On the first node, point `--init-discovery` at a discovery API you run
(`DISCOVERY_SERVER` is required; there is no public hosted service):

```
$ sudo DISCOVERY_SERVER=https://discovery.example.com flynn-host init --init-discovery
https://discovery.example.com/clusters/<id>
```

On the other nodes:

```
$ sudo flynn-host init --discovery https://discovery.example.com/clusters/<id>
```

To run discovery **on the cluster**, bootstrap a single node with `--peer-ips`,
then install the discovery plugin (`sudo flynn-host plugin:install discovery`).
The installer prints a join token (also `/etc/flynn/discovery-token`). Additional
nodes use that token:

```
$ sudo flynn-host init --discovery https://discovery.example.com/clusters/<id>
```

Point `discovery.${CLUSTER_DOMAIN}` at the first node (wildcard DNS is enough)
before other hosts try to join.

### Peer IPs (no discovery service)

If you already know the node addresses, skip discovery:

```
$ sudo flynn-host init --peer-ips 192.168.56.20,192.168.56.21,192.168.56.22 --external-ip 192.168.56.20
```

Use each node's own address as `--external-ip`. After the cluster is running,
`--peer-ips` is also how you join an additional host.

## Start Flynn

```
$ sudo systemctl start flynn-host
$ sudo systemctl status flynn-host
```

If the unit is not running, check `/var/log/flynn/flynn-host.log` and try
again.

## Bootstrap Flynn

With `flynn-host` running, bootstrap Layer 1. You need a domain with DNS A
records for every node IP, plus a wildcard CNAME to that domain.

**Example**

```
demo.example.com.    A      192.168.56.20
demo.example.com.    A      192.168.56.21
demo.example.com.    A      192.168.56.22
*.demo.example.com.  CNAME  demo.example.com.
```

Set `CLUSTER_DOMAIN` and run bootstrap **once** (it schedules jobs across the
cluster):

```
$ sudo \
    CLUSTER_DOMAIN=demo.example.com \
    flynn-host bootstrap \
    --min-hosts 3 \
    --discovery https://discovery.example.com/clusters/<id>
```

With peer IPs instead of discovery:

```
$ sudo \
    CLUSTER_DOMAIN=demo.example.com \
    flynn-host bootstrap \
    --min-hosts 3 \
    --peer-ips 192.168.56.20,192.168.56.21,192.168.56.22
```

The last bootstrap log line is the `flynn cluster add` command for the [CLI](../cli.md). You can also run `sudo flynn-host cli-add-command` on a host.

If bootstrap fails, confirm traffic can flow on `flannel.1`, `flynnbr0`, and
`veth*` interfaces, then open a GitHub issue.

## HTTPS / Let's Encrypt

Bootstrap issues a self-signed certificate. For trusted TLS, on any cluster
host, register ACME and enable it on system routes (controller, dashboard, …):

```
$ sudo flynn-host acme:configure --email=admin@example.com --agree-tos
$ sudo flynn-host acme:enable-system-routes
```

`configure` also enables ACME for the cluster. Use `--staging` while testing
(Let's Encrypt issues untrusted certs) or `--directory-url` for another ACME
CA. Check status with `sudo flynn-host acme:status`.

App routes opt in with `flynn route add http --auto-tls <domain>`. The name
must resolve to the cluster so Let's Encrypt can complete HTTP-01 on ports 80
and 443. You can still attach your own cert with `--tls-cert` / `--tls-key`.
After system routes have a public certificate, run
`flynn cluster:refresh --clear` so the CLI uses normal TLS verification
instead of the bootstrap pin.

See [Apps — HTTPS](../apps.md#https).

Next: [Flynn Basics](../basics.md).

## CLI

On your laptop (Linux, macOS, or Windows):

```
$ curl -fsSL https://github.com/randy-girard/flynn/releases/latest/download/install-flynn-cli | sudo bash
```

See [CLI](../cli.md).
