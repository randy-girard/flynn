---
title: PostgreSQL
layout: docs
---

# PostgreSQL

Flynn has two different Postgres roles. They are not interchangeable.

The **platform appliance** (`appliance/postgresql`, system app `postgres`) is
the database bootstrap starts for the controller and other system apps
(blobstore). It stays in Flynn, uses sirenia, and is not a catalog plugin.
`flynn addons:create postgres` does not create a role or database on it, and
tenant apps never receive its superuser password.

**Tenant Postgres** is the `flynn-plugin-postgres` plugin. Install it with
`flynn-host plugin:install postgres`. It registers provider `postgres` at
`postgres-plugin.discoverd`. `flynn addons:create postgres` provisions on that
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
flynn addons:create postgres
flynn addons:create postgres --as ANALYTICS
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
a new instance is a random alphanumeric name. `flynn addons:attach` /
`flynn addons:detach` add and
remove that variable. The same resource can attach to several apps under
different names. `flynn env:set` of an attached `*_URL` is rejected while it
is attached.

Inside one instance you can add databases and users. Those users exist only
in that instance.

### Follow, wait, promote

You can only follow a resource of the same app. Another tenant's resource is
refused as not found.

There is no in-place resize or upgrade. Create a follower, wait until it is
caught up, then promote:

```text
flynn pg:follow
flynn pg:wait <follower>
flynn pg:promote <follower>
```

`flynn pg:wait` prints live copy progress (basebackup percent, then WAL catch-up)
until lag is zero. The dashboard Follow and Upgrade pages poll the same progress.

A follower is a separate resource, not an extra node. It is read-only.
Each follower has its own read-only login, different from the leader's. Apps
attached to a follower get only that login; it can read but not write, on the
follower and on the leader (replication copies roles, so it is created on the
leader). The follower's resource env never contains the leader's credentials.
After an auto-failover promote the leader's apps keep their URL, which the
promoted host now answers, with the leader's login (replication copies it).
Apps attached to the follower keep the read-only login after a failover or
unfollow. A follower created before per-follower logins used the leader's
login, and the releases of its apps may still carry
`FLYNN_POSTGRES_PRIMARY_URL`; `flynn pg:maintenance` (or plugin start with
`MAINTENANCE_AUTO=true`) gives it its own read-only login, moves its apps
onto it, removes that key from their releases when it points at the leader,
and deploys them. Nothing in Flynn reads the key from an app; use the
leader's own attachment (`DATABASE_URL`) to write. It cannot follow another follower. Its own `--as` does not replace the leader
URL until promote.

`flynn pg:promote [--force] <follower>` (and the dashboard Promote button) is
a switchover: the follower becomes the primary, the leader's apps follow it,
and the old primary is retired. It restarts the follower's job as a primary;
rewriting its release alone would leave the running process a read-only
standby. It first reads the primary's
WAL position and waits up to 20 seconds for the follower to replay it. A
follower that is behind or not streaming is refused and nothing changes, so
run `flynn pg:wait <follower>` first. If the primary cannot be read, the
promote is refused unless you pass `--force`, which skips that proof: writes
the follower has not received are lost. The current primary is then stopped
and marked deposed before the restart (it never starts writable again), and
the follower applies everything it received, so no acknowledged write is
lost. An auto-failover follower keeps `leader.<original>.discoverd`. The restarted job must
leave recovery and accept a write (a temp table, never a tenant table) within
3 minutes; otherwise the follower goes back to its previous release and a
stopped primary is started again. Only a verified promote changes the roles
the plugin reports. For a plain follower the plugin then rewrites the old
primary's attachments (`DATABASE_URL` and the rest) to the promoted host with
the leader's login, moves the promoted resource's own attachments onto that
writable login, deploys each app and waits for it; the result's `Rewritten`
lists them and the CLI prints the apps and keys. Apps cannot write between
the stop and their deploy. If a deploy fails, run the promote again to
finish. The old primary stays stopped with its data; destroying its resource
deletes it. Running the promote again afterwards changes nothing.

`flynn pg:unfollow <follower>` restarts the follower as a standalone writable
copy, checks that it accepts a write, and only then reports it standalone; if
the check fails it stays a follower. The old leader is not touched. The
dashboard Followers tab can also remove a follower while it is still
replicating. Remove stops replication and deletes the replica.

`flynn-host postgres:availability` is `manual` or `system` for the whole
cluster. Hosted and self-hosted use the same values. `manual` is the
self-hosted default and creates no platform replica. `system` is the default
only for a cluster that was already hosted when the controller migrated, and
for a fresh `flynn-host plugin:install hosted` that has no Postgres resources
yet. A cluster admin sets it with that command or on Cluster → Settings.
Changing it does not deploy the plugin or restart user apps.

After a system failover, user followers stay read-only and are repointed at
`leader.<original>.discoverd`, which the new primary keeps claiming on later
failovers.

`system` keeps one platform replica per primary when another host can take
the volume's memory, disk, process cap, and persistent-singleton cap. That
replica is not an add-on and does not count against the resource quota. The
customer cannot delete, unfollow, or promote it. Deleting the database
deletes it. The page shows protected only while that replica is streaming
and at most 16 MiB behind. A one-host cluster stays unprotected. The watch
runs on one plugin process, fences the running primary before promotion, and
creates at most one new platform replica every five seconds.

`DATABASE_URL` keeps `leader.<original>.discoverd`. The promoted process
registers that name. Running apps are not restarted. Replication is
asynchronous, so a failover can drop commits the primary already
acknowledged. Scheduled backups remain the copy you restore. Synchronous
commit stays off.

`flynn addons:create postgres --follow <primary> --auto-failover` (or
`flynn pg:follow --auto-failover`) is the manual-mode failover follower. It
is a real add-on. If another host can take the volume, it is placed there.
If not, it starts on the primary's host and the page says so. When
availability is `manual` and the primary job has been gone for about 30
seconds, Flynn promotes that follower and leaves `DATABASE_URL` on the
original discoverd name. Both plugin API processes read the primary's WAL
position every 5 seconds while it runs. The follower is promoted only when it
has replayed the position the primary had when it was last seen running, no
more than 30 seconds before its job stopped. With no such sighting (both API
processes restarted during the outage) or a follower that is behind, nothing
changes and the plugin logs why once; `flynn pg:promote --force` is the way
out. The promote is the same verified sequence as `pg:promote`: the old
primary is stopped and stamped `deposed` (it refuses to start writable)
before the follower is restarted as the primary, which must leave recovery
and accept a write; any failure puts both back. A replacement replica is
then created for the tenant that owns the resource, on another host when one
fits. Destroying that replacement turns auto-failover off for the primary,
so no other one is created. When availability is `system`, add follower still
works and those followers stay read-only. The watch does not promote them.
The add-follower form does not offer automatic failover while `system` is on.

Followers always stream on the same engine version. `flynn pg:upgrade` uses
logical replication, promotes a new primary, then recreates followers. A
replacement follower takes the old follower's name and is scaled back up to its
original process count; a failed switch restores both apps. A
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

Each isolated instance has its own CA and a server certificate for
`leader.<instance>.discoverd`. The plugin's own connections (dashboard, dump
and restore, metrics, backups, failover checks) use `sslmode=verify-full`
against that CA. Instances created before the CA existed keep
`sslmode=require` until `pg:maintenance` replaces them.

App `DATABASE_URL` connections use TCP 5432. The platform appliance admin HTTP
API on :5433 (`/status`, `/stop`) requires the cluster controller key; `GET
/.well-known/status` stays open for health checks. User jobs cannot open :5433.

### External access

`flynn addons:expose postgres` exports **this app's tenant instance** (the
plugin resource), not the platform appliance. It creates a leader TCP route
on the lowest free port in 10000–29999 and the host firewall opens that one
port on every host:

```text
flynn addons:expose postgres
# default hostname <service>.<cluster-domain>, tls_mode=passthrough
# The cluster firewall will open TCP 10000.
```

Passthrough is required for Postgres: clients send an SSLRequest in plaintext
before TLS, so router TLS terminate breaks `libpq`. Point DNS (or the cluster
wildcard) at the hosts and connect with `sslmode=require`.

The platform appliance is a different discoverd service (`postgres`). Export
that only from a host if you need operator access; tenant apps should not
receive its superuser URL.

```text
flynn route:add tcp --service postgres --leader --domain postgres.example.com --tls-mode passthrough
```

The host daemon opens that port. Do not open 10000–29999 as a range. Remove a
tenant export with `flynn addons:unexpose postgres`. The daemon closes the
port. Run `sudo flynn-host firewall:unexpose PORT` only if you pinned it.
Treat the exported port as public;
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

`flynn pg:dump` writes one custom-format `pg_dump` per logical database on
this app's primary into object storage and prints the timestamp.
`flynn pg:backups` lists those timestamps. `flynn pg:restore` restores the
newest, and `flynn pg:restore <timestamp>` restores a listed timestamp into
the same database names. When the app has more than one Postgres resource,
pass the instance name: `flynn pg:dump postgresql-mist-20145`,
`flynn pg:backups postgresql-mist-20145`, and
`flynn pg:restore postgresql-mist-20145 20261111T160405Z`. The instance name
is enough on its own. The job cannot run on the database app, so that command
uses the user app that owns the database. Pass `-a` only to choose among
several attached user apps. `-f`, `--file`,
and `-q` are rejected. There is no dump file to download. The dashboard
Backup page is the same operation: it shows where backups are stored, a
warning when the last success is missing or older than 25 hours, **Back up
now**, a table of timestamps, and **Restore selected**. That page does not
show cluster-host commands.

The schedule runs on the `postgres-backup` discoverd leader, once a day per
primary, with the first dump due immediately. A postgres blobstore is refused
in both hosted and self-hosted mode. An app user sees that backups are
unavailable until an administrator configures object storage. Operators point
the blobstore at S3, MinIO, GCS, or Azure with
`flynn-host blobstore:set --migrate --delete` before dumping.

`flynn-host pg:dump` / `flynn-host pg:restore` dump the **platform** appliance
(controller, blobstore, plugin metadata on `platform-postgres`). They do not
dump a tenant instance. Cluster `flynn-host backup` uses `pg_dumpall` on that
same appliance; tenant instance volumes are not in the tarball, and tenant
dumps under `backups/` are not applied by cluster restore.

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

### Maintenance

`flynn pg:maintenance [--all] [<instances>]` moves the resources attached to
the current app onto the plugin's current image and engine. With one attached
resource the name is optional; otherwise pass a name, a comma-separated list,
or `--all`. A resource that is already current is reported as
`already current` and left running. `flynn-host pg:maintenance
<name>|<list>|--all` does the same for any resource on the cluster.

Both run `/bin/flynn-postgres-api task maintenance` in the plugin's current image with only the plugin's
API key: the `flynn` form on your app (it reads the key from the `postgres-plugin`
release, so it needs a cluster admin login), the `flynn-host` form on
`postgres-plugin`. Set `MAINTENANCE_AUTO=true` on the plugin app to run
maintenance when the plugin starts.

## Safety

This appliance is designed to provide full consistency and partition tolerance
for all operations that are committed to the write-ahead log (WAL). Note that
this guarantee does not apply to advisory locks, as they are specific to the
server they are acquired and are not persisted to the WAL.

There is currently no support for tuning, and data transfer during recovery is
not optimized, so we do not recommend using the appliance for applications that
have high throughput or many records.
