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

The plugin provides MongoDB 7.0 in a highly-available configuration with
automatic provisioning. Replication is implemented using MongoDB's replica set
feature. Each resource is one replica set; change its node count with
`flynn mongodb:scale` (see [Scaling the cluster](#scaling-the-cluster)). The
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

This will provision a database on the MongoDB cluster and configure your
application to connect to it.

By default, MongoDB is not running in the Flynn cluster. The first time you
provision a database, MongoDB will be started and configured.

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

`<n>` is 1 to 7. An even count is accepted with a warning.

* Scaling up from one node first deploys a release with `SINGLETON=false`,
  which briefly restarts the primary.
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

The Flynn CLI provides commands for exporting and restoring database dumps.

`flynn mongodb:dump` saves a complete copy of the database to a local file.
The in-cluster job talks to 27017 with mongodump `--ssl --tlsInsecure` (the
database tools do not accept mongosh's `--tls` flag).

```text
$ flynn mongodb:dump -f latest.dump
60.34 MB 8.77 MB/s
```

The file can be used to restore the database with `flynn mongodb:restore`. It may
also be imported into a local MongoDB database that is not managed by Flynn with
`mongorestore` and `tar` to extract the Flynn dump:

```text
$ mkdir dump # create a temporary directory for the dump
$ tar -x -C tmp latest.dump # extract the dump to the temporary directory
$ mongorestore tmp/* # you will need to use --host and auth flags as appropriate
```

`flynn mongodb:restore` loads a database dump from a local file into a Flynn
MongoDB database. Any existing collections and objects will be dropped before
restoring data.

```text
$ flynn mongodb:restore -f latest.dump
62.29 MB / 62.29 MB [===================] 100.00 % 4.96 MB/s
```

The restore command may also be used to restore a database dump from another
non-Flynn MongoDB database, use `mongodump` and `tar` to create a dump file:

```text
$ mkdir tmp # create a temporary directory
$ mongodump -o tmp # you will need to use --host and auth flags as appropriate
$ cd tmp
$ tar -cf ../mydb.dump .
```

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

You can still create the route yourself:

```text
flynn -a mongodb route:add tcp --service mongodb --leader --domain mongodb.example.com --tls-mode passthrough
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

The MongoDB appliance uses a [replica
set](https://docs.mongodb.com/manual/replication/) for replication and is unsafe
by default, as all operations are replicated asynchronously.

To guarantee durability of writes, the [majority write
concern](https://docs.mongodb.com/manual/reference/write-concern/) should be
used. This will ensure writes are replicated before being acknowledged by the
client. If read consistency/isolation is required then the [majority read
concern](https://docs.mongodb.com/manual/reference/read-concern/) must also be
utilised. This setting ensures writes can only be read if they have been
committed to the other cluster members, it is otherwise possible to return
uncommitted data and potentially stale reads.
