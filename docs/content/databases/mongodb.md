---
title: MongoDB
layout: docs
---

# MongoDB

MongoDB is a Flynn **plugin** (not part of the bootstrap tarball). Install it on a
cluster host, then provision from an app. See [Plugins](../plugins.md).

```text
sudo flynn-host plugin:install mongodb --ref vX
sudo flynn-host plugin:install https://github.com/randy-girard/flynn-plugin-mongodb.git --ref vX
sudo flynn-host plugin:install ../flynn-plugin-mongodb
flynn addons:create mongodb
```

The plugin controller app is `mongodb-plugin` (`flynn-host ps`). The provider
API is `http://mongodb-plugin.discoverd/databases`. Provision with
`flynn addons:create mongodb`.

Each `flynn addons:create mongodb` starts a new instance: its own app, one
volume, and one node (a one-member replica set). It does not join another
tenant's set. `flynn mongodb:scale` grows that resource (see
[Scaling the cluster](#scaling-the-cluster)). The engine is MongoDB 7.0. The
image includes `mongod` 7.0.43 (ubuntu2204 build), database tools 100.19.1, and
`mongosh` 2.13.0 (not the legacy `mongo` shell). They are installed from
official tarballs vendored in the plugin repo and checked against SHA-256
sums; image builds do not use `repo.mongodb.org`.

Each resource gets a user with `dbOwner` on its own database. Provision stores a
connection budget (default 20) and rejects `max_connections` above
`MONGO_MAX_CONNECTIONS`. It does not change the server-wide
`maxIncomingConnections` setting. The provider sets `tenant_safe`. See
[Plugins](../plugins.md).

## Usage

### Adding a database to an app

MongoDB is available after the operator installs the plugin. After you create
an app, provision a database with:

```text
flynn addons:create mongodb
```

This starts a new one-node replica set for the app and configures the app to
connect to it. The command returns after the instance is scheduled.

### Connecting to the database

Provisioning the database will add a few environment variables to your app
release. `MONGO_HOST`, `MONGO_PORT`, `MONGO_USER`, `MONGO_PWD`, and
`MONGO_DATABASE` provide connection details for the database and are used
automatically by many MongoDB clients. `FLYNN_MONGO` is the name of the
MongoDB app.

Every provision and attach sets `FLYNN_MONGODB_<COLOR>_URL` unless `--as NAME`
names the attachment (`NAME_URL`, or `FLYNN_MONGODB_<COLOR>_URL` when `--as` is a
color short name such as `AMBER`). A new provision also sets `DATABASE_URL`
when that key is free; attaching an existing resource does not. It is utilized
by some frameworks to configure database connections. It
points at `leader.<app>.discoverd` and stays the same across scaling and
failover. TLS is on by default
(`MONGO_TLS_ENABLED=true`). The URL carries `tls=true` and
`directConnection=true`. The app release also receives `FLYNN_MONGODB_TRUSTED_CERT`,
the appliance CA, and clients must use it to verify the server certificate.
`leader.<app>.discoverd` is on that certificate. `mongod` runs in `preferTLS`
mode, so replication and older in-cluster clients can still connect without TLS.

### Scaling the cluster

MongoDB has no followers or joined resources. Each resource is one cluster, and
`flynn addons:create mongodb --follow` is rejected. Change the number of nodes
with:

```text
flynn mongodb:scale <n> [<resource>]
```

`<n>` is 1 to 7. An even count is accepted with a warning: it tolerates no
more failures than the odd count below it.

`flynn-host mongodb:availability` is `manual` or `system`. Hosted clusters
start on `system`. Self-hosted clusters start on `manual`. Either cluster can
flip it, and `tenancy:mode` does not change it afterward. The same code runs
for a large self-hosted cluster.

`manual` starts one member and leaves the formation to `mongodb:scale`.
`system` grows each resource one member at a time up to three when three hosts
are registered, and up to the host count on a smaller cluster. It does not
shrink when set back to `manual`. On one or two hosts failover is unavailable.
A set is protected only while three members are live on three hosts. Losing one
of those hosts leaves a majority of two. Two members elect only while both are
up, because each member's vote defaults to 1.

The scheduler does not promote a tenant MongoDB app. Provision returns after
the first member is scheduled. The plugin adds the next member only after the
previous voter is committed, so create does not overlap an env flip.

* Scaling up from one node first deploys a release with `SINGLETON=false`,
  which briefly restarts the primary, then adds one member at a time.
* Scaling from more than one node back to 1 is rejected.
* Scaling down removes only async peers, never the primary or the sync peer.
  Each removed node is taken out of the replica set with `replSetReconfig`
  before it stops.

`DATABASE_URL` does not change when you scale.

The plugin checks each replica set every 30 seconds and records topology
changes as dashboard events `P19` (node added), `P20` (node removed), `P21`
(node rejoined), and `P22` (primary elected). Drift correction adds a missing
live peer back without a vote (`P21`) and removes at most one stale member per
pass (`P20`).

### Connecting to a console

To connect to a `mongosh` console for the database, run `flynn mongodb:cli`
(alias `flynn mongodb mongo`). This does not require the MongoDB client to be
installed locally or firewall/security changes, as it runs in a container on
the Flynn cluster.

### Dumping and restoring

`flynn mongodb:dump` writes one `mongodump --archive --oplog` of
`leader.<instance>.discoverd` to the platform blobstore and prints a
timestamp. The dump includes every database except `local`, plus `admin`
users and roles. It runs as the instance superuser `flynn`, not the app
user, and uses `--ssl --tlsInsecure`. The password is not a process
argument. `-f` and `-q` are rejected. There is no dump file to download.

`flynn mongodb:backups` lists timestamps. `flynn mongodb:restore` restores
the newest onto that same primary with `mongorestore --archive --oplogReplay
--drop`. `flynn mongodb:restore <timestamp>` restores a listed timestamp. A
path or object id is rejected.

The object key is `backups/<owner>/<resource UUID>/<timestamp>/archive`.
Empty owner uses `cluster`. The schedule runs on the `mongodb-backup`
discoverd leader, once a day per resource, with the first dump due
immediately. A postgres blobstore is refused in both hosted and
self-hosted mode. Run `flynn-host blobstore:set --migrate --delete` first.
`production:check` fails in both modes until a resource has a dump newer
than 24 hours plus one hour.

The dashboard Backup page lists those timestamps. It has no file input.
`flynn-host backup` does not contain the volume or this object. A
`mongodb.archive.gz` appliance dump is not this backup.

To copy a database to a MongoDB that is not Flynn, expose the instance and
run `mongodump` yourself. The Flynn command does not write that file.

### External access

Export the database on a TCP route with a stable hostname, then open the host
port:

```text
flynn addons:expose mongodb
sudo flynn-host firewall:expose PORT   # on every host
```

Default TLS mode is passthrough so `mongod` completes the TLS handshake
itself. External clients connect with `tls=true` and trust
`FLYNN_MONGODB_TRUSTED_CERT` (skip hostname verification if the route hostname is not
on the certificate).

You can still create the route yourself, on the instance app (the value of
`FLYNN_MONGO`), not on an app named `mongodb`:

```text
flynn -a <instance-app> route:add tcp --service <instance-app> --leader --domain mongodb.example.com --tls-mode passthrough
sudo flynn-host firewall:expose PORT
```

Remove with `flynn addons:unexpose mongodb` then
`sudo flynn-host firewall:unexpose PORT`. See
[Production — Firewalling](../production.html.md#firewalling).

## Metrics

Every instance release sets `FLYNN_METRICS_PORT=9180`. Each `mongod` job serves
`GET /metrics/log` on that port with these `sample#` series:
`service_available`, `connections`, `ops_per_sec`, `members`,
`healthy_members`, `replica_lag_seconds`, `mem_bytes`, and `wiredtiger_bytes`.
The dashboard sample also carries `db_size_bytes` and `index_size_bytes`, summed
from `dbStats` over user databases.
A key that fails to read is omitted. An unreachable `mongod` reports
`service_available=0 errors=1`. Dashboard samples are posted once per UTC
minute per controller resource. See
[Plugins — Resource metrics line](../plugins.md#resource-metrics-line).

## Upgrades

A plugin update rolls the replica set members one at a time, primary last,
through the sirenia deploy strategy. One-node resources have brief downtime.

## Dashboard

The MongoDB data source page has the tabs Overview (attached apps and Nodes),
Metrics, Databases, Users, and Backup.

## Safety

A one-node resource does not fail over. After `mongodb:scale` above 1, or
after the scheduler has promoted the resource on a three-host cluster, loss of
the primary can elect another member. The replica
set is [asynchronous](https://docs.mongodb.com/manual/replication/) by
default.

To guarantee durability of writes, the [majority write
concern](https://docs.mongodb.com/manual/reference/write-concern/) should be
used. This will ensure writes are replicated before being acknowledged by the
client. If read consistency/isolation is required then the [majority read
concern](https://docs.mongodb.com/manual/reference/read-concern/) must also be
utilised. This setting ensures writes can only be read if they have been
committed to the other cluster members, it is otherwise possible to return
uncommitted data and potentially stale reads.
