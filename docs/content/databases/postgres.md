---
title: PostgreSQL
layout: docs
---

# PostgreSQL

Flynn has two different Postgres roles. They are not interchangeable.

The **platform appliance** (`appliance/postgresql`, system app `postgres`) is
the database bootstrap starts for the controller and other system apps
(blobstore). It stays in Flynn, uses sirenia, and is not a catalog plugin.
`flynn resource:add postgres` does not create a role or database on it, and
tenant apps never receive its superuser password.

**Tenant Postgres** is the `flynn-plugin-postgres` plugin. Install it with
`flynn-host plugin:install postgres`. It registers provider `postgres` at
`postgres-plugin.discoverd`. `flynn resource:add postgres` provisions on that
plugin. It does not create a role on the platform appliance, and it does not
dial `postgres-api.discoverd`.

The platform image is PostgreSQL 16 in a highly-available configuration. It
automatically fails over to a synchronous replica with no loss of data if the
primary server goes down. A rolling update starts each replacement replica and
waits for it to catch up before stopping the peer it replaces, so the
three-peer set stays intact during the new job's base backup. Writes still
pause briefly when the old sync is promoted off, the same window a sync
failure already causes. A single-host (`SINGLETON`) cluster runs one peer;
when a third host joins, the scheduler promotes the appliance to a three-peer
replica set automatically.

The image includes **PostGIS 3**, **pgRouting**, and **TimescaleDB 2** in
addition to `postgresql-contrib`.

Roles the platform appliance creates for system apps cannot `CREATE EXTENSION`
except as the appliance superuser. `CONNECT` stays revoked from `PUBLIC`.
Those roles are not tenant databases and do not get a `CONNECTION LIMIT`:
each controller process already caps itself with a small pgx pool, and an
HA cluster runs several of those processes. Tenant Postgres still applies
`CONNECTION LIMIT` on the plugin appliance.

## Usage

### Adding a database to an app

The platform appliance is already running after install. It is only for
system apps. To give an app its own database, install the postgres plugin
and then run:

```text
flynn resource:add postgres
flynn resource:add postgres --as ANALYTICS
```

That command does not provision a database on the platform appliance. It
creates a new Flynn app with one volume and exactly one Postgres node. Two
resources do not share an app, volume, superuser, or any credential that can
read the other instance. Sirenia is not started. The command returns after the instance is scheduled.
The new volume still runs `initdb` and registers in discoverd in the
background (up to five minutes). Check later with `flynn pg:wait` or the
dashboard overview, which live-updates while it starts.

`--as ANALYTICS` sets `ANALYTICS_URL`. A new provision also sets
`DATABASE_URL` when the app does not already have it. Every provision and
attach sets `FLYNN_POSTGRESQL_<COLOR>_URL` unless `--as` names the attachment.
`--as AMBER` sets `FLYNN_POSTGRESQL_AMBER_URL`. Attaching an existing resource
does not set `DATABASE_URL`. The first logical database on
a new instance is a random alphanumeric name. `flynn resource:attach` /
`flynn resource:detach` add and
remove that variable. The same resource can attach to several apps under
different names. `flynn env:set` of an attached `*_URL` is rejected while it
is attached.

Inside one instance you can add databases and users. Those users exist only
in that instance.

### Follow, wait, promote

There is no in-place resize or upgrade. Create a follower, wait until it is
caught up, then promote:

```text
flynn pg:follow
flynn pg:wait <follower>
flynn pg:promote <follower>
```

`flynn pg:wait` prints live copy progress (basebackup percent, then WAL catch-up)
until lag is zero. The dashboard Follow and Upgrade pages poll the same progress.

A follower is a separate resource, not an extra node. It is read-only. It
cannot follow another follower. Its own `--as` does not replace the leader
URL until promote. Promote makes the follower writable, ends the follow, and
rewrites the primary attachment `*_URL`. The old leader remains its own
resource. `flynn pg:unfollow <follower>` stops replication and leaves a
standalone writable copy.

`flynn resource:add postgres --follow <primary> --auto-failover` (or
`flynn pg:follow --auto-failover`) places the replica on a different host.
If the primary job is gone for about 30 seconds, Flynn promotes the replica,
fences the old primary, and starts a new replica so the pair remains. Failover
is asynchronous, so any unreplicated writes are lost (RPO is replication lag).
A single-node cluster cannot use auto-failover. Default `--follow` stays
manual.

Followers always stream on the same engine version. `flynn pg:upgrade` uses
logical replication, promotes a new primary, then recreates followers. A
follower may use a different `--runtime` name.

`flynn pg:info` shows the role, who follows whom, auto-failover, replica
pending, host, and lag. `flynn pg:create`
creates a logical database on this instance. `flynn pg:psql` opens a console
for this instance's URL only. Those commands come from the postgres plugin.
They are not built into the `flynn` CLI.

### Connecting to the database

A provisioned plugin database adds environment variables to the app
release: `FLYNN_POSTGRESQL_<COLOR>_URL` unless `--as <NAME>` names the
attachment (`<NAME>_URL`, or `FLYNN_POSTGRESQL_<COLOR>_URL` when `--as` is a
color short name such as `AMBER`). A new provision also sets `DATABASE_URL`
when the app does not already have it. Attaching an existing resource does
not set `DATABASE_URL`. That
URL is a role on this instance, not the platform appliance superuser. New
URLs use `sslmode=require`. The platform appliance enables
`ssl=on` with a cluster-generated server certificate (SANs include
`postgres.discoverd`, `leader.postgres.discoverd`, and
`postgres.<cluster-domain>`). `pg_hba` still uses `host` (not `hostssl`), so
in-cluster clients that pass `sslmode=disable` keep working.

App `DATABASE_URL` connections use TCP 5432. The platform appliance admin HTTP
API on :5433 (`/status`, `/stop`) requires the cluster controller key; `GET
/.well-known/status` stays open for health checks. User jobs cannot open :5433.

### External access

`flynn resource:expose postgres` exports **this app's tenant instance** (the
plugin resource), not the platform appliance. It creates a leader TCP route
and prints the host command that opens the port:

```text
flynn resource:expose postgres
# default hostname <service>.<cluster-domain>, tls_mode=passthrough
sudo flynn-host firewall:expose PORT   # on every host
```

Passthrough is required for Postgres: clients send an SSLRequest in plaintext
before TLS, so router TLS terminate breaks `libpq`. Point DNS (or the cluster
wildcard) at the hosts and connect with `sslmode=require`.

The platform appliance is a different discoverd service (`postgres`). Export
that only from a host if you need operator access; tenant apps should not
receive its superuser URL.

```text
flynn route:add tcp --service postgres --leader --domain postgres.example.com --tls-mode passthrough
sudo flynn-host firewall:expose PORT
```

Remove a tenant export with `flynn resource:unexpose postgres` then
`sudo flynn-host firewall:unexpose PORT`. Treat the exported port as public;
prefer a VPN when you can. See
[Production — Firewalling](../production.html.md#firewalling).

### Connecting to a console

To connect to a `psql` console for **your app's** database, install the postgres
plugin and run `flynn pg:psql`. That command is the plugin, not a built-in
`flynn` command. It runs in a container on the cluster and uses the same
controller credential as other `flynn` commands.

The platform database (controller, blobstore, and the built-in appliance) is
not that console. On a cluster host, run `flynn-host pg:psql`, `flynn-host
pg:dump`, or `flynn-host pg:restore`. See
[Production — Internal Databases](../production.html.md#internal-databases).

### Dumping and restoring

`flynn pg:dump` / `flynn pg:restore` dump this app's tenant instance in
Postgres custom format. They are plugin commands (`flynn pg --help`). The
plugin dashboard Backup page is the same operation.

`flynn-host pg:dump` / `flynn-host pg:restore` dump the **platform** appliance
(controller, blobstore, plugin metadata on `platform-postgres`). They do not
dump a tenant instance. Cluster `flynn-host backup` uses `pg_dumpall` on that
same appliance; tenant instance volumes are not in the tarball.

To copy a tenant database with tools on your laptop, connect with the
injected `DATABASE_URL` (or `flynn pg:psql`) and run `pg_dump` /
`pg_restore` yourself:

```text
$ pg_dump --format=custom --no-acl --no-owner "$DATABASE_URL" > mydb.dump
$ pg_restore --clean --no-acl --no-owner -d mydb mydb.dump
```

### Extensions

The Flynn Postgres appliance ships `postgresql-contrib-16` plus PostGIS,
pgRouting, and TimescaleDB. Enable an extension with `CREATE EXTENSION`:

```text
$ flynn pg:psql
psql (16)
Type "help" for help.

bbabc090024fcdd118b04c50a0fb0d8c=> CREATE EXTENSION hstore;
CREATE EXTENSION
bbabc090024fcdd118b04c50a0fb0d8c=> CREATE EXTENSION timescaledb;
CREATE EXTENSION
```

Notable packaged extensions:

|        Name          |                             Description                             |
|----------------------|---------------------------------------------------------------------|
| btree\_gin           | support for indexing common datatypes in GIN                        |
| btree\_gist          | support for indexing common datatypes in GiST                       |
| citext               | data type for case-insensitive character strings                    |
| cube                 | data type for multidimensional cubes                                |
| dblink               | connect to other PostgreSQL databases from within a database        |
| dict\_int            | text search dictionary template for integers                        |
| earthdistance        | calculate great-circle distances on the surface of the Earth        |
| fuzzystrmatch        | determine similarities and distance between strings                 |
| hstore               | data type for storing sets of (key, value) pairs                    |
| intarray             | functions, operators, and index support for 1-D arrays of integers  |
| isn                  | data types for international product numbering standards            |
| ltree                | data type for hierarchical tree-like structures                     |
| pg\_prewarm          | prewarm relation data                                               |
| pg\_stat\_statements | track execution statistics of all SQL statements executed           |
| pg\_trgm             | text similarity measurement and index searching based on trigrams   |
| pgcrypto             | cryptographic functions                                             |
| pgrouting            | pgRouting Extension                                                 |
| pgrowlocks           | show row-level locking information                                  |
| pgstattuple          | show tuple-level statistics                                         |
| plpgsql              | PL/pgSQL procedural language                                        |
| postgis              | PostGIS geometry, geography, and raster spatial types and functions |
| postgis\_topology    | PostGIS topology spatial types and functions                        |
| postgres\_fdw        | foreign-data wrapper for remote PostgreSQL servers                  |
| tablefunc            | functions that manipulate whole tables, including crosstab          |
| timescaledb          | time-series hypertables and compression (TimescaleDB 2)             |
| unaccent             | text search dictionary that removes accents                         |
| uuid-ossp            | generate universally unique identifiers (UUIDs)                     |

`postgresql-contrib` also provides the usual additional contrib modules. List
them with `\dx` in `psql`. PLV8 is not installed.

Additionally, the following full text search dictionaries are installed:

|      Name        |                        Description                        |
|------------------|-----------------------------------------------------------|
| danish\_stem     | snowball stemmer for danish language                      |
| dutch\_stem      | snowball stemmer for dutch language                       |
| english\_stem    | snowball stemmer for english language                     |
| finnish\_stem    | snowball stemmer for finnish language                     |
| french\_stem     | snowball stemmer for french language                      |
| german\_stem     | snowball stemmer for german language                      |
| hungarian\_stem  | snowball stemmer for hungarian language                   |
| italian\_stem    | snowball stemmer for italian language                     |
| norwegian\_stem  | snowball stemmer for norwegian language                   |
| portuguese\_stem | snowball stemmer for portuguese language                  |
| romanian\_stem   | snowball stemmer for romanian language                    |
| russian\_stem    | snowball stemmer for russian language                     |
| simple           | simple dictionary: just lower case and check for stopword |
| spanish\_stem    | snowball stemmer for spanish language                     |
| swedish\_stem    | snowball stemmer for swedish language                     |
| turkish\_stem    | snowball stemmer for turkish language                     |

## Safety

This appliance is designed to provide full consistency and partition tolerance
for all operations that are committed to the write-ahead log (WAL). Note that
this guarantee does not apply to advisory locks, as they are specific to the
server they are acquired and are not persisted to the WAL.

There is currently no support for tuning, and data transfer during recovery is
not optimized, so we do not recommend using the appliance for applications that
have high throughput or many records.
