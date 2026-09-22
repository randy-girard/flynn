---
title: Security
layout: docs
toc_min_level: 2
---

# Security

If you have an issue to report, please jump to the [reporting
issues](#reporting-issues) section.

Security is extraordinarily important to us.

Because Flynn is an integrated platform that we control end-to-end we are able
to implement many best practices by default and deploy technologies that would
be extremely difficult for users to take advantage of on their own.

This community fork is still working toward secure-by-default. Until that is
true, it is important to understand the current security properties of Flynn.

## Distribution Security

All binaries that we provide including `flynn-host`, the `flynn` CLI tool, and
container images are distributed using [GitHub
Releases](https://github.com/randy-girard/flynn/releases). Content is served over
HTTPS.

## Internal Communication

Flynn uses several ports to communicate internally. The host HTTP API
authenticates with `FLYNN_HOST_AUTH_KEY`, discoverd's HTTP API with
`DISCOVERD_AUTH_KEY`, and controller, tarreceive, and blobstore with the
cluster key (or a scoped token). Other internal services still rely on
network isolation, so access to these ports must not be exposed to the
Internet. A firewall must be configured so that the only Flynn ports
accessible are 80 and 443 to prevent compromise. Access to unauthenticated
internal ports is equivalent to root access, so be careful. After
install, `flynn-host firewall` manages extra peer IPs and TCP ports on the
host UFW rules; see [Production — Firewalling](production.html.md#firewalling).
`flynn resource:expose` prints `flynn-host firewall:expose` when a datastore is
exported on a TCP port in 3000–3500. Treat those ports as public; prefer TLS
passthrough (backend certs) or terminate (`--auto-tls`). Postgres enables
`ssl=on` with a cluster-generated certificate; in-cluster `sslmode=disable`
clients still work.

The flynn-host HTTP API (`:1113`) uses `FLYNN_HOST_AUTH_KEY` (the `Auth-Key`
header or HTTP Basic password). Bootstrap writes one cluster-wide key to
`/etc/flynn/host.json` (mode `0600`) via `POST /host/auth-key` and restarts
the daemon. After that, host-to-host update pulls and the `flynn-host` CLI
present the same key.

When no key is configured (fresh host, missing `host.json`, or before
bootstrap) the API **fails closed**, with these exceptions:

* `GET /host/status` stays unauthenticated (health checks and bootstrap host
  discovery).
* First-time `POST /host/auth-key` is trust-on-first-use from any peer,
  including advertised subnet IPs. Multi-node bootstrap runs
  `configure-host-auth` on one coordinator and must reach every host's
  `:1113`, not only loopback.
* Every other method is `401` unless the client is on **this host**: TCP
  loopback (`127.0.0.0/8` or `::1`), a **Unix domain socket**, or a **local
  interface IP**. `flynn-host` often listens on `--listen-ip` rather than
  `127.0.0.1`; discoverd, flannel, and the image builder notify that address
  from the same machine. Job, volume, and update APIs stay closed to other
  hosts on the subnet.

After a key is set, loopback is not a bypass; the request must present
`authKey`. Forwarded headers (`X-Forwarded-For`, `X-Real-IP`) are ignored.

`flynn-host init` does **not** generate a unique per-host key. A random key
at init would desynchronize multi-node bootstrap, which installs one shared
secret. If `FLYNN_HOST_AUTH_KEY` is already in the environment, init persists
it into `host.json`. Joining hosts should set that cluster key before the
daemon listens. Bootstrap still prefers `127.0.0.1` when configuring the
local host (faster); remote peers use the advertised address and empty-key
TOFU on `POST /host/auth-key` only.

Access to the controller is available via HTTPS over port 443, and
a randomly generated bearer token is used for authentication. Cluster
administrator access is the install key (`ClusterKey`), a dashboard JWT
with scope `cluster:admin`, or scope `*`. A JWT with empty scopes and
empty app grants is not an administrator; it has no controller access.
The TLS certificate used for communication is generated during installation
(self-signed). Configure Let's Encrypt after bootstrap with
`flynn-host acme:configure --email=<you> --agree-tos` and
`flynn-host acme:enable-system-routes` so the dashboard and controller
present a trusted certificate; see [Apps — HTTPS](apps.md#https).
A cryptographic hash of the certificate is pinned as part of the CLI
configuration string to prevent man-in-the-middle attacks.

discoverd's HTTP API (`:1111`) requires `DISCOVERD_AUTH_KEY` (a 128-bit
secret generated at bootstrap, the same size as `CONTROLLER_KEY` /
`FLYNN_HOST_AUTH_KEY`). System jobs receive the key in their environment;
`flynn-host` persists it in `/etc/flynn/host.json` and injects it into
system-class containers. `flynn-host bootstrap --from-backup` writes that
key onto each host and restarts the daemon before starting discoverd, so
the host can register and `wait-hosts` can finish. The client sends it as an `Auth-Key` header
(or HTTP basic password). `/ping` and `/.well-known/status` stay
unauthenticated so health checks work. DNS on `:53` is unauthenticated
because user jobs need it. User and build jobs still cannot open
`:1111` (host iptables). Do not bind discoverd only to loopback; hosts
need peer HTTP.

The controller no longer publishes `AUTH_KEY` (the cluster admin key) in
discoverd instance metadata. `GET /services/controller/instances` does
not return that secret. System consumers (router, status, updater,
flynn-host CLI) read `CONTROLLER_KEY` or `AUTH_KEY` from their
environment and only fall back to instance meta during mixed-version
rolling updates.

## Applications

Applications run in their own network namespace on the overlay. User jobs
cannot open connections to other user jobs or to internal Flynn services
(`controller`, `blobstore`, `postgres-api`, …). They may reach provisioned
datastores only at the leader host Flynn put in `DATABASE_URL` /
`REDIS_URL` / etc., and only on the database protocol ports (Postgres
5432, MariaDB 3306, MongoDB 27017, Redis 6379, Kafka 9092, ClickHouse
9440/8443/8123/9000). Appliance admin HTTP on those same IPs (Postgres
5433, MariaDB 3307, MongoDB 27018, Redis 6380, Kafka 9095, ClickHouse
9090) is blocked from user jobs. Those admin APIs (`/backup`, `/stop`,
Kafka topic/group management, ClickHouse database management) require
the cluster controller key (HTTP basic or `Authorization: Bearer`).
`GET /.well-known/status` stays unauthenticated for health checks.
Each Postgres role can CONNECT only to its own database
(`PUBLIC` CONNECT is revoked), so a user job cannot open the controller,
router, or blobstore databases. Cross-app HTTP still works through routes you
add (the router). System appliances keep a full overlay mesh.

Build jobs (slugbuilder / dockerbuilder) are a separate overlay class. They
cannot reach user jobs or the host control plane, but they can still reach
cluster services the build needs, including blobstore. Blobstore HTTP
requires the cluster `AUTH_KEY` / `CONTROLLER_KEY` (Basic or Bearer) for
object GET/PUT/DELETE. `/.well-known/status` and `HEAD /` stay
unauthenticated so bootstrap and health checks work. Per-app
`BUILD_CACHE_URL` query tokens are HMAC-SHA256 of the app ID under the
cluster key and are verified server-side; a token is valid only for that
app's `-cache.tgz` / `-docker-cache.tgz` objects. A scoped
`build:artifacts` token (the same one create-artifact uses for the
controller) may GET/PUT `/slugs/` and `/tarreceive/` paths only.

When gitreceive can mint a scoped build token it mounts it at
`/run/secrets/controller_token` and does **not** put `CONTROLLER_KEY` in
the build job environment (it would otherwise remain in the container
config and `/proc` environ). When `ACCESS_TOKEN_SIGNING_KEY` is unset,
gitreceive still injects `CONTROLLER_KEY` so older hosts keep working;
`build.sh` relocates that key to `/run/secrets/controller_key` and unsets
the env before buildpack code runs. Treat that fallback as a temporary
upgrade path, not a security boundary.

`flynn-host update` copies missing cluster secrets onto system-app
releases (blobstore `AUTH_KEY` / `ACCESS_TOKEN_KEY`, postgres
`CONTROLLER_KEY`, `DISCOVERD_AUTH_KEY` on jobs that talk to discoverd,
and the same controller key onto redis appliances, router, and acme).
`flynn-host plugin:update` / `plugin:install` do the same for plugin
apps. Catalog plugins print that they receive cluster secrets and
continue. Third-party plugins prompt on a TTY (cluster-admin equivalent);
pass `--yes` for non-interactive installs. You do not need to `env:set`
those keys by hand after an upgrade.
Rotating a key is still a separate operator step; see
[Production — Controller Keys](production.html.md#controller-keys).

User and build jobs cannot open the host control plane: SSH (`:22`),
host HTTP (`:80`/`:443`), the host API, or discoverd (`:1111`).
DNS to the overlay gateway (`:53`) is allowed. flynn-host registers user
HTTP backends with discoverd; the job never receives a `DISCOVERD` URL.

`flynn -a controller pg:psql` (and the same for `blobstore` / other system
apps) requires the cluster controller key. Dashboard tokens scoped to user
apps cannot open those consoles. Treat the key from `flynn cluster:add` as
root.

`git push` to gitreceive requires the cluster controller key, `cluster:admin`,
or `app:deploy` on that app (or a coarser grant that expands to it, such as
`app:write` / `app:admin`). A valid token for one app cannot push to another
app by name. View-only (`app:read`) tokens cannot push. GitHub webhooks at
`POST /github/webhook` are authenticated with the webhook HMAC, not git
credentials.

Dashboard JWTs hide builder process types (`slugbuilder`, `dockerbuilder`,
`slugrunner`) from jobs, logs, formations, and release process maps. Setting
those process limits requires the cluster controller key (`flynn limit:set` or
flynn-host). Path-based
HTTP routes (`example.com/api`) can only be created with
`flynn-host route:add`. App-scoped tokens with `app:routes:write` can only
point a route at a discoverd service this app owns (`$APP-web` /
`$APP-$TYPE`, or a `service` declared on the current release). Routing to
system services (`controller`, `blobstore`, `postgres-api`, dashboard, www,
discovery) requires the cluster controller key.

Applications run in a user namespace: container UID 0 is an unprivileged
host UID (≥ 1_000_000). Overlay squashfs layers stay owned by host 0/5000
on disk; idmapped mounts make them appear as 0/5000 inside the job, so
`/.containerconfig` (0600) and image files stay readable. The overlay
root is chowned to the mapped UID so jobs can create directories at `/`.
Default `HOME` for those jobs is `/tmp`. System and
build jobs are not remapped (nested runc / host volumes). Isolation also
includes `no_new_privs`, seccomp, AppArmor `flynn-default`, dropped
capabilities, a private cgroup namespace, and the overlay/host firewall
above. User jobs cannot request host network/PID, writable cgroups,
device profiles (`zfs`/`kvm`/`loop`), extra capabilities, or host bind
mounts (including runtime sockets). One-off jobs (`POST /apps/:id/jobs`,
`jobs:run`) also cannot become system-class: app-scoped callers have
reserved metadata (`flynn-system-app`, `flynn-controller.*`,
`flynn-datastore`, `flynn-plugin`) stripped, `partition=system` forced
to `user`, and non-empty device profiles rejected. System apps and
cluster-admin credentials still run on the system partition with
existing profiles. The controller strips privileged process fields
from user-app releases at create and again before `PUT /apps/:id/release`
attaches a release; a cluster-admin or controller key can still create
privileged releases for system apps. `POST /releases` requires `app_id`
for app-scoped tokens, and minting a release needs `app:env:write` (the
Manage and Admin roles include it). `app:scale:write` alone cannot mint
a release. A release minted for one app cannot be attached to
another. One-off jobs (`POST /apps/:id/jobs`) may still *run* another
app's release when the caller is cluster-admin, or when that release is
the image of a resource attached to this app (`flynn redis:cli` and the
other plugin consoles). Do not run untrusted code in Flynn.
HostNetwork remains restricted to system/builder jobs.

There may be other unknown security flaws in Flynn. For the time being we do not
recommend running Flynn in environments where there is access to sensitive data
or services.

## Reporting Issues

This fork does not operate the historical `security@flynn.io` mailbox. If you
discover a security flaw that is not already described here:

1. Open a **private** [GitHub security
   advisory](https://github.com/randy-girard/flynn/security/advisories/new) if
   you can, or
2. Contact maintainers on [Discord](https://discord.gg/VU2ZqrPUay) without
   posting exploit details in a public channel.

Please include the affected component and enough information to reproduce. We
will acknowledge reports as quickly as we can.

If you believe an [existing
issue](https://github.com/randy-girard/flynn/issues) is security-related, do
not add exploit details in public comments; use an advisory or a private
message instead.

The PGP key previously published for `security@flynn.io` expired in 2018 and
should not be used.

## Disclosure Process

This fork uses coordinated disclosure:

1. A report is confirmed and affected components and versions are identified.
1. A fix is prepared privately when that is practical.
1. A GitHub Release is published with the fix and a description of the issue,
   affected versions, and how to update.
1. Advance notice may be skipped if the issue is already public, is being
   exploited, or is in an upstream dependency with no embargo.

_This policy is based on the [Go security disclosure policy](https://golang.org/security)._

## Security Announcements

Watch [GitHub Releases](https://github.com/randy-girard/flynn/releases) and the
repository. Discussion is on [Discord](https://discord.gg/VU2ZqrPUay).
