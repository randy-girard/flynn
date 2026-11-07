---
title: MySQL
layout: docs
---

# MySQL

MySQL is a Flynn **plugin** (not part of the bootstrap tarball). The engine is
MariaDB 10.11 LTS. Install it on a cluster host, then provision from an app.
See [Plugins](../plugins.md). Canonical install name is `mysql`; `mariadb` is a
deprecated alias. The plugin app is `mysql-plugin` (provider
`http://mysql-plugin.discoverd/databases`).

```text
sudo flynn-host plugin:install mysql --ref vX
sudo flynn-host plugin:install mariadb --ref vX
sudo flynn-host plugin:install https://github.com/randy-girard/flynn-plugin-mysql.git --ref vX
sudo flynn-host plugin:install ../flynn-plugin-mysql
flynn addons:create mysql
```

Each `flynn addons:create mysql` starts a new instance: its own app, one
volume, and one process. The app strategy is `one-down-one-up`. One node does
not fail over, and a third Flynn host does not add peers. There is no
`mysql:scale`.

A follower is another resource (`flynn mysql:follow` or
`flynn addons:create mysql --follow`). It is an async read-only replica of
that leader, and it copies the leader's users and passwords. It does not
follow another follower. `--auto-failover` requires `--follow`. After the
primary job has been down for 30 seconds, that follower is promoted, the old
primary is fenced, and a replacement replica is started. Replication is
asynchronous, so the latest writes can be lost. There is no synchronous
replica.

Each resource user is created with `MAX_USER_CONNECTIONS` (default 20, override
with `MYSQL_MAX_USER_CONNECTIONS` on the API). The provider sets `tenant_safe`.
See [Plugins](../plugins.md).

Instances run with `performance_schema` on. The dashboard resource page has a
**Slow queries** tab listing the statement digests with the highest mean
latency (or, when `performance_schema` is off, queries running longer than
500 ms). Instances created earlier enable it on their next restart.

## Usage

### Adding a database to an app

MySQL is available after the operator installs the plugin. After you create
an app, provision a database with:

```text
flynn addons:create mysql
```

This starts a new MySQL instance for the app and configures the app to
connect to it. The command returns after the instance is scheduled; MySQL
finishes starting in the background.

### Followers

A follower is a separate read-only instance, not a second process on the
leader. It copies the leader's users and passwords and stays caught up
asynchronously.

```text
flynn addons:create mysql --follow <resource>
flynn mysql:follow <resource>
flynn mysql:follow <resource> --auto-failover
flynn mysql:wait <follower>
flynn mysql:promote <follower>
flynn mysql:unfollow <follower>
```

`--auto-failover` places the replica on another host. If the primary job
stays down for 30 seconds, Flynn promotes that follower, fences the old
primary, and starts a replacement replica. One node, with no follower, does
not fail over. The dashboard Followers tab can remove a follower while it is
still replicating. Remove stops replication and deletes the replica. Unfollow
leaves a standalone writable copy.

### Connecting to the database

Provisioning the database will add a few environment variables to your app
release. `MYSQL_HOST`, `MYSQL_PORT`, `MYSQL_USER`, `MYSQL_PWD`, and
`MYSQL_DATABASE` provide connection details for the database and are used
automatically by many MySQL clients. `FLYNN_MYSQL` is the instance app
name. `MYSQL_HOST` is `leader.<that-app>.discoverd`. Do not use
`leader.mysql.discoverd`.

Every provision and attach sets `FLYNN_MYSQL_<COLOR>_URL` unless `--as NAME` names
the attachment (`NAME_URL`, or `FLYNN_MYSQL_<COLOR>_URL` when `--as` is a color
short name such as `AMBER`). A new provision also sets `DATABASE_URL` when the
app does not already have it; attaching an existing resource does not. `DATABASE_URL`
is utilized by some frameworks to configure database connections. Colors are assigned
per app and instance and are not reused while the instance exists, so two apps on one
instance never share a key. While the app is attached, the controller rejects
`flynn env set` / `env unset` for that key. TLS on 3306 is on by
default (`MYSQL_TLS_ENABLED=true`); the appliance CA is in
`FLYNN_MYSQL_TRUSTED_CERT`, and `DATABASE_URL` carries `tls=skip-verify` so Go
clients work with the private CA (pin `FLYNN_MYSQL_TRUSTED_CERT` to verify).
Plaintext is still accepted inside the cluster for replication and older
clients.

### Connecting to a console

To connect to a `mysql` console for the database, run `flynn mysql:cli` (alias
`flynn mysql console`). This does not require the MySQL client to be installed
locally or firewall/security changes, as it runs in a container on the Flynn
cluster.

### Dumping and restoring

The Flynn CLI provides commands for exporting and restoring database dumps.

`flynn mysql:dump` saves a complete copy of the database schema and data to a local file.

```text
$ flynn mysql:dump -f latest.dump
60.34 MB 8.77 MB/s
```

The file can be used to restore the database with `flynn mysql:restore`. It may
also be imported into a local MySQL database that is not managed by Flynn with
`mysql`:

```text
$ mysql -D mydb < latest.dump
```

`flynn mysql:restore` loads a database dump from a local file into a Flynn MySQL
database. Any existing tables and database objects will be dropped before they
are recreated.

```text
$ flynn mysql:restore -f latest.dump
62.29 MB / 62.29 MB [===================] 100.00 % 4.96 MB/s
```

The restore command may also be used to restore a database dump from another non-Flynn
MySQL database, use `mysqldump` to create a dump file:

```text
$ mysqldump mydb > mydb.dump
```

### External access

Export the database on a TCP route with a stable hostname, then open the host
port:

```text
flynn addons:expose mysql
# default hostname mysql.<cluster-domain>, tls_mode=passthrough
sudo flynn-host firewall:expose PORT   # on every host
```

Passthrough is required for MySQL: clients negotiate SSL after a
plaintext handshake, so router TLS terminate breaks those clients. Point DNS
(or the cluster wildcard) at the hosts and connect with TLS to
`mysql.<cluster-domain>:PORT`.

You can still create the route yourself, on the instance app (the value of
`FLYNN_MYSQL`), not on `mysql-plugin`:

```text
flynn -a <instance-app> route:add tcp --service <instance-app> --leader --domain mysql.example.com --tls-mode passthrough
sudo flynn-host firewall:expose PORT
```

External clients must use TLS and trust `FLYNN_MYSQL_TRUSTED_CERT`; in-cluster
clients keep using the discoverd host in `DATABASE_URL`.
Remove with `flynn addons:unexpose mysql` then
`sudo flynn-host firewall:unexpose PORT`. See
[Production — Firewalling](../production.html.md#firewalling).

## Safety

A single node has no replica. Writes acknowledged by that node are on that
volume only. A follower replicates asynchronously, so a failover can lose
writes that had not reached it yet. This is not a synchronous three-peer set.

There is currently no support for tuning, and data transfer during recovery is
not optimized, so we do not recommend using the appliance for applications that
have high throughput or many records.
