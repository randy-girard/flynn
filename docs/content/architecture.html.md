---
title: Architecture
layout: docs
toc_min_level: 2
---

# Architecture

The Flynn architecture is designed to be simple and understandable. Most of the
components of Flynn are no different than the applications that are deployed on
top of Flynn.

Flynn's primary goal is to provide a solid platform to deploy applications on
that is highly available, easy to run and manage, and requires minimal
configuration.

## Building Blocks

Flynn is almost entirely written in Go. Go is one of the few languages designed
with concurrency as a first-class feature which makes it a great fit for Flynn's
distributed systems. Go is statically typed and reasonably fast to compile so we
catch more bugs before they ship. The limited syntax and built-in formatting
tools make contribution and collaboration smoother than any other language we've
used.

JSON over HTTP is used for communication, both internal and external. Our
experience is that it is easy to debug and understand HTTP-based services. We
use [Server-Sent Events](https://www.w3.org/TR/eventsource/) to provide streams
over HTTP. Since most developers are already familiar with HTTP and almost every
language already has great HTTP support, taking advantage of Flynn's APIs is
easier than yet another RPC protocol.

This fork targets **Ubuntu 24.04 LTS amd64** as the host operating system.
Flynn has no hard kernel dependency on a specific distribution, but supporting
several distros is time-consuming and irrelevant to the platform, so a single
common configuration is supported. Container images are built from Ubuntu
Noble and Heroku-24 roots.

## flynn-host

The lowest level component in Flynn is the flynn-host daemon. It is the only
service that doesn't run inside of a container. flynn-host provides an API for
starting and managing containers on a single host.

Flynn runs everything else in containers provided by flynn-host. The container
image and running systems are implementation details. Currently flynn-host
uses `libcontainer` to run containers and a custom system for container images.

The APIs that flynn-host provides are not specific to Linux containers, so we
call a running unit of work a *job*.

flynn-host watches the host root filesystem. Every 15 minutes, and whenever
free space is low, it deletes leftover per-job image directories and
unreferenced layer-cache files. Persistent ZFS volumes are not garbage-collected
this way: the controller tracks those, and `flynn-host volume:gc` (also run
automatically before a cluster update) removes volumes that are neither in use
nor still tracked. `flynn-host disk:reclaim` runs that volume GC, the image
cleanup, and ZFS TRIM so a sparse file vdev can give unused space back to `/`.

## Bootstrapping

After the `flynn-host` daemon is started on some hosts, the bootstrap tool
creates a Flynn cluster on top of them. It reads a JSON manifest that describes
the version and configuration of Flynn to be started and gets everything up and
running by talking to `flynn-host` and the services that are started
subsequently.

After everything is started for the first time, the components work together to
keep the system up and running, even in the face of individual host failures.

## discoverd

One of the most important components of Flynn is discoverd, which provides
service discovery to the cluster. All Flynn components register with discoverd
and then heartbeat every few seconds to confirm that they are still alive.
A steady heartbeat is a stamp on the leader's memory, not a Raft write. A
process that holds many registrations, including `flynn-host` for the user
jobs on that host, sends those instance ids in one `POST /heartbeats` per
period. The period is 5 seconds minus a jitter of up to 2 seconds, inside
the 10 second expiry window. The first registration, an address or metadata
change, and unregister still go through Raft. While a leader is rolling
forward, a missing batch route returns 404 and the client uses one PUT per
instance. Proxies and followers redirect heartbeats to the leader. After a
leader change, expiry waits 20 seconds so live instances can stamp the new
leader. Hosted and self-hosted clusters use this same path.

discoverd provides an API over HTTP that provides access to the list of
registered instances of each service. A compare-and-swap metadata store is also
available for each service, which provides consistent, linearizable storage of
high value configuration values.

Watchers (`GET /services/:service` or `/instances` with
`Accept: text/event-stream`) first receive every current instance, the leader,
the metadata and a `current` marker, then live events. That snapshot is taken
under the store lock but sent outside it, so a service with many instances,
such as `flynn-net-user` with one per user job, does not stall writes, DNS or
other watchers. A watcher that falls more than 64 live events behind is
disconnected and has to reconnect.

The Raft algorithm is used to ensure that data is consistent and stays as available
as possible during failures. A discoverd instance runs on every host in the
cluster, and all instances provide reads. Instances that are not part of the
consensus cluster act as a cache and redirect relevant requests to the leader.

Leader election for each registered service is performed automatically,
selecting the oldest instance of each service as the leader.

`DELETE /services/:service` removes the service, its metadata, its instances
and its leader. Heartbeats for an instance of a removed service are refused,
so a job that is still running re-creates the service on its next heartbeat
and registers again, and the new service starts with only the jobs that came
back. A manual-leader service has no leader until one is set again. Setting
a leader on a service that does not exist is refused. A leader that took
over from an older discoverd also drops what removed services left behind,
on its first expiry sweep.

Services are also exposed via DNS to allow clients that are not aware of
discoverd's HTTP API to find and communicate with each other.

Each `*.discoverd` query first finds the client's job class from its source
IP. discoverd keeps the four netpolicy services (`flynn-net-user`,
`flynn-net-sys`, `flynn-net-data`, `flynn-net-build`) indexed by address,
updated as Raft entries are applied, so that lookup is one map read under a
shared read lock and does not grow with the number of user jobs. Queries run
in parallel and do not hold up Raft apply or heartbeats. A discoverd that is
not a Raft peer builds the same index from event streams of those four
services to the peers. Until a stream has sent its current snapshot, or while
it reconnects, that service is listed over HTTP instead.

## flannel

An overlay network is automatically configured using the Linux kernel's native
support for VXLAN encapsulation of Layer 2 frames over UDP. Each host in the
cluster is assigned an /24 block of IPv4 addresses and registered in discoverd.

IPs from the block are allocated to each job that is started by flynn-host.
Routes are configured by flannel that route all inter-container communication to
the correct host.

When a job ends, flynn-host deletes its veth from the `flynnbr0` bridge and
keeps the job's IP out of the pool for two more minutes, so clients that still
hold the old address do not reach a new job. The kernel can keep a finished
job's network namespace, and so its veth, for minutes while closed TCP
connections retransmit. If flynn-host cannot delete the veth, the IP stays
allocated until the veth is gone. After a flynn-host restart, the IPs of jobs
that ended in the last two minutes (from the host state database) and the IPs
still in use behind bridge ports that no running job owns are held the same
way. When no other IP in the block is free, the held IP whose time is
closest to running out is reused, rather than refusing to start the job.

This combined with discoverd's DNS service discovery allows well-known ports to
be used, avoiding complicated port allocation and NAT.

The default network is `100.100.0.0/16`. Bootstrap can set `FLANNEL_NETWORK`
and `FLANNEL_SUBNET_LEN` before metadata exists. On a running cluster,
`flynn-host overlay:set` is the way to widen that network or change the
per-host prefix. Existing leases stay. Extra hosts beyond the Raft voter
set remain discoverd proxies. See [Production — Overlay](production.html.md#overlay).

## PostgreSQL

A state machine persisted in discoverd powers automatic configuration and
failover of a highly available PostgreSQL cluster running within Flynn. We use
synchronous replication in a chained cluster that is easy to reason about to
ensure that no data is lost.

All persistent data stored by Flynn **system** components is in this
appliance. Tenant databases are the `postgres` catalog plugin
(`flynn addons:create postgres` after `flynn-host plugin:install postgres`),
not roles on this cluster. The appliance provides a great combination of
features, performance, and reliability that we have not found in any other
database systems.

## Controller

The controller provides a HTTP API that models the high-level concepts that come
together to form a running application in Flynn.

The lowest-level object in the controller is an *artifact*. Artifacts are immutable
images that flynn-host uses to run jobs. Usually this is a URI of a container
image stored somewhere else.

Building on top of artifacts is the *release*. Releases are immutable
configurations for running one or more processes from an artifact. They contain
information about process types including the executable name and arguments,
environment variables, forwarded ports, and resource requirements.

The immutability of artifacts and releases allow for easy rollbacks, and ensure
that no action is destructive.

The final object is the *app*. Apps are namespaces that have associated metadata
and allow instantiation of running releases. These running releases are called
*formations* and are namespaced under apps. Formations reference an immutable
release and contain a mutable count of desired running processes for each
process type.

The controller's scheduler gets updates whenever desired formations change and
ensures that jobs are always running as required by talking to the flynn-host
APIs on each host. A scheduler instance runs on every host in the cluster for
fault tolerance, but only the leader elected by discoverd makes scheduling
decisions. The leader's loop is the only writer of in-memory jobs, volumes,
and placements. Host and controller calls from that loop run outside it, and
the loop only applies the results. When a host rejects a job because a volume
is missing, the loop resets or blocks the job only if it is still the pending
job that was sent. A job the loop has already stopped is not placed again. A
slow host API stalls that host's call.
Placement onto other hosts continues. This is the same process on a hosted
cluster and a self-hosted one. Writes back to the controller stay on a
separate persistence loop. A write that is still failing after one minute is
dropped from that queue. A failed write of a stopped job keeps the job in
memory so a later sync can retry it.

Every service, including the controller, is an app in the controller and is
scaled and updated using the same APIs that are used to manage every other app.
This allows Flynn to be entirely self-bootstrapping and removes a huge amount of
complexity involved in deploying and managing the platform.

## Router

The router allows external clients to communicate with services running within
Flynn. HTTP requests and TCP connections are load balanced by the router over
backing services based on configured routes.

A route uses discoverd to find the instances of the named service and matches
incoming requests against a pattern: a domain and optional path for HTTP, and
a specific port for TCP. TCP routes may also store a hostname (for example
`postgres.clusterdomain`) used as TLS identity and operator DNS.

HTTP TLS is terminated at the router. You can attach a certificate chain to a
route, or enable ACME/Let's Encrypt (`flynn-host letsencrypt` and `flynn letsencrypt:enable`). HTTPS also enables HTTP/2.

TCP routes have three TLS modes:

* **off** — plaintext proxy (historical default for `flynn route:add tcp`)
* **passthrough** — the backend speaks TLS (default for `flynn addons:expose`;
  required for Postgres/MySQL SSLRequest)
* **terminate** — the router wraps the listener (`--auto-tls` or a manual cert)

`flynn addons:expose` creates the TCP route on the lowest free port in
10000–29999. The host firewall opens that one port on every host, the same way
it opens 80/443 for HTTP. It does not open the rest of the window. Ports
outside the router pre-bind (3000–3500) listen when the route exists. See
[Production — Firewalling](production.html.md#firewalling). A route update
(for example a new certificate) or a router resync takes over the route's
existing socket, so old settings stop serving at once and a deleted route's
port stops accepting.

An instance of the router runs on every host so client traffic can land on any
node. The router uses host networking and binds ports 80 and 443, so
`flynn-host update` stops the old router before starting the replacement, one
host at a time on a multi-node cluster (other nodes keep serving). A
single-node cluster still has a brief HTTP/HTTPS blip. Redis appliances use the
same one-down-one-up strategy so the data volume is reused.

User-deployed jobs are isolated on the overlay: they cannot open connections to
other user jobs or to internal Flynn services. They cannot open host SSH,
discoverd, or other node APIs. They reach HTTP through
routes you add, and datastores only at the leader host injected in `DATABASE_URL` /
`REDIS_URL` / similar. System apps keep a full overlay mesh. User jobs also
run in a user namespace (container root is not host root); squashfs layers are
idmapped so image UIDs still look like 0/5000 inside the job. Overlay `/` is
chowned to the mapped UID so container images can create directories there.

## Buildpacks and container images

`git push` defaults to the **heroku-24** stack: [Heroku
Buildpacks](https://devcenter.heroku.com/articles/buildpacks) turn source into a
*slug* (a tarball of the app and its dependencies) that *slugrunner* executes.

Apps can instead use the **container** stack (`flynn stack:set container`). The
cluster builds a `Dockerfile` with BuildKit (*dockerbuilder*) and imports the
image the same way as `flynn docker:push` (*tarreceive*).

Neither path is special-cased in the scheduler; both register artifacts and
releases through the controller and roll out with the same deploy logic.

### gitreceive

Git pushes are received over HTTPS by the gitreceive component which spawns
a receiver that starts a new *slugbuilder* job to process the build. If the repo
has been pushed before, a cached archive of the repo will be downloaded from the
*blobstore* before receiving the git push. A tenant's push is authorized
against the grants the controller returns for that caller from
`GET /whoami/grants` (see [Security](security.md)).

### slugbuilder

A slugbuilder job takes an incoming tar stream of the code being deployed,
selects a buildpack and then runs it against the code. The result is a *slug*
that contains the compiled application code and all of its dependencies.

The slug is uploaded to the blobstore, the receiver registers the new artifact
and release in the controller, and then tells the controller to do a rolling
deploy of the new release.

On the **container** stack, gitreceive starts a *dockerbuilder* job instead.
BuildKit produces an image that *tarreceive* imports.

### taffy

Taffy clones a Git repository (including GitHub App HTTPS clones) and pipes an
archive of the revision into *flynn-receiver*, the same builder gitreceive uses
for `git push`. After `flynn-host plugin:install github`, the dashboard Deploy
tab and `flynn github:deploy` start a taffy job. GitHub App webhooks on the
controller (and proxied at gitreceive `/github/webhook`) can start that job
automatically.

### blobstore

The blobstore provides a simple API for storing and retrieving binary blobs.
Git repositories, app slugs, and buildpack caches are stored in the blobstore.
Object routes require the cluster key; health (`/.well-known/status`) does not.
Build-cache URLs carry a per-app HMAC token that blobstore verifies. Postgres is
the default backend; operators switch to S3-compatible storage with
`flynn-host blobstore:set` (see [Production — Blobstore Backend](production.html.md#blobstore-backend)).

### slugrunner

Slugrunner is responsible for running a specific process type from an app slug
created by slugbuilder. When an application deployed with the buildpack flow is
scaled up, a slugrunner job is started for each process and then executes the
application from the slug.

## Log Aggregator

The logaggregator takes log lines from jobs running on each host and buffers
the newest 10,000 lines for each app in that aggregator process. The lines
are sent from flynn-host as RFC6587-framed RFC5424 syslog messages over TCP.
A frame whose MSG-LEN is not a positive number of at most 11,000 bytes closes
that one connection and is logged, and a frame that is not valid RFC5424 is
logged and skipped; the aggregator and its other connections keep running.
The ring is a live tail, not retention. A ring exists only for an app that
has logged or that someone follows with `flynn log -f`; reading an app that
has none does not create one. The aggregator follows the controller's
`app_deletion` events (replaying the last 1,000 on every connect) and drops
a deleted app's ring at once, refusing its late lines for `LOG_BUFFER_TTL`.
A ring that has had no line for `LOG_BUFFER_TTL` (a Go duration in the
logaggregator release env, default `168h`) and has no follower is dropped
too. A new aggregator instance loads the leader's snapshot, which leaves out
empty and expired rings, so dropped apps are not copied forward on deploy. Each host also keeps about 200MB of
log files per app. A cluster that needs to keep logs adds a syslog sink
with `flynn-host log-sink:add`. Hosted and self-hosted clusters use that
same path.

Clients can get live streams of aggregated logs, and retrieve previous log
messages without sending requests to every host individually. Do not scrape
the aggregator's log port for retention. It is unauthenticated.
