---
title: ClickHouse
layout: docs
---

# ClickHouse

The Flynn ClickHouse appliance provisions a [ClickHouse](https://clickhouse.com)
cluster with [ClickHouse Keeper](https://clickhouse.com/docs/en/guides/sre/keeper/clickhouse-keeper)
for replication coordination. A cluster is spread across the nodes of your Flynn
install with three replicas on multi-node installs (or a single replica on
single-node/`SINGLETON` installs).

User databases must be created with the `flynn clickhouse` CLI so they are
provisioned with `ON CLUSTER` DDL and replicated to every replica.

## Usage

### Adding a cluster to an app

ClickHouse comes ready to go as soon as you've installed Flynn. After you create
an app, provision a cluster for it by running:

```text
flynn resource add clickhouse
```

This provisions a ClickHouse cluster as a Flynn app and configures your
application to connect to it.

### Connecting to the cluster

Provisioning adds several environment variables to your app release:

* `CLICKHOUSE_URL` — native protocol connection URL for ClickHouse clients.
* `CLICKHOUSE_HTTP_URL` — HTTP interface URL.
* `CLICKHOUSE_HOST`, `CLICKHOUSE_PORT` — the discoverd host and native port.
* `CLICKHOUSE_HTTP_PORT` — the HTTP port (8123).
* `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD` — credentials for the `default` user.
* `CLICKHOUSE_DATABASE` — the built-in `default` database.
* `CLICKHOUSE_CLUSTER` — the cluster name (`flynn`) used for `ON CLUSTER` DDL.
* `CLICKHOUSE_REPLICA_COUNT` — the number of replicas in the cluster.

### Connecting to a console

To connect to a console for the cluster, run `flynn clickhouse client`. This
does not require the ClickHouse client to be installed locally or firewall or
security changes, as it runs in a container on the Flynn cluster.

## Managing databases

Databases must be created before they can be used on a replicated cluster.

```text
# List databases
flynn clickhouse databases

# Create a replicated database on every replica
flynn clickhouse databases create analytics

# Show tables in a database
flynn clickhouse databases info analytics

# Delete a database from every replica
flynn clickhouse databases destroy analytics
```

Database DDL is executed with `ON CLUSTER flynn` so schema changes are applied
consistently across replicas. When creating tables inside a replicated database,
use a `ReplicatedMergeTree` (or other replicated) table engine so data is
replicated through Keeper.

All `flynn clickhouse` commands run inside a container on the Flynn cluster.

### External access

An external route can be created to allow access from services not running on
Flynn:

```text
flynn -a $(flynn env get FLYNN_CLICKHOUSE) route add tcp --service $(flynn env get FLYNN_CLICKHOUSE) --leader
```

For security reasons this port should be firewalled and only accessed over the
local network, VPN, or SSH tunnel.

## Safety

The ClickHouse appliance stores data on a persistent volume attached to each
replica. Replication through Keeper provides durability across replicas on
multi-node installs. On single-node/`SINGLETON` installs the cluster runs a
single replica with no availability guarantees; treat that configuration as
suitable for development and testing only.
