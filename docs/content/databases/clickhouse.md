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

The plugin provisions a [ClickHouse](https://clickhouse.com) cluster with
[ClickHouse Keeper](https://clickhouse.com/docs/en/guides/sre/keeper/clickhouse-keeper)
for replication coordination. A cluster is spread across the nodes of your Flynn
install with three replicas on multi-node installs (or a single replica on
single-node/`SINGLETON` installs).

User databases must be created with the `flynn clickhouse` CLI so they are
provisioned with `ON CLUSTER` DDL and replicated to every replica.

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

Provisioning adds several environment variables to your app release:

* `FLYNN_CLICKHOUSE` — the name of the ClickHouse app.
* `CLICKHOUSE_URL` — native protocol connection URL for ClickHouse clients
  (`clickhouses://` when TLS is on, the default).
* `CLICKHOUSE_HTTP_URL` — HTTP interface URL (`https://` when TLS is on).
* `CLICKHOUSE_HOST`, `CLICKHOUSE_PORT` — the discoverd host and native port
  (9440 with TLS; 9000 when `CLICKHOUSE_TLS_ENABLED=false`).
* `CLICKHOUSE_HTTP_PORT` — the HTTP port (8443 with TLS; 8123 without).
* `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD` — credentials for the `default` user.
* `CLICKHOUSE_DATABASE` — the built-in `default` database.
* `CLICKHOUSE_CLUSTER` — the cluster name (`flynn`) used for `ON CLUSTER` DDL.
* `CLICKHOUSE_REPLICA_COUNT` — the number of replicas in the cluster.
* `CLICKHOUSE_TLS_ENABLED`, `CLICKHOUSE_TRUSTED_CERT` — TLS flag and the
  appliance CA (PEM). Replication between replicas uses the plaintext
  `interserver_http_port` 9009 on the overlay network; the plaintext client
  ports 9000/8123 remain open next to the TLS ports.

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

Database DDL is executed with `ON CLUSTER flynn` so schema changes are applied
consistently across replicas. When creating tables inside a replicated database,
use a `ReplicatedMergeTree` (or other replicated) table engine so data is
replicated through Keeper.

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

## Safety

The ClickHouse appliance stores data on a persistent volume attached to each
replica. Replication through Keeper provides durability across replicas on
multi-node installs. On single-node/`SINGLETON` installs the cluster runs a
single replica with no availability guarantees; treat that configuration as
suitable for development and testing only.
