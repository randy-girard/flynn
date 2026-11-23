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
`flynn addons:expose` opens a TCP route on the lowest free port in 10000–29999.
The cluster firewall opens that one port on every host. It does not open the
rest of the window. Treat the allocated port as public; prefer TLS
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
* `POST /host/auth-key` on a host with no key is accepted from **this host**
  (loopback, a Unix socket, or a local interface IP), or from a peer that
  sends the host's one-time join token (`join_token` in the body; set with
  `flynn-host init --join-token` or `FLYNN_HOST_JOIN_TOKEN`). Multi-node
  bootstrap runs `configure-host-auth` on one coordinator and must reach
  every host's `:1113`, so every host gets the same join token at install.
  A host with no key and no join token answers a remote claim with `403`;
  a wrong token is `401`. The host deletes the token from `host.json` and
  from its environment once a key is set, so it cannot be replayed. The
  claim may write only `FLYNN_HOST_AUTH_KEY`, `DISCOVERD_AUTH_KEY`,
  `AUTH_KEY`, `CONTROLLER_KEY`, and `DISCOVERD` into `host.json`; any other
  env key is `400`, so a caller cannot inject `LD_PRELOAD` or `PATH` into the
  root daemon. Claims are serialized, so two racing claims cannot both win.
* Every other method is `401` unless the client is on **this host**: TCP
  loopback (`127.0.0.0/8` or `::1`), a **Unix domain socket**, or a **local
  interface IP**. `flynn-host` often listens on `--listen-ip` rather than
  `127.0.0.1`; discoverd, flannel, and the image builder notify that address
  from the same machine. Job, volume, and update APIs stay closed to other
  hosts on the subnet.

After a key is set, loopback is not a bypass; the request must present
`authKey`. Forwarded headers (`X-Forwarded-For`, `X-Real-IP`) are ignored.
The per-IP limit (100 requests a minute) is always on. Requests that
present the key, and requests from this host while it has no key, are not
counted. A host that got its key in-process (not under systemd) is limited
the same way as one that started with it.

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
local host (faster); remote peers use the advertised address and the join
token. Bootstrap configures remote hosts first and its own host last, so a
run that fails on a remote host keeps the coordinator's token for a retry.
See [Manual installation — Join token](installation/manual.md#join-token-multi-node).

Access to the controller is available via HTTPS over port 443, and
a randomly generated bearer token is used for authentication. Cluster
administrator access is the install key (`ClusterKey`, on hosts and system
jobs), a user JWT with scope `cluster:admin`, or scope `*`. A JWT with empty scopes and
empty app grants is not an administrator; it has no controller access.
For a token that names a controller user (a `flynn login` token, a dashboard
session token, or a personal access token), admin comes only from that
user's `cluster_admin` flag. The controller drops `cluster:admin` and `*` from
the token and adds `cluster:admin` back when the user is an admin, so a
demoted admin loses admin at once, before the login token expires.
The gRPC API (`StreamApps` and the other controller RPCs, including
grpc-web) checks the user row the same way on every call: a demoted admin
gets `PermissionDenied`, a disabled or suspended user or account gets
`PermissionDenied` ("this account is suspended"), and a login token signed
before a password change gets `Unauthenticated`. A stream is checked when it
opens; one already open keeps running until it ends. gRPC does not accept
personal access tokens.

Personal access tokens (`POST /tokens`, `flynn token:create`) accept only the
scope `user` (or none). `cluster:admin`, `*`, `account:quota`, `acme:issue`,
`build:artifacts`, `gitreceive:build`, and any other scope get `400`. A token
acts as its user: an admin's token is admin while the user is an admin, and a
tenant's token reaches the tenant's apps. On authentication a stored row keeps
only allowlisted scopes, and controller migration 92 clears every other scope
from existing rows.

Every personal access token expires. A token created with no `expires_at`
(including `flynn-host user:token`) expires after 90 days, and `POST /tokens`
refuses an `expires_at` more than 365 days away, or one in the past, with
`400`. Set `PAT_DEFAULT_LIFETIME` and `PAT_MAX_LIFETIME` on the controller app
to change them (`flynn -a controller env:set PAT_MAX_LIFETIME=30d`; days such
as `30d` or a Go duration such as `720h`). A default longer than the maximum is
cut to the maximum. `0` or `none` turns a limit off on a `self_hosted` cluster
only: a hosted cluster with no maximum still uses 365 days. A value that does
not parse is logged and the built-in value is used. Controller migration 98
gives every live token that had no expiry, or one more than 365 days out, an
expiry 365 days after the upgrade. A token is still revoked at once by
`flynn token:revoke`, a password change, or disabling the user.

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
(or HTTP basic password). `POST /heartbeats` uses that same key. A missing
key is 401, and a discoverd proxy checks the key before it redirects.
`/ping` and `/.well-known/status` stay
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
`build:<owner>` (`user:<id>` as `build:<id>`, `org:<id>` as `build:org:<id>`)
so all builds for one owner share one budget that is separate from the
owner's own. Over the budget the response is HTTP 429
with `Retry-After` and JSON `retry: false`. The Go client retries
`retry: true` for ten seconds, which would multiply the load. gRPC,
including grpc-web, uses the same buckets and returns `ResourceExhausted`.
`GET /ping` is unlimited. Cluster-key `GET /.well-known/status` is
unlimited so a full cluster budget does not flap health. Login, OAuth,
the GitHub webhook, and `GET /ca-cert` are unlimited because they run
before authentication. There is no per-IP limit: external clients arrive
through the router. Hosted and self-hosted clusters use the same limits.

## Browser login (OAuth authorize)

`GET /oauth/authorize` on `auth.<domain>` (used by `flynn login --oauth`)
only serves the registered client `flynn-cli`. `redirect_uri` must be the
out-of-band URN `urn:ietf:wg:oauth:2.0:oob` or plain `http` on a loopback
address (`127.0.0.1`, `[::1]`, `localhost`, any port and path) with no
userinfo or fragment. Anything else, including `https://` hosts and
look-alikes such as `http://127.0.0.1.example.com/`, is HTTP 400 with no
redirect and no code. A login session cookie (`flynn_oauth_session`,
SameSite=Lax, 12 hours) no longer issues a code by itself: the page shows
the signed-in email and the redirect target, and the code is issued only
after the user presses Continue. That POST is refused (HTTP 403) when the
browser marks it cross-site or same-site (`Sec-Fetch-Site`) or when
`Origin` is another host, so an app on another subdomain of the cluster
domain cannot submit it. The dashboard's own `/oauth/authorize` applies
the same redirect rule and the same consent step: a dashboard session shows
the signed-in email and redirect target, and the code is issued only by a
POST of that page from the dashboard host (a missing `Origin` and `Referer`
is refused too).

`POST /oauth/token` with `grant_type=authorization_code`, on the controller
and on the dashboard, checks the code against the `redirect_uri`, the PKCE
`code_verifier` and the `client_id` it was issued for. A different
`client_id` is HTTP 400 `invalid_grant`, and the code cannot be used again.
A request with no `client_id` is still accepted so older clients keep
working; `flynn login` sends `client_id=flynn-cli` at both steps.

## Sign-in attempts and password changes

Password sign-in (`POST /oauth/token` with `grant_type=password`, which
`flynn login` and the dashboard use, and the form on `POST
/oauth/authorize`) is throttled in each controller `web` process. The
budgets are per process, so `web=2` doubles them. Only
failed attempts count; a correct password gives its attempt back. Three
budgets apply, each refilling at a steady rate, so a client that keeps
failing is slowed down but never locked out for good:

| Budget | Burst | Refill |
| --- | --- | --- |
| One account from one address | 5 failures | 1 per minute |
| One account from all addresses | 20 failures | 1 per 2 minutes |
| One address, all accounts | 30 failures | 1 per 20 seconds |

An address that signed in to the account in the last 24 hours skips the
account-wide budget, so failures from other addresses cannot keep the
owner out from where they usually sign in. Unknown emails are counted the
same way as real ones. A request over a budget is HTTP 429 with
`Retry-After`; the token endpoint answers `{"error":"too_many_attempts"}`
and the authorize form shows the wait. The address is the last
`X-Forwarded-For` entry (the one the router appends) or the TCP peer. The
dashboard signs browser users in with the cluster key and forwards the
browser's address, so users behind the dashboard do not share one
address budget; a cluster-key caller that forwards no address is counted
per account only. Users behind one NAT share the address budget; raise it
with `flynn -a controller env:set CONTROLLER_LOGIN_IP_BURST=200`. The cluster
key, personal access tokens and service tokens never go through password
sign-in and are not affected. A caller inside the cluster that reaches the
controller directly can choose its `X-Forwarded-For`, so only the account
budgets hold against it.

Changing a user's password with `PATCH /users/:id` (the dashboard,
`flynn-host user:bootstrap-admin`) signs that user out everywhere: their
refresh tokens, authorization codes and `/oauth/authorize` sessions are
deleted, their personal access tokens are revoked, and login tokens signed
more than a second before the change are refused with HTTP 401 (the second
covers clock skew with the dashboard, which signs its own login tokens). This includes the session of
the person making the change; the CLI has to `flynn login` again. The
dashboard keeps its own session and mints new tokens, so a user who
changes their password there stays signed in to the dashboard. Setting the
password a user already has (the dashboard re-sends it when it syncs a
user) is not a change and revokes nothing. The `user.update` audit row
records `password_changed` and `credentials_revoked`.

Authorization codes and refresh tokens are single use: the controller
deletes the row as it reads it, so two concurrent requests with one code or
one refresh token cannot both succeed. If two `flynn` processes refresh at
the same moment, the one that loses picks up the tokens the other wrote to
`~/.flynn`. Expired codes, refresh tokens and sessions are deleted every
hour.

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

Grants are keyed by app id, never by name. A request that names the app in
the URL is resolved to its id before the grant check, and a lookup that
matches both an id and a name returns the id match. App names that look like
an app id are rejected, so an app cannot take another app's grants by
being named after its UUID.

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
or collaborates on, and hides catalog-internal apps. The `acme:issue`
service token drops the owner filter and keeps the catalog filter. That filter runs
before the limit. The unpaged list reads the parent apps of all routes in
one query, not one per route. The cluster key, a nil internal caller, and a
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

## Hosted custom domains

In `hosted` mode a route hostname outside the cluster domain needs a
verified domain for the app's owner account, either the exact name or a
parent. `self_hosted` does not check this. The rule covers the route
`domain` and a `managed_certificate_domain` that differs from it, on create
and on `PUT /apps/:id/routes/:type/:id` whenever the update changes either
value. An update keeps the route on the app in the URL: `parent_ref` in the
body is ignored. The flow is two API calls
(`flynn domain:claim` and `flynn domain:check` in the CLI):

1. `POST /domains/verify {"hostname":"example.com","owner_account":"user:<id>"}`
   returns a token and `txt_name` (`_flynn-verify.example.com`). The caller
   must be an admin of that account.
2. Publish the token as a TXT record at `txt_name`, then
   `POST /domains/verify/example.com/check`. The controller looks the record
   up itself, with a 5 second timeout, and marks the domain verified only
   when one TXT value equals the token.

A claim holds its hostname for 7 days from when that account first claimed
it. During that time a claim for the same name by another account (the
cluster key included) is `409` and the stored owner and token do not change.
After 7 days another account may replace a claim that is still unverified. A
verified claim is never replaced; it stays with its account until it is
deleted. The owner may claim again at any time to get a new token; that
clears verification and does not restart the 7 days. Before controller
migration 99 any account could overwrite an unverified claim and its token,
so it could keep the real owner from verifying. Migration 99 adds
`claimed_at`, set to the upgrade time for existing claims.

Keep the TXT record published after the check. The controller stores when
it last found the record (`checked_at`) and trusts a verification for 24
hours after that. A route create or hostname update that needs a verified
name whose last check is older looks the record up again: if the token is
still there `checked_at` moves and the route is allowed; if the lookup
answers without it (no record, NXDOMAIN, another value) the claim is
unverified, a `domain.unverify` audit row is written, and the route is
refused until the record is published and `flynn domain:check` runs again.
A lookup that fails (timeout, server error) refuses the route and leaves the
claim verified. A claim by another account on a verified name whose last
check is older than 24 hours looks the record up the same way first, so a
domain that expired or changed hands does not stay with the old account;
once it is unverified the 7-day hold above applies. Routes already added
under a name stay when it is unverified. Before controller migration 100 a
verification never lapsed; migration 100 adds `checked_at` and sets it to
`verified_at` for verified rows.

`GET /domains?owner_account=<account>` lists that account's claims
(hostname, owner, token, `txt_name`, `verified_at`, `claimed_at`,
`checked_at`) for an
admin of the account (`flynn domain:list`). Without `owner_account` only the
cluster key or a cluster admin may list, and gets every claim
(`flynn domain:list --all`). `DELETE /domains/<hostname>` removes a claim,
verified or not, for an admin of the owning account or the cluster key
(`flynn domain:remove`). Routes already added under a deleted name stay; new
routes under it need the name claimed and verified again. Claims and deletes
are written to the audit log (`domain.claim`, `domain.delete`, and
`domain.unverify` when a re-check no longer finds the record).

The claim is refused when the name is a single label, an IP address, a
wildcard, not a valid DNS name, a special-use name (`localhost`, `.local`,
`.internal`, `.test`, `.example`, `.invalid`, `.onion`, `.alt`, `.arpa`), or
itself a public suffix on the [Public Suffix List](https://publicsuffix.org/)
(`com`, `co.uk`, and private suffixes such as `github.io`). It is also
refused when the name is the cluster domain (`DEFAULT_ROUTE_DOMAIN` or the
`app_domain` override), a name under it, or a parent of it. `check` applies
the same rule again.

A `records` array in the `check` body is accepted from the cluster key only,
for operators and tests. Any other caller that sends `records` gets a
validation error and the domain stays unverified. Before controller
migration 93 the controller trusted those records from any caller, so a
tenant could "verify" `com` and route every `*.com` name not already taken.
Migration 93 clears `verified_at` on every stored domain, keeping the claim
and token, so each owner must publish the TXT record and run `check` again
before adding new routes under it. Existing routes are not removed. Route
checks also ignore any stored verified row that fails the claim rule above.

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

Discoverd DNS answers a user job only for `leader.<service>.discoverd` names
of datastores attached to its app. The list comes from the controller's
resource records (`addons:create`, `addons:attach`), which only a
provider or the cluster key can write, and the controller and scheduler
stamp it on each job as the reserved `flynn-controller.discoverd_allow`
metadata. Release env, `flynn env:set` and `flynn run -e` values are not
read, so setting `X=leader.redis-other.discoverd` does not let a job resolve
another app's datastore. Formation jobs pick up a newly attached or
detached resource on the next deploy (attaching one sets env, which
deploys); one-off jobs read the current list. Jobs started by a controller from
before this rule (still running after an update) keep the list read from
their env until they are next deployed.

Discoverd DNS does not treat an unknown overlay address as a system job. A
query from an overlay IP that is in none of the `flynn-net-*` sets below gets
the answer for a user job with no attached datastores (`NXDOMAIN` for every
`*.discoverd` name), with two exceptions. If the IP belongs to the host that
runs this discoverd, discoverd asks that flynn-host which job has the IP
(`GET /host/netpolicy/ips/:ip`, host-key authenticated) and answers as it would
once the job's registration arrives: a user job gets its own allowlist, and a
system, datastore or build job gets full answers. A job registers before it
starts, but the local discoverd can apply the registration a few milliseconds
after the job's first query, and a failed registration is retried only every
15 seconds; the host lookup covers both. The host's own bridge address, which
host-network jobs query from, and any address outside the overlay are system
clients. If flynn-host does not answer within a second, the query fails
closed.

The controller refuses to delete an app while another app still points at
it (a consumer's `REDIS_URL`, a follower's `FLYNN_REDIS_LEADER`). Release env
is tenant input, so only an app of the same owner account, or one attached
to a resource whose env names the datastore, can hold back that delete.
Another tenant setting `X=redis-harbor-48291` in its own env does not block
your `addons:destroy` or `flynn delete`. The check reads every app's owner,
current release env and attachments in one database query.

These rules match ipsets that hold the overlay IPs of each job class
(`flynn-net-user`, `flynn-net-build`, `flynn-net-data`, `flynn-net-sys`), the
cluster node IPs (`flynn-net-nodes`), and two per-service sets that flynn-host
fills from discoverd: `flynn-net-build-svc` (the controller, blobstore and
tarreceive instances) and `flynn-net-logagg` (the logaggregator instances). flynn-host adds a job's IP when it
starts the job and learns other hosts' jobs from discoverd. A finished job's IP
is not given to another job until its veth is off the bridge and two minutes
have passed, so traffic still addressed to the old job is not matched to a
new job's set (see [architecture](architecture.html.md#flannel)). A user or build
job does not start when its IP cannot be added to its set, because a job
outside the set would match no isolation rule. A failed discoverd
registration is retried every 15 seconds. The 15-second resync rebuilds each
set without emptying it: `ipset restore` fills a temporary set and swaps it
with the live set in one kernel operation, so a packet sees either the old or
the new contents and isolation never switches off during a resync.

User and build jobs cannot open connections to `169.254.0.0/16`, the
link-local range that holds the cloud instance metadata service and the
node's instance credentials. Operators can deny more ranges with
`FLYNN_JOB_EGRESS_DENY`; see
[Production — Job egress deny list](production.html.md#job-egress-deny-list).

Submodules of a `git push` are fetched by the gitreceive pre-receive hook,
which runs as root in the system gitreceive job, not in the build job. The
URL comes from the pushed `.gitmodules`, so the hook treats it as tenant
input: only `https` to hosts whose every address is public (git is pinned to
the checked addresses and does not follow redirects), and `ssh` only for apps
in `SSH_CLIENT_KEY_APPS`. Local paths, `file://`, `*.discoverd`, other
schemes, option-looking URLs and private, overlay and loopback addresses are
refused before anything is contacted. Each fetch is depth 1, time limited and
file size limited. gitreceive writes the cluster SSH client key only when
`SSH_CLIENT_KEY_APPS` is set, to `~/.ssh/cluster_client_key` rather than the
default identity, and passes it to ssh only for an opted-in app's submodule
(with `IdentitiesOnly`, `BatchMode` and strict host key checking against
`SSH_CLIENT_HOSTS`). See [Apps — Git submodules](apps.md#git-submodules).

Build jobs (slugbuilder / dockerbuilder) are a separate overlay class. They
run tenant code (`bin/compile`, custom buildpacks, Dockerfile `RUN`), so on
the overlay they may open only the services a build needs: the controller,
blobstore and tarreceive (`flynn-net-build-svc`). Every other overlay address
is dropped for new connections from a build job: logaggregator, the system
Postgres, every tenant datastore (data and admin ports), other system jobs,
user jobs, and overlay IPs not yet in any set. Build jobs also cannot reach the
host control plane or the node underlay (which holds the router API). The
internet stays open for buildpack downloads. A set rebuilds on every
instance change of its services and every 15 seconds; a failed discoverd
lookup keeps the previous contents. `flynn-host overlay:set` moves the build
drop to the new overlay with the user drop.

The logaggregator API (`/log/:app`, `/snapshot`, `/cursors`) serves every
app's buffered logs, and the router API (`/events`, `/metrics`, `/debug`)
lists every route and backend address. Both require the cluster key
(`CONTROLLER_KEY` or an `AUTH_KEY` entry, as HTTP Basic password or Bearer);
only `/.well-known/status` is public, and a release with no key refuses every
other request. The controller, the scheduler and logaggregator itself send the
key from their env; flynn-host sends the key of a controller or logaggregator
job on the host, or one it looks up from the cluster. The logaggregator syslog
port has no authentication, so it is reachable only from hosts and platform
jobs (gitreceive ships push progress there): user and build jobs are covered
by their overlay drops, and datastore engines are dropped by
`flynn-net-logagg`. A malformed frame on that port (a zero, negative or
oversized MSG-LEN) closes only the sending connection, and a truncated
RFC5424 header is a parse error, not a crash. Blobstore HTTP
requires the cluster `AUTH_KEY` / `CONTROLLER_KEY` (Basic or Bearer) for
object GET/PUT/DELETE. `/.well-known/status` and `HEAD /` stay
unauthenticated so bootstrap and health checks work. Per-app
build cache URLs carry query tokens that the blobstore verifies server-side.
Each token is an HMAC-SHA256 over `appID|object|operation|exp`, keyed by a
subkey derived from the cluster key with HKDF, and it stops working at `exp`
(two hours after the build starts). `BUILD_CACHE_URL` can only read
(`GET`/`HEAD`) and `BUILD_CACHE_PUT_URL` can only write (`PUT`); each is valid
only for that app's `-cache.tgz` or `-docker-cache.tgz` object, as named in the
token. The slugbuilder script does the cache download and upload as root and
unsets both URLs before any buildpack runs, so buildpack code never sees them. The
app's `.slugignore` is applied by the unprivileged build user, never as root,
and only to paths inside the build directory (`..` patterns and symlink escapes
are skipped). A scoped
`build:artifacts` token (the same one create-artifact uses for the
controller) may GET/HEAD/PUT only its own app's objects:
`/slugs/apps/<app-id>/layers/`, `/slugs/apps/<app-id>/images/`, and
`/tarreceive/apps/<app-id>/images/`. Another app's prefix and the older
shared `/slugs/layers/`, `/slugs/images/`, and `/tarreceive/` directories
are cluster-key only, so a compile for one app cannot read or replace
another app's slug. The scheduler sets `FLYNN_APP_ID` on the compile so the
builders know which prefix to use. Layer and image objects are named by the
hash of their content, so a scoped token cannot PUT over one that already
exists: the blobstore answers `409` without writing, and the builders treat
that as already uploaded. tarreceive likewise keeps a layer whose config is
already stored instead of rebuilding it. When a build token registers an
artifact (`POST /artifacts`), the controller checks every URL a host would
fetch for it (the manifest URI, the layer URL template, each layer's URL,
and any `layer_url.*` override). Each must be a plain
`http://blobstore.discoverd` URL in the app's own prefix or the shared
tarreceive layer store, because hosts fetch layers with the cluster key.
The cluster key is not limited. Neither a cache
token nor a build token may send `Blobstore-Copy-From` (the copy source is not
authorized separately, so it would read any object); such a PUT is `403`. Only
the cluster key or a cluster-admin token may copy.

The controller mints a scoped `build:artifacts` token when a compile is
about to start, and the scheduler mounts it at `/run/secrets/controller_token`.
The token is not stored on the job row and is not minted when the pending
row is created, so time spent waiting for a host does not consume its
lifetime. The lifetime is the build timeout, at most 30 minutes. The
token's user id is `build:<owner>`, a rate-limit identity only, so builds
for one owner share one controller budget. An app with no owner uses
`created_by`. Neither compile credential (`build:artifacts`,
`gitreceive:build`) is the owner: the controller does not expand it into
the owner's other apps or `cluster:admin`, and it cannot call user-session
routes such as `POST /tokens` (personal access tokens), `GET /whoami`, or
collaborator and transfer routes. It keeps the one `app:write` grant it was
minted with. Gitreceive
and taffy do not put `CONTROLLER_KEY` in slugbuilder or dockerbuilder
environment (that key would otherwise remain in the container config and
`/proc` environ). A controller without `ACCESS_TOKEN_SIGNING_KEY` rejects
the build before a job is inserted.
`build.sh` still relocates a leftover `CONTROLLER_KEY` to
`/run/secrets/controller_key` and unsets the env if one is present.

The dashboard reads controller events with scope `events:read`, not the
cluster key, when it has `ACCESS_TOKEN_PRIVATE_KEY`. The token grants
`events:read` on one app. It is not `cluster:admin`, it does not set the
cluster-key flag, and it does not expand to `app:env:read`, so release,
`app_release`, `resource`, and `resource_deletion` events for that app arrive
with release, per-process, and resource env stripped. `GET /events` and
`GET /events/:id` without a granted `app_id` are denied. A token whose
user id is an account must match the app owner; a watcher id
`events:<app id>` is bound only by the grant. The dashboard opens at most
one stream per subscribed app and does not open an unfiltered stream.
When the signing key is unset, the dashboard still uses the cluster key
so local Compose keeps working. A controller that has not learned
`events:read` still accepts the cluster key on those routes. Rotate the
dashboard signing key on the dashboard release; do not paste
`CONTROLLER_KEY` into a tenant app to refresh this token. The dashboard
process still holds `CONTROLLER_KEY` for metrics, user admin, and plugin
UI. That key is not the events credential.

`gitreceive` still starts with `CONTROLLER_KEY` and looks up the app with
that client before it checks the push. The hook then receives
`FLYNN_RECEIVE_TOKEN`, a `gitreceive:build` token whose grant is
`app:write` plus `events:read` on that app only (the receiver waits on that
app's deployment and job events) and which may also read `GET /cluster/policy`
for the buildpack allowlist. The controller does not widen it to the owner's
other apps. `flynn-receiver` uses it for controller,
pipeline, and GitHub calls. If the token is absent (no signing key, or a
hook from the previous release), those calls still use `CONTROLLER_KEY`.
The token is removed from the new release env and from the build job env.
The build job keeps its separate `build:artifacts` secret mount.
`CONTROLLER_KEY` stays in the gitreceive and router releases so route sync
and the cache HMAC keep working. It is not copied into a tenant release.
A token for one app cannot read another app's env, delete that app, or
change cluster settings. `POST /apps/:id/builds` accepts the cluster key
or this token for that app, and rejects a `cluster:admin` JWT. Rotate it
by restarting the gitreceive release after `ACCESS_TOKEN_SIGNING_KEY`
changes. Do not paste `CONTROLLER_KEY` into the app being built.

A tenant's `flynn login` token names the user and carries no app grants;
the controller works them out on each request from the apps the user's
account can reach. gitreceive cannot do that itself, so for a user token
that is not `cluster:admin` it sends the caller's own `Authorization`
header (nothing else) to `GET /whoami/grants` and checks the push or fetch
against the returned grants. Those grants are keyed by app id only, and the
check matches the app's id, so an app named after another app's id does not
pick up that grant. A tenant can push (`app:deploy`) and clone (`app:read`)
its own apps and collaborator apps, and gets `403` on another tenant's app.
A suspended account gets `403`. A personal access token is resolved the same
way, so `git push` works with `flynn cluster:add --token`. The cluster key,
a `cluster:admin` token, and app-scoped tokens (dashboard grants, the
receiver's `gitreceive:build` token) are decided from the token alone. If the
controller cannot be reached, a user token keeps only the grants it carries,
which for a tenant is none. `GET /whoami/grants` is a user-session route:
compile and service tokens are denied.

`GET /resources` is open to user sessions. A non-admin user sees only the
resources whose owner account it administers (its own account, an
organization it owns or admins, or an account that made it an `admin`
collaborator); unowned platform resources, suspended accounts and other
tenants' resources are left out. `GET /providers/:id/resources` applies the
same filter. App-scoped and compile tokens are denied and use
`GET /apps/:id/resources`. A cluster admin still sees every resource.
The controller works out the administered accounts once per request and
filters by owner in SQL, so a tenant list runs a fixed number of queries
however many resources and accounts the cluster holds.

`GET /plugin-catalog` is open to user sessions. It is the plugin commands
and add-on types the flynn CLI offers (`flynn help`, `addons:types`,
`addons:create`, `addons:attach`, `flynn redis` and the other plugin
commands). The body is the CLI and add-on specs from each installed
plugin's manifest and the plugin app names, with no app ids, meta, env, or
provider URLs. A tenant's `GET /apps` lists only its own apps, never plugin
apps, so the CLI cannot build this list itself. In `hosted` mode a
non-operator does not see a provider without `tenant_safe` (or its add-on
type and plugin command), the same providers `addons:create` refuses.
App-scoped and compile tokens are denied.

Billing quota writes and suspension use scope `account:quota` in
`QUOTA_TOKEN`. The token lasts up to 180 days. It is not `cluster:admin`.
It may list tenant apps (`GET /apps`) and list each app's formations
(`GET /apps/:id/formations`) so metering still works. It may not read a
single formation (`?expand=true` embeds the release), read release env, or
change a formation. Its `UserId` is only a
caller label, so it is never treated as a user session: `POST /users`,
`POST /apps`, `/tokens`, `/whoami`, collaborators, transfer, and resource
provision or delete are denied for it (and for `acme:issue`). Install mints it from the
controller signing key and then deletes `CONTROLLER_KEY`, `AUTH_KEY`, and
the signing keys from the billing release. The billing API still accepts
the cluster key from `flynn-host` because the release stores
`BILLING_AUTH_HASH` (sha256 of that key), not the key. A cluster without
the billing plugin boots and accepts git pushes. Rotate with
`flynn-host token:rotate billing-plugin QUOTA_TOKEN account:quota`.
`plugin:update` re-mints `QUOTA_TOKEN` (and `ACME_TOKEN`) when the stored
token is expired, unparseable, carries a different scope, or expires within
30 days. A healthy token is kept. It also recomputes `BILLING_AUTH_HASH` from
the current cluster key on every update. `flynn-host production:check` warns
(`service-token`) before a token reaches that window.
Until that release is deployed, a billing process that still has
`CONTROLLER_KEY` is accepted.

Router route sync stays on the cluster key in the router release. Certificate
issuance runs in the letsencrypt plugin. Scope `acme:issue` (`ACME_TOKEN`)
may list and update managed certificates, read `GET /acme/config`, list apps
and routes, and create or change routes. `GET /routes` (paged or not) gives
it the routes of every app its `GET /apps` returns, whatever the owner, and
hides bootstrap system apps. A system app's routes stay closed to it by id
too. It may not read release env, change a formation, or `PUT /acme/config`. Install strips `CONTROLLER_KEY` and the
signing keys from that release. `ACME_TOKEN` wins over `CONTROLLER_KEY` when
both are set. An empty token keeps the cluster key so HTTP-01 renewal still
works during a rolling update. There is no DNS-01 issuer in this tree.
Rotate with `flynn-host token:rotate letsencrypt-plugin ACME_TOKEN acme:issue`.
`flynn letsencrypt:list`, `:enable`, `:disable`, and `:status` run in the
CLI with the caller's own token, so the controller checks the caller's
route permissions. They never borrow `ACME_TOKEN`. The plugin's own
`letsencrypt-cli` needs the plugin credential and is an operator tool on
the plugin app; its `FLYNN_APP_ID` app filter is a convenience, not a
tenant boundary.

`flynn-host update` copies missing cluster secrets onto system-app
releases (blobstore `AUTH_KEY` / `ACCESS_TOKEN_KEY`, taffy
`ACCESS_TOKEN_KEY` / `ACCESS_TOKEN_SIGNING_KEY`, postgres
`CONTROLLER_KEY`, `DISCOVERD_AUTH_KEY` on jobs that talk to discoverd,
and the same controller key onto router and acme).
`flynn-host plugin:update` / `plugin:install` do the same for plugin
apps. A plugin counts as official only when its source is the catalog
repo (`owner/repo` exactly) on `github.com`; official plugins print that they
receive cluster secrets and continue. Everything else is third-party: the
same name served from another git host (for example GitHub Enterprise), a
different owner or repo, or a source with no repo identity such as a bare
local directory. Third-party plugins prompt on a TTY (cluster-admin
equivalent); pass `--yes` for non-interactive installs. You do not need to `env:set`
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
build jobs (no user namespace, extra capabilities for nested BuildKit).
That rule applies to every caller, including `cluster:admin`, the cluster
key, and system apps. Compiles are `POST /apps/:id/builds`, and only the
cluster key can call it. The scheduler places the job. It is not pinned to
the gitreceive host.

`slugbuilder` and `dockerbuilder` jobs stay visible on the app, including
while they are pending. `slugrunner` stays hidden. Setting builder process
limits requires the cluster controller key (`flynn limit:set` or
flynn-host). Path-based
HTTP routes (`example.com/api`) can only be created with
`flynn-host route:add`. App-scoped tokens with `app:routes:write` can only
point a route at a discoverd service this app owns (`$APP-web` /
`$APP-$TYPE`, or a `service` declared on the current release). Routing to
system services (`controller`, `blobstore`, `postgres-api`, dashboard, www,
discovery) requires the cluster controller key.

A release for a user app may only declare discoverd service names the app
owns: the app name itself or `$APP-*`, in a port's `service.name` and in the
process type's `service`. Platform names are refused even when the app name
is a prefix (`controller`, `controller-scheduler`, `blobstore`,
`acme-challenge`, `postgres`, `postgres-api`, `router-api`, `status-web`,
`flynn-host`, `flynn-net-*`, isolated datastore and appliance names), and so
is a name a more specific existing app owns (`shop` may not declare
`shop-admin-web` while an app `shop-admin` exists). The controller checks this
on `POST /releases`, gRPC `CreateRelease`, and `PUT /apps/:id/release`, for
every caller including the cluster key. flynn-host enforces the same prefix
rule when it accepts a user job and again before it registers the job's
ports, because it registers them with its own discoverd credentials. System
and plugin apps keep their names. Without this check a tenant job could
register as `controller` or `blobstore` and receive requests that carry the
cluster key, or as `acme-challenge` and answer HTTP-01 challenges for any
host name. The router also skips any `acme-challenge` instance whose IP is a
user or build job IP (`flynn-net-user`, `flynn-net-build`). `GET` route and route list
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
existing profiles. Non-admin callers also cannot set or change those
reserved keys on an app (`POST /apps`, `PATCH /apps/:id` meta), so a
tenant cannot mark its app as a plugin or system app to take a reserved
name. Host trust (the host API key, host network/PID, build-job
privileges) depends only on `flynn-system-app` or the system partition;
the app name `builder` alone is never trusted. The controller strips privileged process fields
from user-app releases at create and again before `PUT /apps/:id/release`
attaches a release; a cluster-admin or controller key can still create
privileged releases for system apps. `POST /releases` requires `app_id`
for app-scoped tokens, and minting a release needs `app:env:write` (the
Manage and Admin roles include it). `app:scale:write` alone cannot mint
a release. `GET /apps/:id/release` and release lists stay available to any
grant on the app, but `env` and per-process `env` are omitted unless the
caller has `app:env:read` (View, Deploy, Manage, and Admin include it;
`app:logs:read` and `app:scale:read` alone do not). The same omission
applies to expanded formations and release, `app_release`, `resource`, and
`resource_deletion` events. The cluster key and
cluster-admin tokens still receive env. `GET /apps/:id/log` stays
available with `app:logs:read`, as do `GET /apps/:id/jobs/:id/output` and
`GET /apps/:id/releases/:id/output`. `PUT /apps/:id/releases/:id/output`
requires `app:write`. `PUT` and `PATCH /apps/:id/jobs/:id` (the scheduler's job-state upsert) are
cluster-key only; `app:jobs:run` allows `POST /apps/:id/jobs` but cannot write job
rows. The job UUID in the body must match the URL, and the upsert never
rewrites a job that belongs to another app. The controller redacts current
release env values and per-process env values that appear in those log
lines, including the stored copies, when the caller does not have `app:env:read`. Values shorter than
8 characters are left as-is. Callers with `app:env:read` (including
View, Deploy, Manage, and Admin), cluster-admin tokens, and the cluster
key receive the log text unchanged. A release minted for one app cannot be attached to
another. `POST /apps/:id/deploy` refuses a release minted for another app
for every caller, with the same `could not find release` error as an
unknown id. `PUT /apps/:id/formations/:release`, `PUT
/apps/:id/scale/:release`, `DELETE /apps/:id/formations/:release`, and
`DELETE /apps/:id/releases/:release` answer 404 for another app's release
unless the caller is cluster-admin (operators may still place a formation
across apps). The app's current release is always accepted, so a
cross-app formation or current release made by an older controller stays
until it is cleared; `flynn-host cross-app:check` (`GET
/cluster/cross-app-releases`) lists them (see [Production — Cross-app
releases](production.html.md#cross-app-releases-left-by-older-controllers)). One-off jobs (`POST /apps/:id/jobs`) may still *run* another
app's release when the caller is cluster-admin, or when that release is
the image of a resource attached to this app (`flynn redis:cli` and the
other plugin consoles).

Resource routes check the owner and the provider. `GET
/providers/:p/resources/:id` returns the resource env (database URLs and
passwords), so it needs admin on the resource's `owner_account`, or the
cluster key or a cluster admin. A resource with no owner (a platform
database such as the controller's own) is cluster-admin only for read,
delete, and attach; attaching an unowned resource to a tenant app no longer
makes it that tenant's. `PUT /providers/:p/resources/:id` (create or rewrite
a resource row, including `external_id`, env and owner) is the cluster key
only; bootstrap, migration, and the database plugins use it. All
`/providers/:p/resources/:id` routes answer 404 when the resource belongs to
a different provider than the one in the URL. Provision, attach, and
detach (`flynn resource:add`, `addons:attach`, `resource:remove`) are
unchanged for the owner. `POST /providers/:p/resources` checks every app in
`apps`, not just the first: the caller needs write or admin on each one, and
each must have the same `owner_account` as the first (the rule attach
already applies), so a provision cannot attach a resource to another
tenant's app. That owner rule holds for the cluster key too. The platform
Postgres appliance accepts the request only when every listed app is a
system or plugin app.

`POST /users` creates an account that is already email-verified, so it is
cluster-admin only (the cluster key or a cluster admin). A logged-in tenant
cannot create accounts, which stops it squatting an address such as
`alice@corp.com` and receiving the grants made when Alice is later invited.
Self-signup goes through the dashboard, which calls the controller with the
cluster key. Do not run untrusted code in Flynn.

`DELETE /users/:id` (cluster-admin only) removes the user with their tokens,
OAuth sessions, collaborator rows, memberships, and handle in one
transaction. While the user still owns a live app (one in their personal
account, or one with no owner account that they created) it answers `409`
and deletes nothing; transfer or delete those apps first. Deleted apps keep
their row, so the delete clears `created_by` on every app the user created,
and an app they created in another account stays with that account. Any
other row that still references the user is also a `409` naming the table,
not a `500`.
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
`cluster.runtime_settings.set`, `policy.set`, `collaborator.add`,
`tenancy.update`, `tenancy.force_self_hosted`), the
app it touched, and when. Only successful requests are recorded, from
both the HTTP API and the gRPC API.
Request bodies are not stored, so env values and keys never land in the
log; entries keep the method, path, and status. Every row also passes
through one redaction step before it is written: a field whose name looks
like a password, key, token, secret, or credential is stored as
`[redacted]` at any depth (booleans such as `password_changed` are kept),
and inside an `env` map names such as `DATABASE_URL` are redacted too.
`user.update` records which user fields a `PATCH /users/:id` set and
`password_changed: true` when it set a password, never the password.

Before migration 94, `user.update` rows held the new password in clear
text. Migration 94 removes it from existing rows, but cluster backups taken
before the upgrade, and copies the enterprise plugin imported or sent to
its audit webhook, still hold it. Rotate the passwords of users changed
through `PATCH /users/:id` (dashboard password changes and resets synced
to the controller, and `flynn-host user:bootstrap-admin --password` on an
existing user) on a cluster that ran an older controller, and treat older backups as holding
those passwords.

### Request log (`AUDIT_LOG=true`)

Setting `AUDIT_LOG=true` on the controller logs every request with its
JSON body. Only bodies that describe app configuration are logged in full:
app create and meta updates, releases, jobs, deploys, formations, scale,
routes, collaborators, transfers, quotas, and the `/cluster/*` settings,
runtimes, and hosts. Those bodies get the same redaction as `core_audit`,
whatever the route type. Any other body (users, tokens, providers and
resources, sinks, GitHub, ACME, managed certificates, `PUT /domain`,
regions, tenant backups, plugin add-ons, and any route added later) is
logged as its field names only, for example
`[body not logged; fields: email,password]`.

`tenancy.force_self_hosted` is written before a cluster-key
`PUT /tenancy` with `"force": true` switches a hosted cluster to
`self_hosted` while a non-system app still has an `owner_account`. Its data
holds the previous mode, the number of owned apps, and what the switch
leaves in place; see
[Production — Tenancy mode](production.html.md#tenancy-mode). Only the
cluster key can send `force`.

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
