---
title: Databases
layout: docs
---

# Databases

Postgres is the platform appliance in Flynn (controller and other system apps).
It is not a tenant database. Tenant Postgres is the upcoming
`flynn-plugin-postgres`. Other engines (Redis, MariaDB, MongoDB, Kafka,
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

Provision from an app with `flynn resource:add <provider>`. Connection details
are injected as environment variables. User jobs may resolve the **leader**
hostname Flynn puts in those URLs; other internal `*.discoverd` names do not
resolve from user jobs.

| Provider | Engine | Topology |
| --- | --- | --- |
| [`postgres`](databases/postgres.md) | PostgreSQL 16 (PostGIS, pgRouting, TimescaleDB) | HA: primary + synchronous replica + async chain |
| [`mysql`](databases/mysql.md) | MariaDB 10.11 | Same HA state machine; scaled up on first provision |
| [`mongodb`](databases/mongodb.md) | MongoDB 7.0 | Replica set; scaled up on first provision |
| [`redis`](databases/redis.md) | Redis (Ubuntu 24.04 package) | Single process; AOF on a volume, no replicas, not in cluster backup |
| [`kafka`](databases/kafka.md) | Apache Kafka 3.9 (KRaft, no ZooKeeper) | Three brokers (one on singleton); TLS to clients by default |
| [`clickhouse`](databases/clickhouse.md) | ClickHouse with ClickHouse Keeper | Three replicas (one on singleton) |

Redis, Kafka, and ClickHouse do not use the sirenia state machine described
below. See each page for safety notes.

## Database runtimes

A database runtime is a name plus CPU, memory, and disk for one engine
(`postgres`, `redis`, `mariadb`, `mongodb`, `kafka`, `clickhouse`). The
MariaDB provider on `resource:add` is `mysql`. These are **not** app process
runtimes. `flynn-host runtime` and `flynn limit:runtime` size app processes
only. Database runtimes are listed and edited with `flynn-host db-runtime`.

Each engine ships `small`, `medium`, and `large`. The numbers are per engine.
Redis `small` disk is 1GB; Postgres `small` disk is 10GB.

| Engine | small | medium | large |
| --- | --- | --- | --- |
| postgres | 500 milliCPU, 512MB, 10GB | 1000 milliCPU, 1GB, 50GB | 2000 milliCPU, 2GB, 100GB |
| redis | 250 milliCPU, 256MB, 1GB | 500 milliCPU, 512MB, 5GB | 1000 milliCPU, 1GB, 10GB |
| mariadb | 500 milliCPU, 512MB, 8GB | 1000 milliCPU, 1GB, 32GB | 2000 milliCPU, 2GB, 80GB |
| mongodb | 500 milliCPU, 1GB, 16GB | 1000 milliCPU, 2GB, 64GB | 2000 milliCPU, 4GB, 200GB |
| kafka | 1000 milliCPU, 1GB, 20GB | 2000 milliCPU, 2GB, 100GB | 4000 milliCPU, 4GB, 500GB |
| clickhouse | 1000 milliCPU, 2GB, 32GB | 2000 milliCPU, 4GB, 128GB | 4000 milliCPU, 8GB, 500GB |

```text
flynn resource:add redis
flynn resource:add redis --runtime medium
sudo flynn-host db-runtime
sudo flynn-host db-runtime:create --memory 1GB --cpu 500 --disk 20GB redis cache
sudo flynn-host db-runtime:update --disk 2GB redis small
sudo flynn-host db-runtime:remove redis cache
```

Omitting `--runtime` uses `small`. There is no controller table for these
definitions. Builtins are in memory. Admin creates, updates, removals, and
the custom-size switch are stored in `/etc/flynn/db-runtimes.json`
(`FLYNN_DB_RUNTIMES` overrides the path). `flynn resource:add` reads that
same file. On a machine without it, only the builtins above are published
and custom sizes stay off.

Changing a definition does not resize instances already created from it.
There is no in-place resize; provision a new resource to get a new size.

Tenants cannot set raw `--cpu`, `--memory`, or `--disk`, and they cannot
request a runtime name that is not published. A cluster admin allows raw
sizes with `sudo flynn-host db-runtime:allow-custom`.

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

## Per-instance ports

`flynn resource:add` for the tenant Postgres plugin, Redis, MariaDB (`mysql`),
MongoDB, Kafka, and ClickHouse stores a TCP port from 3000–3500 on that
resource. Allocation refuses a port another resource already owns. The
platform Postgres appliance does not get one. The port is
`FLYNN_INSTANCE_PORT`. `FLYNN_INSTANCE_ID` is the resource's external id.
Both are returned in the app environment with the connection URL.

In-cluster clients use the discoverd hostname in that URL. The discoverd URL
keeps the service port (`5432`, `6379`, and so on). The host port is the
external path: clients outside the cluster connect to a host that is running
this instance, on `FLYNN_INSTANCE_PORT`.

`flynn-host` opens that port only on hosts where this instance's processes
are running. The instance job carries `flynn-instance.id` and
`flynn-instance.port`. A datastore job may instead set `flynn-datastore=true`
and carry `FLYNN_INSTANCE_ID` / `FLYNN_INSTANCE_PORT` in its own environment.
An app that only received those variables from the resource does not open the
port. Every 15 seconds the host rebuilds the expose set from the jobs it is
running. After a scale, failover, or reschedule, that sync opens the port on
the new host and closes it on the host that no longer runs the job.

The plan for an instance lists that instance's port and the hosts running it.
It does not list another instance's port. The allow is for this process on
this host. Two instances on one host are two ports. A shared listener does
not demultiplex tenants.

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

Rolling deploys of an HA appliance start each replacement peer and wait for it
to join the async chain and catch up **before** stopping the peer it replaces.
That keeps three live replicas during the new job's base backup; writes still
pause for a few seconds when the old sync is stopped and sirenia promotes the
async, which is the same window a sync failure already causes.

The cluster state is maintained by the primary and stored in discoverd. The
discoverd DNS and HTTP APIs expose the current primary instance.

This design is heavily based on the prior work done by Joyent on the [Manatee
state machine](https://github.com/joyent/manatee-state-machine).

Flynn comes with a cluster configured with three instances by default. If an
instance fails, the scheduler will create a new instance and the cluster will be
reconfigured by the primary without operator intervention.
