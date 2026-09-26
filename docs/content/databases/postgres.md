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

Roles the platform appliance creates for system apps get a `CONNECTION LIMIT`
and cannot `CREATE EXTENSION` except as the appliance superuser. `CONNECT`
stays revoked from `PUBLIC`. Those roles are not tenant databases.

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
read the other instance. Sirenia is not started.

`--as ANALYTICS` sets only `ANALYTICS_URL`. The default name `DATABASE` sets
only `DATABASE_URL`. `flynn resource:attach` / `flynn resource:detach` add and
remove that variable. The same resource can attach to several apps under
different names. `flynn env:set` of an attached `*_URL` is rejected while it
is attached.

Inside one instance you can add databases and users. Those users exist only
in that instance.

### Follow, wait, promote

There is no in-place resize or upgrade. Create a follower, wait until it is
caught up, then promote:

```text
flynn resource:add postgres --follow <resource> --replication streaming
flynn pg:wait <follower>
flynn pg:promote <follower>
```

A follower is a separate resource, not an extra node. It is read-only. It
cannot follow another follower. Its own `--as` does not replace the leader
URL until promote. Promote makes the follower writable, ends the follow, and
rewrites the primary attachment `*_URL`. The old leader remains its own
resource. `flynn pg:unfollow <follower>` stops replication and leaves a
standalone writable copy.

`--replication streaming` is same-major. `--replication logical` is the
major-upgrade path. A follower may use a different `--runtime` name. Runtime
sizing itself lands in a later ticket.

`flynn pg:info` shows the leader, followers, and lag. `flynn pg:psql` opens a
console for this instance's URL only. Those commands come from the postgres
plugin. They are not built into the `flynn` CLI.

### Connecting to the database

A provisioned plugin database adds one environment variable to the app
release: `DATABASE_URL`, or `<NAME>_URL` when you pass `--as <NAME>`. That
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

Export the platform appliance on a TCP route with a stable hostname, then open
the host port:

```text
flynn resource:expose postgres
# default hostname postgres.<cluster-domain>, tls_mode=passthrough
sudo flynn-host firewall:expose PORT   # on every host
```

Passthrough is required for Postgres: clients send an SSLRequest in plaintext
before TLS, so router TLS terminate breaks `libpq`. Point DNS (or the cluster
wildcard) at the hosts and connect with `sslmode=require` to
`postgres.<cluster-domain>:PORT`.

You can still create the route yourself:

```text
flynn route:add tcp --service postgres --leader --domain postgres.example.com --tls-mode passthrough
sudo flynn-host firewall:expose PORT
```

Remove with `flynn resource:unexpose postgres` then
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

The postgres plugin provides commands for exporting and restoring an app database.

`flynn pg:dump` saves a complete copy of that instance's schema and data to a local file.

```text
$ flynn pg:dump -f latest.dump
60.34 MB 8.77 MB/s
```

The file can be used to restore the database with `flynn pg:restore`. It
may also be imported into a local Postgres database that is not managed by Flynn
with `pg_restore`:

```text
$ pg_restore --clean --no-acl --no-owner -d mydb latest.dump
```

`flynn pg:restore` loads a database dump from a local file into a Flynn Postgres
database. Any existing tables and database objects will be dropped before they
are recreated.

```text
$ flynn pg:restore -f latest.dump
62.29 MB / 62.29 MB [===================] 100.00 % 4.96 MB/s
WARNING: errors ignored on restore: 4
```

This will generate some warnings, but they are generally safe to ignore.

The restore command may also be used to restore a database dump from another non-Flynn
Postgres database, use `pg_dump` to create a dump file:

```text
$ pg_dump --format=custom --no-acl --no-owner mydb > mydb.dump
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
