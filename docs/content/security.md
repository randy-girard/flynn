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

Flynn uses several ports to communicate internally, and currently there is no
authentication system for internal communication, so access to these ports must
not be exposed to the Internet. A firewall must be configured so that the only
Flynn ports accessible are 80 and 443 to prevent compromise. Access to these
internal Flynn ports is equivalent to root access, so be careful. After
install, `flynn-host firewall` manages extra peer IPs and TCP ports on the
host UFW rules; see [Production — Firewalling](production.html.md#firewalling).
`flynn resource:expose` prints `flynn-host firewall:expose` when a datastore is
exported on a TCP port in 3000–3500. Treat those ports as public; prefer TLS
passthrough (backend certs) or terminate (`--auto-tls`). Postgres enables
`ssl=on` with a cluster-generated certificate; in-cluster `sslmode=disable`
clients still work.

Access to the controller is available via HTTPS over port 443, and
a randomly generated bearer token is used for authentication. The TLS
certificate used for communication is generated during installation
(self-signed). Configure Let's Encrypt after bootstrap with
`flynn-host acme:configure --email=<you> --agree-tos` and
`flynn-host acme:enable-system-routes` so the dashboard and controller
present a trusted certificate; see [Apps — HTTPS](apps.md#https).
A cryptographic hash of the certificate is pinned as part of the CLI
configuration string to prevent man-in-the-middle attacks.

## Applications

Applications run in their own network namespace on the overlay. User jobs
cannot open connections to other user jobs or to internal Flynn services
(`controller`, `blobstore`, `postgres-api`, …). They may reach provisioned
datastores only at the leader host Flynn put in `DATABASE_URL` /
`REDIS_URL` / etc. Each Postgres role can CONNECT only to its own database
(`PUBLIC` CONNECT is revoked), so a user job cannot open the controller,
router, or blobstore databases. Cross-app HTTP still works through routes you
add (the router). System appliances keep a full overlay mesh.

User and build jobs cannot open the host control plane: SSH (`:22`),
host HTTP (`:80`/`:443`), the host API, or discoverd (`:1111`).
DNS to the overlay gateway (`:53`) is allowed. flynn-host registers user
HTTP backends with discoverd; the job never receives a `DISCOVERD` URL.

`flynn -a controller pg:psql` (and the same for `blobstore` / other system
apps) requires the cluster controller key. Dashboard tokens scoped to user
apps cannot open those consoles. Treat the key from `flynn cluster:add` as
root.

Dashboard JWTs hide builder process types (`slugbuilder`, `dockerbuilder`,
`slugrunner`) from jobs, logs, formations, and release process maps. Setting
those process limits requires the cluster controller key (`flynn limit:set` or
flynn-host). Path-based
HTTP routes (`example.com/api`) can only be created with
`flynn-host route:add`.

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
existing profiles. Do not run untrusted code in Flynn.
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
