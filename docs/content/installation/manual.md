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

A trial install can omit a disk. The script enables `flynn-host` and does not
start it. The first `systemctl start flynn-host`, or a reboot, then creates a
sparse file and uses that file as the ZFS pool.

For production, pass a dedicated disk on this install command, before
`systemctl start flynn-host` or a reboot. The disk must not be the OS disk.
`-f` overwrites an existing label on that device. The flag does not migrate an
existing pool. If `flynn-default` already exists, `zpool create` fails and the
install stops.

```
$ sudo bash /tmp/install-flynn --zpool-create-device /dev/sdb --zpool-create-options "-f"
```

`flynn-host production:check` fails while any leaf of this host's pool is a
regular file, in either tenancy mode. Run it on every host. See
[Production](../production.html.md#storage) for moving an existing file pool
onto a disk.

## Repeat on every host

Install Flynn as above on every host that should join the cluster.

## Set up nodes

Allow all network traffic between cluster members (UDP and TCP). Open these
ports externally on every node:

* 80 (HTTP)
* 443 (HTTPS)
* 3000 to 3500 (user-defined TCP services, optional — datastore exports)

**A firewall with this configuration is required.** Internal Flynn APIs must not
be reachable from the internet; access to them is equivalent to root on the
cluster. After install, `flynn-host firewall` manages extra peer IPs and TCP
ports on the host UFW rules (`firewall:peer:add`, `firewall:expose`,
`firewall:unexpose`, `firewall:sync`). Public 22/80/443 and private cluster
CIDRs stay installer-owned. `flynn addons:expose` creates a datastore TCP route
and the cluster firewall opens that port on every host. It does not print
`flynn-host firewall:expose`.

Next, start a Layer 0 cluster: run `flynn-host` on every node. The daemon uses
Raft for leader election and must know its peers.

### Discovery token

For more than one node, create a discovery token with `flynn-host init`. Skip
this on a single-node install.

On the first node, point `--init-discovery` at a discovery API you run
(`DISCOVERY_SERVER` is required; there is no public hosted service):

```
$ sudo DISCOVERY_SERVER=https://discovery.example.com CONTROLLER_KEY=<controller key> flynn-host init --init-discovery
https://discovery.example.com/clusters/<id>
```

`POST /clusters` requires `CONTROLLER_KEY` and returns that token URL only to an authorized caller. The token is the join capability: `flynn-host` presents the cluster id as `Authorization: Bearer` when it registers and lists instances. `GET /.well-known/cluster` on the discovery plugin requires the same key.

On the other nodes:

```
$ sudo flynn-host init --discovery https://discovery.example.com/clusters/<id>
```

To run discovery **on the cluster**, bootstrap a single node with `--peer-ips`,
then install the discovery plugin (`sudo flynn-host plugin:install discovery`).
The installer writes the join token to `/etc/flynn/discovery-token` (mode 0600)
and does not print it. Additional nodes use that token:

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

To pin a dedicated database node at install time:

```
$ sudo flynn-host init --peer-ips 192.168.56.20,192.168.56.21,192.168.56.22 \
    --external-ip 192.168.56.50 --tag role=database --max-processes 1
```

That writes `--tags flynn-max-processes=1,flynn-max-processes-source=operator,role=database` into `/etc/flynn/host.json`.
Change tags later with `flynn-host tags:set` / `tags:max-processes`.

Omitting `--max-processes` on a new `host.json` writes `flynn-max-processes=auto`. After the daemon starts, the cap is how many jobs of the smallest published runtime fit in `MemTotal`, and not more than the overlay addresses on the host. Hosted and self-hosted clusters use the same rule. An existing file keeps whatever cap it already has, including no cap. `tags:max-processes <host> --auto` opts a running host in. An explicit number stays put.

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

Pass `--peer-ips` as well when you already know the node addresses. Bootstrap then only contacts those hosts and never sends `FLYNN_HOST_AUTH_KEY` to a discovery-registered URL that is not on that list.

With peer IPs instead of discovery:

```
$ sudo \
    CLUSTER_DOMAIN=demo.example.com \
    flynn-host bootstrap \
    --min-hosts 3 \
    --peer-ips 192.168.56.20,192.168.56.21,192.168.56.22 \
    --admin-email admin@demo.example.com \
    --admin-password 'choose-a-password'
```

Non-interactive bootstrap requires `--admin-email` and `--admin-password`. On a TTY, bootstrap prompts for them. `--from-backup` does not create a new admin.

Then run `sudo flynn-host cli-add-command` on a host and `flynn login` on your laptop.

If bootstrap fails, confirm traffic can flow on `flannel.1`, `flynnbr0`, and
`veth*` interfaces, then open a GitHub issue.

## HTTPS / Let's Encrypt

Bootstrap issues a self-signed certificate. For trusted TLS, install the Let's
Encrypt plugin, register ACME, and enable it on system routes (controller,
dashboard, …):

```
$ sudo flynn-host plugin:install letsencrypt
$ sudo flynn-host letsencrypt:configure --email=admin@example.com --agree-tos
$ sudo flynn-host letsencrypt:enable-system-routes
```

`configure` also enables ACME for the cluster. Use `--staging` while testing
(Let's Encrypt issues untrusted certs) or `--directory-url` for another ACME
CA. Check status with `sudo flynn-host letsencrypt:status`.

App routes opt in with `flynn letsencrypt:enable <domain>` after
`flynn route:add http <domain>`. The name
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
