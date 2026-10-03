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

The controller app is `redis-plugin` (`flynn-host ps`, `flynn -a redis-plugin`).
Provider name stays `redis`.

The plugin provides Redis from the Ubuntu 24.04 package set in a
single process configuration. Redis writes an append-only file on a persistent
volume, so data survives job restarts and `flynn-host update`. A follower is a
separate resource (`flynn redis:follow` or `flynn resource:add redis --follow`).
The volume is **not** part of `flynn-host backup`. Treat the data
as ephemeral: caching, development, and test use.

Each resource is its own Redis instance and must have a password (`requirepass`).
Provision fails if that password would be empty. The provider sets `tenant_safe`.
See [Plugins](../plugins.md).

## Usage

### Adding a server to an app

Redis is available after the operator installs the plugin. After you create
an app, provision a server with:

```text
flynn resource:add redis
```

This will provision a Redis server as a Flynn app and configure your application
to connect to it. The command returns after the instance is scheduled; Redis
finishes starting in the background. Check later with `flynn redis:wait` or the
dashboard, which live-updates while it starts.

### Connecting to the database

Provisioning the database will add environment variables to your app
release. A new provision sets `REDIS_URL` when that key is free, plus
`FLYNN_REDIS_<COLOR>_URL`. `--as CACHE` sets `CACHE_URL`. `--as AMBER`
sets `FLYNN_REDIS_AMBER_URL`. Attaching an existing resource does not set
`REDIS_URL`. `FLYNN_REDIS` (the Redis app name) and `REDIS_ROLE` stay on the
resource record. TLS for `resource:expose` is on 16379 by default, so
external clients use `rediss://` and `REDIS_TRUSTED_CERT`. In-cluster
`REDIS_URL` is `redis://` on 6379 so Sidekiq and redis-rb do not need to
trust Flynn's private CA. After a plugin update, leftover in-cluster
`rediss://` URLs on attached apps are rewritten to `redis://`.

### Connecting to a console

To connect to a console for the database, run `flynn redis:cli` (alias
`flynn redis redis-cli`). This does not require the Redis client to be installed
locally or firewall/security changes, as it runs in a container on the Flynn
cluster using the Redis appliance image. The console uses
`leader.<redis-app>.discoverd`, the same host as `REDIS_URL`. User jobs
resolve that leader name only when Redis is attached to that app.

### Followers

A follower is a new Redis resource, not a second process on the leader. It
copies the leader with `REPLICAOF` and stays read-only until you promote or
unfollow it. With TLS (the default) the replica talks to the primary
`tls-port` **16379**, not plaintext 6379.

```text
flynn redis:follow
flynn redis:wait <follower>
flynn redis:promote <follower>
flynn redis:unfollow <follower>
```

`redis:wait` prints live copy progress (full resync, then streaming catch-up).
The dashboard Followers tab has **Add follower** and the same progress. A
primary cannot be deleted while it still has followers.

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
`flynn redis`). Default TLS mode is passthrough: the appliance speaks
TLS on 16379, so external clients connect with TLS.

You can still create the route yourself:

```text
flynn -a redis-harbor-48291 route:add tcp --service redis-harbor-48291 --leader --domain redis.example.com --tls-mode passthrough
sudo flynn-host firewall:expose PORT
```

Remove with `flynn resource:unexpose redis` then
`sudo flynn-host firewall:unexpose PORT`. See
[Production — Firewalling](../production.html.md#firewalling).

## Safety

No safety or availability guarantees are currently provided for the Redis
appliance. Data loss and inconsistency is likely. Any data stored should be
treated as ephemeral and only used for caching, development, and testing.
