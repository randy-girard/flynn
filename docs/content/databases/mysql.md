---
title: MariaDB
layout: docs
---

# MariaDB

MariaDB is a Flynn **plugin** (not part of the bootstrap tarball). Install it on a
cluster host, then provision from an app. See [Plugins](../plugins.md).

```text
sudo flynn-host plugin:install mysql --ref vX
sudo flynn-host plugin:install https://github.com/randy-girard/flynn-plugin-mariadb.git --ref vX
sudo flynn-host plugin:install ../flynn-plugin-mariadb
flynn resource:add mysql
```

The plugin provides MariaDB 10.11 LTS in a highly-available configuration with
automatic provisioning. It automatically fails over to a synchronous replica
with no loss of data if the primary server goes down. A single-host
(`SINGLETON`) cluster runs one peer; when a third host joins, the scheduler
promotes the appliance to a three-peer replica set automatically.

## Usage

### Adding a database to an app

MariaDB is available after the operator installs the plugin. After you create
an app, provision a database with:

```text
flynn resource:add mysql
```

This will provision a database on the MariaDB cluster and configure your
application to connect to it.

By default, MariaDB is not running in the Flynn cluster. The first time you
provision a database, MariaDB will be started and configured.

### Connecting to the database

Provisioning the database will add a few environment variables to your app
release. `MYSQL_HOST`, `MYSQL_PORT`, `MYSQL_USER`, `MYSQL_PWD`, and
`MYSQL_DATABASE` provide connection details for the database and are used
automatically by many MySQL clients. `FLYNN_MYSQL` is the name of the MariaDB
app.

Flynn will also create the `DATABASE_URL` environment variable which is utilized
by some frameworks to configure database connections. TLS on 3306 is on by
default (`MYSQL_TLS_ENABLED=true`); the appliance CA is in
`MYSQL_TRUSTED_CERT`, and `DATABASE_URL` carries `tls=skip-verify` so Go
clients work with the private CA (pin `MYSQL_TRUSTED_CERT` to verify).
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

`flynn mysql:restore` loads a database dump from a local file into a Flynn MariaDB
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
flynn resource:expose mysql
# default hostname mariadb.<cluster-domain>, tls_mode=passthrough
sudo flynn-host firewall:expose PORT   # on every host
```

Passthrough is required for MariaDB/MySQL: clients negotiate SSL after a
plaintext handshake, so router TLS terminate breaks those clients. Point DNS
(or the cluster wildcard) at the hosts and connect with TLS to
`mariadb.<cluster-domain>:PORT`.

You can still create the route yourself:

```text
flynn -a mariadb route:add tcp --service mariadb --leader --domain mariadb.example.com --tls-mode passthrough
sudo flynn-host firewall:expose PORT
```

External clients must use TLS and trust `MYSQL_TRUSTED_CERT`; in-cluster
clients keep using the discoverd host in `DATABASE_URL`.
Remove with `flynn resource:unexpose mysql` then
`sudo flynn-host firewall:unexpose PORT`. See
[Production — Firewalling](../production.html.md#firewalling).

## Safety

This appliance is designed to provide full consistency and partition tolerance
for all operations that are committed to the binlog. However, the semi-sync
replication configuration is not as well tested as our Postgres appliance, so
we do not have full confidence in the system yet.

There is currently no support for tuning, and data transfer during recovery is
not optimized, so we do not recommend using the appliance for applications that
have high throughput or many records.
