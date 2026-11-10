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
`flynn addons:expose` opens a TCP route on the lowest free port in 3000–3500.
The cluster firewall opens that port on every host. Treat those ports as public; prefer TLS
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
  discovery). Before a key is configured the body is the full status, so
  bootstrap can read the host id, URL, and discoverd address without sending
  a secret to a discovery URL. After the key is set, a request that does not
  present it is liveness only: host id, advertised URL, version, flags, and
  `auth`. The discoverd job id and address, and the flannel job id and overlay
  subnet, are returned only when the request presents `FLYNN_HOST_AUTH_KEY`.
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

The cluster status app (`status.$CLUSTER_DOMAIN`) uses a separate
`AUTH_KEY`. Private overlay, loopback, and RFC1918 TCP peers may skip
the key so in-cluster health checks work. `X-Forwarded-For` is trusted
only when that TCP peer is already private (Flynn's router). A public
socket with a forged private `X-Forwarded-For` still requires the key.

`flynn-host init` does **not** generate a unique per-host key. A random key
at init would desynchronize multi-node bootstrap, which installs one shared
secret. If `FLYNN_HOST_AUTH_KEY` is already in the environment, init persists
it into `host.json`. Joining hosts should set that cluster key before the
daemon listens. Bootstrap still prefers `127.0.0.1` when configuring the
local host (faster); remote peers use the advertised address and empty-key
TOFU on `POST /host/auth-key` only.

Access to the controller is available via HTTPS over port 443, and
a randomly generated bearer token is used for authentication. Cluster
administrator access is the install key (`ClusterKey`, on hosts and system
jobs), a user JWT with scope `cluster:admin`, or scope `*`. A JWT with empty scopes and
empty app grants is not an administrator; it has no controller access.
The TLS certificate used for communication is generated during installation
(self-signed). Operators should not pass `--insecure` or skip-verify to talk
to the controller, git, or dashboard:

1. **`flynn cluster:add`** stores a TLS pin in `~/.flynnrc` and writes the
   Flynn CA to `~/.flynn/ca-certs/<cluster>.pem`. The CLI verifies the pin;
   git uses `http.<git-url>.sslCAInfo` on that file. Print the CA with
   `flynn cluster:ca` if you need it in an OS or browser trust store.
2. **Let's Encrypt on system routes** (`flynn-host plugin:install letsencrypt`,
   `letsencrypt:configure`, `letsencrypt:enable-system-routes`) replaces the
   bootstrap cert with a publicly trusted certificate. Then run
   `flynn cluster:refresh --clear` so the CLI uses system CAs instead of the
   pin. Intra-cluster mTLS (discoverd, host APIs, appliances) still uses the
   Flynn CA.

Configure Let's Encrypt after bootstrap with
`flynn-host plugin:install letsencrypt`,
`flynn-host letsencrypt:configure --email=<you> --agree-tos` and
`flynn-host letsencrypt:enable-system-routes` so the dashboard and controller
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
unauthenticated so health checks work. Those responses omit `Discoverd-Peers`
and the raft index unless the request presents `DISCOVERD_AUTH_KEY`.
Authenticated clients still receive the peer list on those paths and on
`GET /raft/peers`. DNS on `:53` is unauthenticated
because user jobs need it. User and build jobs still cannot open
`:1111` (host iptables). Do not bind discoverd only to loopback; hosts
need peer HTTP.

The controller no longer publishes `AUTH_KEY` (the cluster admin key) in
discoverd instance metadata. `GET /services/controller/instances` does
not return that secret. System consumers (router, status, updater,
flynn-host CLI) read `CONTROLLER_KEY` or `AUTH_KEY` from their
environment (including `/etc/flynn/host.json` via the flynn-host CLI)
and only fall back to instance meta during mixed-version rolling
updates. If both are empty, `flynn-host` and the in-cluster updater read
`AUTH_KEY` / `CONTROLLER_KEY` from a running controller job so `volume:gc`,
volume/sirenia repair, and `POST /artifacts` during `flynn-host update` do
not 401. HTTP 401 is not retried as a transient error. When the key is
found on a job, `flynn-host` also writes it to `/etc/flynn/host.json`.

The controller limits authenticated requests per `web` process. JWTs and
personal access tokens are bucketed by user id, not by token signature
and not by client IP. Reminting a dashboard JWT does not reset the
budget. The install key has a separate, higher budget so router sync,
the scheduler, and git push keep working when a user is over theirs. A
`cluster:admin` user JWT stays on the user budget. Build tokens carry
the app owner (`user:<id>` stored as the id, `org:<id>` stored whole) so
a build shares that account. Over the budget the response is HTTP 429
with `Retry-After` and JSON `retry: false`. The Go client retries
`retry: true` for ten seconds, which would multiply the load. gRPC,
including grpc-web, uses the same buckets and returns `ResourceExhausted`.
`GET /ping` is unlimited. Cluster-key `GET /.well-known/status` is
unlimited so a full cluster budget does not flap health. Login, OAuth,
the GitHub webhook, and `GET /ca-cert` are unlimited because they run
before authentication. There is no per-IP limit: external clients arrive
through the router. Hosted and self-hosted clusters use the same limits.

## App catalog

`GET /apps` is scoped to the caller:

* **Cluster key / `cluster:admin`.** Default response omits platform
  and system (`flynn-system-app`) apps and keeps installed plugins
  (`flynn-plugin=true`). Pass `?all=true` (CLI: `flynn apps --all`) for
  the operator catalog, including bootstrap apps. `flynn-host`, plugin
  install, backup, and the updater use that full list. Treat the cluster
  key as superuser: it can still `GET /apps/:id` for system apps by name.
* **User token** (`UserID` set). Returns only apps the user owns or
  collaborates on. `?all=true` does not reveal other tenants' apps or
  platform apps.
* **App-scoped token** (dashboard grant, build token). `GET /apps` is
  `403`. The token may only `GET /apps/:id` for apps it was granted.

Hosted and self-hosted clusters use the same list. `tenancy_mode` does
not change it. Omit `count` and the response is still one JSON array of
every app that caller can see. Pass `count` (at most 1000), `before`, and
`before_id` to opt into a page. Visibility is applied before the limit, so
a page shorter than `count` is the end. The next page uses the last row's
`created_at` and `id`. `?context=` is a hint and is not a filter. The CLI,
dashboard, backup, and updater walk pages. An old client that omits
`count` still receives one array.

`flynn cluster:add` after bootstrap does not store the cluster key. `flynn login`
creates a user session. A cluster-admin user listing `GET /apps` used to print
`gitreceive`, `router`, `blobstore`, `postgres`, and `controller`. That was
superuser listing, not a public catalog.

## Route and active job lists

`GET /routes` and `GET /active-jobs` are the same on a hosted cluster and a
self-hosted one. `tenancy_mode` does not change them.

Omit `count` and the response is one JSON array. Pass `count` (at most
1000), `before`, and `before_id` to opt into a page. A page shorter than
`count` is the end. There is no `next` link.

`GET /routes` for a user token includes only routes of apps that user owns
or collaborates on, and hides catalog-internal apps. That filter runs
before the limit. The cluster key, a nil internal caller, and a
cluster-admin token see every route, including system apps. That is
different from `GET /apps`, which hides bootstrap apps unless `?all=true`.
The router and the host firewall need those routes. Private keys are still
removed unless the caller may export them. The route cursor is
`created_at` plus `type/id` (`http/<uuid>` or `tcp/<uuid>`), newest first.
`GET /apps/:id/routes` stays one array.

`GET /active-jobs` stays cluster-admin only. It does not gain an app-owner
filter. The paged order is `created_at` then job `uuid`, not `updated_at`.
`updated_at` changes every time a job is stored, so a cursor on it would
skip jobs that are still up. An unpaged list stays ordered by
`updated_at`. For every token except the cluster key, `slugrunner` jobs
are omitted before the limit. `slugbuilder` and `dockerbuilder` stay.
A cluster-admin user token still hides `slugrunner`. The cluster key sees
them.

An old client that omits `count` still receives one array. A new client
that sees a first body longer than `count` treats it as the whole list.
A later page that repeats an id or ignores the cursor is an error. A
failed later page is an error and is not a partial list. The router, the
scheduler, and the host firewall depend on that: a partial read must not
delete routes, mark jobs down, or drop firewall exposes.

## Applications

Applications run in their own network namespace on the overlay. User jobs
cannot open connections to other user jobs or to internal Flynn services
(`controller`, `blobstore`, `postgres-api`, …). They may reach provisioned
datastores only at the leader host Flynn put in **that app's**
`DATABASE_URL` / `REDIS_URL` / etc. (not another app's datastore), and only
on the database protocol ports (Postgres
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
add (the router). System jobs still reach other overlay addresses.
Data-plane engines (the database process, not the plugin `web` API) are not
host-trusted: they do not receive `FLYNN_HOST_AUTH_KEY`, and new connections
from their overlay IPs to host SSH (`:22`) and the host API (`:1113`) are
dropped. They still use discoverd (`:1111`) and DNS (`:53`), and they still
reach other datastore peers and system overlay IPs. `/data` stays mounted.
User jobs can still open the documented data-plane ports.

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

Gitreceive and taffy require `ACCESS_TOKEN_SIGNING_KEY` to mint a scoped
`build:artifacts` token and mount it at `/run/secrets/controller_token`.
The token's user id is the app owner, so the controller rate limit for
that build is the owner's. An app with no owner uses `created_by`.
Taffy runs `flynn-receiver` for URL deploys, so a taffy release without
that key fails the build the same way a git push would. Neither app puts
`CONTROLLER_KEY` in slugbuilder or dockerbuilder environment (that key
would otherwise remain in the container config and `/proc` environ). A
cluster without a signing key fails the build with a clear operator error
instead of injecting the cluster-admin key.
`build.sh` still relocates a leftover `CONTROLLER_KEY` to
`/run/secrets/controller_key` and unsets the env if one is present.

`flynn-host update` copies missing cluster secrets onto system-app
releases (blobstore `AUTH_KEY` / `ACCESS_TOKEN_KEY`, taffy
`ACCESS_TOKEN_KEY` / `ACCESS_TOKEN_SIGNING_KEY`, postgres
`CONTROLLER_KEY`, `DISCOVERD_AUTH_KEY` on jobs that talk to discoverd,
and the same controller key onto router and acme).
`flynn-host plugin:update` / `plugin:install` do the same for plugin
apps. Catalog plugins print that they receive cluster secrets and
continue. Third-party plugins prompt on a TTY (cluster-admin equivalent);
pass `--yes` for non-interactive installs. You do not need to `env:set`
those keys by hand after an upgrade.
Rotating a key is still a separate operator step; see
[Production — Controller Keys](production.html.md#controller-keys).

New isolated Redis, MySQL, MongoDB, Kafka, and ClickHouse instances do
not receive `CONTROLLER_KEY`, `AUTH_KEY`, or access-token signing keys.
Each new database gets its own `APPLIANCE_KEY` for appliance admin HTTP
(backup, restore, status, scale). The plugin API job still holds the
cluster controller key and reads `APPLIANCE_KEY` from the instance
release when it calls that admin API. Replicas of a new database share
that appliance key. An instance created before this change still has
`CONTROLLER_KEY` in its release. Adding a follower to that database does
not copy the cluster key onto the new process, so peer admin calls
between them fail until an operator rotates the set onto `APPLIANCE_KEY`.
Plugin API jobs are unchanged and still receive the cluster key through
`inject_env`. Postgres isolated instances already omitted the controller
key; they stay that way.

User and build jobs cannot open the host control plane: SSH (`:22`),
host HTTP (`:80`/`:443`), the host API, or discoverd (`:1111`).
DNS to the overlay gateway (`:53`) is allowed. flynn-host registers user
HTTP backends with discoverd; the job never receives a `DISCOVERD` URL.
`DISCOVERD_AUTH_KEY` is not written into user or build `/.containerconfig`
or the workload environment, including when a release env contains it.
System and datastore jobs still receive the key so they can register
with discoverd.

`flynn-host pg:psql` opens the platform database and requires the cluster
controller key on the host. Dashboard tokens scoped to user apps cannot open
that console. Tenant consoles are `flynn pg psql` from the postgres plugin
(a one-off on the **user** app that speaks the native protocol). Flynn CLI
cannot `flynn run` / attach a shell on a database app (postgres, mysql,
mongodb, redis, kafka, clickhouse — plugin apps, isolated instances, and
platform postgres). That one-off path is flynn-host only
(`X-Flynn-Host-Auth`). Treat the host cluster key as root; it is not a laptop login.

`git push` to gitreceive requires the cluster controller key, `cluster:admin`,
or `app:deploy` on that app (or a coarser grant that expands to it, such as
`app:write` / `app:admin`). A valid token for one app cannot push to another
app by name. View-only (`app:read`) tokens cannot push. GitHub webhooks at
`POST /github/webhook` are authenticated with the webhook HMAC, not git
credentials.

One-off jobs (`flynn run`, `POST /apps/:id/jobs`) cannot set process type
`slugbuilder` or `dockerbuilder`. Host classification treats those types as
build jobs (no user namespace, extra capabilities for nested BuildKit). That
exception is only for gitreceive compile jobs; app-scoped `app:jobs:run`
callers get HTTP 400. Cluster-admin and system apps may still set those
types.

Dashboard JWTs hide builder process types (`slugbuilder`, `dockerbuilder`,
`slugrunner`) from jobs, logs, formations, and release process maps. Setting
those process limits requires the cluster controller key (`flynn limit:set` or
flynn-host). Path-based
HTTP routes (`example.com/api`) can only be created with
`flynn-host route:add`. App-scoped tokens with `app:routes:write` can only
point a route at a discoverd service this app owns (`$APP-web` /
`$APP-$TYPE`, or a `service` declared on the current release). Routing to
system services (`controller`, `blobstore`, `postgres-api`, dashboard, www,
discovery) requires the cluster controller key. `GET` route and route list
responses include the public certificate and omit `certificate.key` and
`tls_key` unless the caller is the cluster key or has `cluster:admin`.
The same omission applies to route events. `POST` and `PUT` still accept
a private key from `app:routes:write`. An update that repeats the stored
certificate and leaves the key empty keeps the stored key, so reading a
route and writing it back does not remove TLS. The router keeps using
the stored key to terminate HTTPS.

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
a release. `GET /apps/:id/release` and release lists stay available to any
grant on the app, but `env` and per-process `env` are omitted unless the
caller has `app:env:read` (View, Deploy, Manage, and Admin include it;
`app:logs:read` and `app:scale:read` alone do not). The same omission
applies to expanded formations and release events. The cluster key and
cluster-admin tokens still receive env. `GET /apps/:id/log` stays
available with `app:logs:read`, as do `GET /apps/:id/jobs/:id/output` and
`GET /apps/:id/releases/:id/output`. `PUT /apps/:id/releases/:id/output`
requires `app:write`. The controller redacts current
release env values and per-process env values that appear in those log
lines, including the stored copies, when the caller does not have `app:env:read`. Values shorter than
8 characters are left as-is. Callers with `app:env:read` (including
View, Deploy, Manage, and Admin), cluster-admin tokens, and the cluster
key receive the log text unchanged. A release minted for one app cannot be attached to
another. One-off jobs (`POST /apps/:id/jobs`) may still *run* another
app's release when the caller is cluster-admin, or when that release is
the image of a resource attached to this app (`flynn redis:cli` and the
other plugin consoles). Do not run untrusted code in Flynn.
HostNetwork remains restricted to system/builder jobs.

There may be other unknown security flaws in Flynn. For the time being we do not
recommend running Flynn in environments where there is access to sensitive data
or services.

## Controller audit log

The controller records privileged actions in its `core_audit` table: who
(`cluster-key`, `user:<id>`, or the token's user), what (for example
`app.create`, `app.delete`, `release.create`, `app.release.set`,
`deployment.create`, `formation.set`, `scale.request`, `job.run`,
`job.kill`, `route.create`, `resource.create`, `token.create`,
`cluster.runtime_settings.set`, `policy.set`, `collaborator.add`), the
app it touched, and when. Only successful requests are recorded, from
both the HTTP API and the gRPC API.
Request bodies are not stored, so env values and keys never land in the
log; entries keep the method, path, and status.

Cluster-key or cluster-admin credentials can read it with
`GET /cluster/audit?after=<id>&limit=<n>` (oldest first, at most 1000
rows) and prune it with `DELETE /cluster/audit?before=<RFC3339>&max_id=<id>`,
which is itself recorded as `audit.prune`. The controller never prunes on
its own. The enterprise plugin copies these entries into its audit log and
applies its retention setting to both copies; see
[Plugins](plugins.md).

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
