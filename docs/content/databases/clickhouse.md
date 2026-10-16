---
title: ClickHouse
layout: docs
---

# ClickHouse

ClickHouse is a Flynn **plugin** (not part of the bootstrap tarball). Install it
on a cluster host, then provision from an app. See [Plugins](../plugins.md).

```text
sudo flynn-host plugin:install clickhouse --ref vX
sudo flynn-host plugin:install https://github.com/randy-girard/flynn-plugin-clickhouse.git --ref vX
sudo flynn-host plugin:install ../flynn-plugin-clickhouse
flynn resource:add clickhouse
```

The controller app is `clickhouse-plugin` (`flynn-host ps`, `flynn -a clickhouse-plugin`).
Provider name stays `clickhouse`.

The plugin provisions a [ClickHouse](https://clickhouse.com) cluster running
ClickHouse 24.8.14.39 LTS (amd64 and arm64), installed from official tarballs
vendored in the plugin repo. Every node runs
[ClickHouse Keeper](https://clickhouse.com/docs/en/guides/sre/keeper/clickhouse-keeper)
inside the server for replication coordination and is a Keeper voter with its
own volume. Change the node count with `flynn clickhouse:scale` (see
[Scaling the cluster](#scaling-the-cluster)).

User databases must be created with the `flynn clickhouse` CLI so they are
provisioned with `ON CLUSTER` DDL and the `Replicated` database engine.

`flynn resource:add clickhouse` creates a database and a user granted only that
database, with a memory and query quota. The app receives that user, not the
cluster `default` user. The provider sets `tenant_safe`. See [Plugins](../plugins.md).

## Usage

### Adding a cluster to an app

ClickHouse is available after the operator installs the plugin. After you create
an app, provision a cluster with:

```text
flynn resource:add clickhouse
```

This provisions a ClickHouse cluster as a Flynn app and configures your
application to connect to it.

### Connecting to the cluster

Provisioning adds the attachment URL to your app release. Every provision and
attach sets `FLYNN_CLICKHOUSE_<COLOR>_URL` unless `--as NAME` names the
attachment (`NAME_URL`, or `FLYNN_CLICKHOUSE_<COLOR>_URL` when `--as` is a color
short name such as `AMBER`). A new provision also sets `CLICKHOUSE_URL` when
that key is free; attaching an existing resource does not. Colors are assigned
per app and instance and are not reused while the instance exists, and
`flynn env set` / `env unset` of an attached key is rejected while attached.

The names below live on the ClickHouse resource record — the dashboard and the
appliance's own jobs read them there. `CLICKHOUSE_URL` and
`CLICKHOUSE_TRUSTED_CERT` are also copied onto the app release so a client can
open a verified TLS connection. The other names stay on the resource:

* `FLYNN_CLICKHOUSE` — the name of the ClickHouse app.
* `CLICKHOUSE_URL` — native protocol connection URL for ClickHouse clients.
  With TLS (the default) this is a clickhouse-go DSN:
  `clickhouse://<user>:<pass>@leader.<app>.discoverd:9440/<database>?secure=true`.
  The `clickhouses` scheme is not treated as TLS by clickhouse-go (the client
  then fails the handshake with `unexpected packet [21]`). Verify the server
  with `CLICKHOUSE_TRUSTED_CERT`. `leader.<app>.discoverd` is on the certificate.
* `CLICKHOUSE_HOST`, `CLICKHOUSE_PORT` — the discoverd host and native port
  (9440 with TLS; 9000 when `CLICKHOUSE_TLS_ENABLED=false`).
* `CLICKHOUSE_HTTP_PORT` — the HTTP port (8443 with TLS; 8123 without). The HTTP
  interface URL is derived from `CLICKHOUSE_HOST` and `CLICKHOUSE_HTTP_PORT`;
  there is no `CLICKHOUSE_HTTP_URL` variable, because the controller keeps only
  the attachment `*_URL` keys on a resource record.
* `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD` — credentials for the `default` user.
* `CLICKHOUSE_DATABASE` — the built-in `default` database.
* `CLICKHOUSE_CLUSTER` — the cluster name (`flynn`) used for `ON CLUSTER` DDL.
* `CLICKHOUSE_REPLICA_COUNT` — the number of replicas in the cluster.
* `CLICKHOUSE_TLS_ENABLED`, `CLICKHOUSE_TRUSTED_CERT` — TLS flag and the
  appliance CA (PEM). Replication between replicas uses the plaintext
  `interserver_http_port` 9009 on the overlay network; the plaintext client
  ports 9000/8123 remain open next to the TLS ports.

### Scaling the cluster

ClickHouse has no followers. Each resource is one cluster, and
`flynn resource:add clickhouse --follow` is rejected. The follow, wait,
promote, and unfollow commands and the dashboard Follow page are gone. Change
the number of nodes with:

```text
flynn clickhouse:scale <nodes> [<resource>]
```

`<nodes>` is 1 to 9; an odd count is recommended because every node is a
Keeper voter. Scaling down drains the highest node ids and drops their
replicas with `SYSTEM DROP REPLICA` before they stop. A node whose job is lost
comes back with the same id and volume. Resources created before embedded
Keeper cannot scale; `clickhouse:scale` returns an error for them.

The plugin reconciles every 30 seconds and removes Keeper voters that stay
missing. The dashboard records topology changes as events `P19` (node added),
`P20` (node removed), and `P21` (node rejoined).

### Connecting to a console

To connect to a console for the cluster, run `flynn clickhouse:cli` (alias
`flynn clickhouse client`). This
does not require the ClickHouse client to be installed locally or firewall or
security changes, as it runs in a container on the Flynn cluster. The CLI uses
`--secure` against **9440** and accepts the Flynn-minted appliance certificate
(the private CA is not in the image trust store). External clients should
still pass `CLICKHOUSE_TRUSTED_CERT`.

## Managing databases

Databases must be created before they can be used on a replicated cluster.

```text
# List databases
flynn clickhouse:databases

# Create a replicated database on every replica
flynn clickhouse:databases:create analytics

# Show tables in a database
flynn clickhouse:databases:info analytics

# Delete a database from every replica
flynn clickhouse:databases:destroy analytics
```

Database DDL is executed with `ON CLUSTER flynn` and the `Replicated` database
engine so schema changes are applied consistently across replicas. Create
tables with `ReplicatedMergeTree` so data is replicated through Keeper; plain
`MergeTree` tables do not replicate.

All `flynn clickhouse` commands run inside a container on the Flynn cluster.

### External access

Export ClickHouse on a TCP route with a stable hostname, then open the host
port:

```text
flynn resource:expose clickhouse
sudo flynn-host firewall:expose PORT   # on every host
```

Default TLS mode is passthrough: the appliance serves TLS on the native port
9440, so external clients use `clickhouse-client --secure --port PORT` and trust
`CLICKHOUSE_TRUSTED_CERT` (skip hostname verification if the route hostname is
not on the certificate).

You can still create the route yourself:

```text
flynn -a $(flynn env:get FLYNN_CLICKHOUSE) route:add tcp --service $(flynn env:get FLYNN_CLICKHOUSE) --leader --domain clickhouse.example.com --tls-mode passthrough
sudo flynn-host firewall:expose PORT
```

Remove with `flynn resource:unexpose clickhouse` then
`sudo flynn-host firewall:unexpose PORT`. See
[Production — Firewalling](../production.html.md#firewalling).

## Metrics

Each node serves `GET /metrics/log` on `FLYNN_METRICS_PORT=9180`. The dashboard
samples once per minute. See
[Plugins — Resource metrics line](../plugins.md#resource-metrics-line).

## Upgrades

A plugin update rolls the cluster one node at a time and refuses to start while
any node is unhealthy.

## Dashboard

The ClickHouse data source page has the tabs Overview (with the Nodes
control), Metrics, Databases, and Backup.

## Safety

The ClickHouse appliance stores data on a persistent volume attached to each
node. Replication through Keeper provides durability across nodes in a
multi-node cluster. A one-node cluster has no availability guarantees; treat
that configuration as suitable for development and testing only.
