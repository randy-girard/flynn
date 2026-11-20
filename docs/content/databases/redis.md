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
flynn addons:create redis
```

The controller app is `redis-plugin` (`flynn-host ps`, `flynn -a redis-plugin`).
Provider name stays `redis`.

The plugin provides Redis from the Ubuntu 24.04 package set in a
single process configuration. Redis writes an append-only file on a persistent
volume, so data survives job restarts and `flynn-host update`. A follower is a
separate resource (`flynn redis:follow` or `flynn addons:create redis --follow`).
`flynn addons:create redis --follow <primary> --auto-failover` places that
replica on another host and promotes it if the primary job is lost, then
creates a replacement replica. Failover is asynchronous (RPO is replication
lag). A single-node cluster cannot use auto-failover. The volume is **not** part of `flynn-host backup`. Treat the data
as ephemeral: caching, development, and test use.

Each resource is its own Redis instance and must have a password (`requirepass`).
Provision fails if that password would be empty. The provider sets `tenant_safe`.
See [Plugins](../plugins.md).

## Usage

### Adding a server to an app

Redis is available after the operator installs the plugin. After you create
an app, provision a server with:

```text
flynn addons:create redis
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
`REDIS_URL`. `FLYNN_REDIS` (the Redis app name) and `FLYNN_REDIS_ROLE` stay on the
resource record. TLS for `addons:expose` is on 16379 by default, so
external clients use `rediss://` and `FLYNN_REDIS_TRUSTED_CERT`. In-cluster
`REDIS_URL` is `redis://` on 6379 so Sidekiq and redis-rb do not need to
trust Flynn's private CA. After a plugin update, leftover in-cluster
`rediss://` URLs on attached apps are rewritten to `redis://`. When the plugin
itself dials an instance over TLS (follower replication checks on 16379), it
verifies the server against that instance's own CA and host name.

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
`tls-port` **16379**, not plaintext 6379. You can only follow a resource that
belongs to the same app. Another tenant's resource is refused as not found.
Each follower has its own password. The leader's password is used only for
replication and is not in the follower's attachment env. After a keep-service
failover the promoted primary also accepts the old leader's password, so the
old leader's apps keep working. Followers created before per-follower
passwords keep their leader's password until an upgrade replaces them.

```text
flynn redis:follow
flynn redis:wait <follower>
flynn redis:promote <follower>
flynn redis:unfollow <follower>
flynn addons:create redis --follow <primary> --auto-failover
```

`redis:wait` prints live copy progress (full resync, then streaming catch-up).
The dashboard Followers tab has **Add follower** and the same progress.

`flynn-host redis:availability` is `manual` or `system` for the whole cluster.
Hosted and self-hosted use the same values. `manual` is the self-hosted
default and creates no platform replica. `system` is the default only for a
cluster that was already hosted when the controller migrated, and for a fresh
`flynn-host plugin:install hosted` that has no Redis resources yet. A cluster
admin sets it with that command or on Cluster → Settings. Changing it does
not restart user apps.

`system` keeps one platform replica per primary when another host can take
the volume. The watch promotes only that replica, after fencing the old
primary, and keeps `REDIS_URL` on `leader.<original>.discoverd`. User
followers stay read-only and are repointed at that name. A fenced primary
can be reused as the next platform replica. Replication is asynchronous. A
failover can drop writes the primary acknowledged after the last replica
offset, within 16 MiB. `master_sync_in_progress` is not caught up.
Synchronous replication stays off, and `min-replicas-to-write` stays unset.
**Remove** stops replication and deletes the replica, including while it is
still copying. Unfollow still leaves a standalone copy. Auto-failover
places the replica on another host, promotes it if the primary job is gone, then
recreates a replica. The plugin worker holding the failover lease promotes only
after it has itself seen the primary job gone on every 5-second check for 30
seconds; a lease change, or a gap of more than 10 seconds between checks,
starts the 30 seconds over. With the primary gone the replica's link is down, so the
watch promotes a follower whose link dropped no more than 30 seconds before the
primary job stopped; a follower that never finished its first sync, or lost
its link long before, is not promoted. Promotion restarts the follower's job
as a writable primary and the fenced old primary rejoins as its follower.
The rejoined old primary registers in discoverd as `<old-name>-follower`, so
`leader.<old-name>.discoverd` stays on the promoted primary when either job
restarts.
Followers of a primary with no running job, and follower jobs younger than two
minutes, are not replaced as broken. The replica avoids the primary's host when the
controller's host stats show another host with room for the runtime; a user
follower otherwise starts on any host. When an upgrade replaces a follower, the replacement takes
the old follower's name and is scaled back up to its original process count; a
failed switch restores both apps. A primary cannot be deleted while it still has
followers. After a failover the fenced old primary is the follower, so
`flynn addons:destroy` takes it first and then the promoted primary. The
promoted primary's claim on the old name (`FLYNN_REDIS_SERVICE_ALIAS`) does
not stop the old primary's app from being deleted: the controller's delete
guard counts an app that reads another app's data, not one that only answers
on its discoverd name. Postgres and MySQL `*_SERVICE_ALIAS` keys are treated
the same way.

### Dumping and restoring

`flynn redis:dump` writes an RDB dump. `flynn redis:restore` loads one. If
`-f` is omitted, dump writes stdout and restore reads stdin:

```text
flynn redis:dump -f db.dump
flynn redis:restore -f db.dump
```

Restore validates the upload before it touches live data: the file must start
with the `REDIS` header, be at least 18 bytes and pass `redis-check-rdb` when
the image ships it. A rejected upload leaves the running instance and its data
unchanged. The previous `dump.rdb` and AOF are moved to `restore-backup/` and
put back if the restored instance fails to start. `redis:dump` exits non-zero
(and writes nothing to stdout) when `redis-cli` fails or returns an empty or
non-RDB file.

### External access

Export Redis on a TCP route with a stable hostname. The host firewall opens
that one port:

```text
flynn addons:expose redis
# The cluster firewall will open that one port. Do not open 10000–29999 as a range.
```

The default hostname is the Redis app name on the cluster domain (from
`flynn redis`). Default TLS mode is passthrough: the appliance speaks
TLS on 16379, so external clients connect with TLS.

You can still create the route yourself:

```text
flynn -a redis-harbor-48291 route:add tcp --service redis-harbor-48291 --leader --domain redis.example.com --tls-mode passthrough
```

The host daemon opens that port. Remove with `flynn addons:unexpose redis`.
The daemon closes the port. See
[Production — Firewalling](../production.html.md#firewalling).

## Safety

No safety or availability guarantees are currently provided for the Redis
appliance. Data loss and inconsistency is likely. Any data stored should be
treated as ephemeral and only used for caching, development, and testing.
