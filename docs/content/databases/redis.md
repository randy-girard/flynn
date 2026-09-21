---
title: Redis
layout: docs
---

# Redis

Redis is a Flynn **plugin** (not part of the bootstrap tarball). Install it on a
cluster host, then provision from an app. See [Plugins](../plugins.md).

```text
sudo flynn-host plugin:install redis --ref v20260914.0.0
sudo flynn-host plugin:install https://github.com/randy-girard/flynn-plugin-redis.git --ref v20260914.0.0
sudo flynn-host plugin:install ../flynn-plugin-redis
flynn resource:add redis
```

The plugin provides Redis from the Ubuntu 24.04 package set in a
single process configuration. Redis writes an append-only file on a persistent
volume, so data survives job restarts and `flynn-host update`, but there are no
replicas and the volume is **not** part of `flynn-host backup`. Treat the data
as ephemeral: caching, development, and test use.

## Usage

### Adding a server to an app

Redis is available after the operator installs the plugin. After you create
an app, provision a server with:

```text
flynn resource:add redis
```

This will provision a Redis server as a Flynn app and configure your application
to connect to it.

### Connecting to the database

Provisioning the database will add a few environment variables to your app
release. `REDIS_HOST`, `REDIS_PORT`, and `REDIS_PASSWORD` provide connection
details for the database. `FLYNN_REDIS` is the name of the Redis app.

Flynn will also create the `REDIS_URL` environment variable which is utilized
by some libraries to configure connections. TLS on 6379 is on by default
(`REDIS_TLS_ENABLED=true`), so `REDIS_URL` uses the `rediss://` scheme and the
appliance CA is in `REDIS_TRUSTED_CERT`. The plaintext port is disabled while
TLS is on; use `rediss://` or `redis-cli --tls`.

### Connecting to a console

To connect to a console for the database, run `flynn redis:cli` (alias
`flynn redis redis-cli`). This does not require the Redis client to be installed
locally or firewall/security changes, as it runs in a container on the Flynn
cluster using the Redis appliance image. The console uses `REDIS_HOST` (`leader.<redis-app>.discoverd`), the
same host as `REDIS_URL`.

### Dumping and restoring

`flynn redis:dump` writes an RDB dump. `flynn redis:restore` loads one. If
`-f` is omitted, dump writes stdout and restore reads stdin:

```text
flynn redis:dump -f db.dump
flynn redis:restore -f db.dump
```

### External access

Export Redis on a TCP route with a stable hostname, then open the host port:

```text
flynn resource:expose redis
sudo flynn-host firewall:expose PORT   # on every host
```

The default hostname is the Redis app name on the cluster domain (from
`FLYNN_REDIS`). Default TLS mode is passthrough: the appliance already speaks
TLS on 6379, so external clients connect with TLS and trust
`REDIS_TRUSTED_CERT` (skip hostname verification if the route hostname is not
on the certificate).

You can still create the route yourself:

```text
flynn -a $(flynn env:get FLYNN_REDIS) route:add tcp --service $(flynn env:get FLYNN_REDIS) --leader --domain redis.example.com --tls-mode passthrough
sudo flynn-host firewall:expose PORT
```

Remove with `flynn resource:unexpose redis` then
`sudo flynn-host firewall:unexpose PORT`. See
[Production — Firewalling](../production.html.md#firewalling).

## Safety

No safety or availability guarantees are currently provided for the Redis
appliance. Data loss and inconsistency is likely. Any data stored should be
treated as ephemeral and only used for caching, development, and testing.
