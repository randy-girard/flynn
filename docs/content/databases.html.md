---
title: Databases
layout: docs
---

# Databases

Postgres is included in Flynn. Other engines (Redis, MariaDB, MongoDB, Kafka,
ClickHouse) are **plugins**: the operator installs them with
[`flynn-host plugin:install`](plugins.md) from a sibling repo or git URL. The
user `flynn` CLI only shows those commands after the plugin is installed on the
cluster. Command syntax lives on the plugin (`flynn-plugin.json` `cli`); the
CLI fetches it from the cluster and runs matching actions as controller jobs.

In some cases it is not possible to meet the strict guarantees of a 'CP' system
under [CAP theorem](https://en.wikipedia.org/wiki/CAP_theorem) due to
limitations in the database software we are wrapping. This is noted specifically
in the Safety section of the documentation for the database in question.

Flynn's databases are currently designed with staging, testing, development, and
small-scale production workloads in mind. They are not currently suitable for
storing large amounts of data. We are in the process of making them usable for
all use cases, including high volume, large dataset workloads.

## Appliances

Provision from an app with `flynn resource add <provider>`. Connection details
are injected as environment variables. User jobs may resolve the **leader**
hostname Flynn puts in those URLs; other internal `*.discoverd` names do not
resolve from user jobs.

| Provider | Engine | Topology |
| --- | --- | --- |
| [`postgres`](databases/postgres.md) | PostgreSQL 16 (PostGIS, pgRouting, TimescaleDB) | HA: primary + synchronous replica + async chain |
| [`mysql`](databases/mysql.md) | MariaDB 10.11 | Same HA state machine; scaled up on first provision |
| [`mongodb`](databases/mongodb.md) | MongoDB 7.0 | Replica set; scaled up on first provision |
| [`redis`](databases/redis.md) | Redis (Ubuntu 24.04 package) | Single process, ephemeral |
| [`kafka`](databases/kafka.md) | Apache Kafka 3.9 (KRaft, no ZooKeeper) | Three brokers (one on singleton); TLS to clients by default |
| [`clickhouse`](databases/clickhouse.md) | ClickHouse with ClickHouse Keeper | Three replicas (one on singleton) |

Redis, Kafka, and ClickHouse do not use the sirenia state machine described
below. See each page for safety notes.

On a single-host cluster, postgres/MariaDB/MongoDB run one peer
(`SINGLETON=true`). When a third host joins, the scheduler promotes them to a
three-peer replica set automatically.

## External TLS access

HTTP apps get a hostname on the cluster domain. Datastores use the same idea on
a TCP port in 3000–3500. From the app that owns the resource:

```text
flynn resource:expose postgres
sudo flynn-host firewall:expose PORT   # on every host
```

Default hostname is `<service>.<cluster-domain>` (for example
`postgres.example.com`). Default TLS mode is **passthrough**: the router
forwards bytes and the appliance speaks TLS (required for Postgres/MySQL
SSLRequest). **terminate** wraps TLS at the router (`--auto-tls` or a manual
cert) and is for TLS-first protocols when the backend is still plaintext.

In-cluster jobs keep using the `*.discoverd` names in `DATABASE_URL` /
`REDIS_URL` / similar. Those names do not need a TCP route. New provisioned
Postgres URLs use `sslmode=require`; plaintext `sslmode=disable` still works
inside the overlay.

Remove the route with `flynn resource:unexpose <provider>`, then
`sudo flynn-host firewall:unexpose PORT`. Details are on each engine page and
in [Production — Firewalling](production.html.md#firewalling).

## State Machine Design

The Flynn database appliances are designed with a few goals in mind:

1. Acknowledged writes must not be lost and must be consistent.
1. Network partitions must be tolerated without corrupting data. There should be
   no potential for split-brain or other data-mangling failures.
1. When a failure occurs, the appliance should transition into an available
   configuration without operator intervention if it can do so safely.

 The appliance is a cluster of three or more database instances where:

- One member of the cluster, the _primary_, serves consistent reads and writes.
- The primary has synchronous replication to a single member called the _sync_.
  Write transactions are not acknowledged to client until they have been added
  to the sync's transaction log.
- Replicating from the sync is a daisy chain of one or more _async_ instances,
  which replicate changes asynchronously from their upstream link in the chain.
- If possible, the system automatically reconfigures itself after failures to
  maximize uptime and never lose data.

In the face of an arbitrary failure or maintenance action, the cluster can
temporarily lose the ability to handle writes and consistent reads. Eventually
consistent reads are always available from the sync and async instances.

If the primary fails, the sync sees this and promotes itself to primary,
converting the async replicating from it to the new sync. Writes are not
accepted until the new sync has caught up. A variety of safety conditions are in
place so that a promotion will never cause writes to be lost or split brain to
occur.

The cluster state is maintained by the primary and stored in discoverd. The
discoverd DNS and HTTP APIs expose the current primary instance.

This design is heavily based on the prior work done by Joyent on the [Manatee
state machine](https://github.com/joyent/manatee-state-machine).

Flynn comes with a cluster configured with three instances by default. If an
instance fails, the scheduler will create a new instance and the cluster will be
reconfigured by the primary without operator intervention.
