---
title: Production
layout: docs
toc_min_level: 2
---

# Production

We've designed Flynn to be very easy to get up and running with quickly.
However, there are a variety of things that should be considered as you start
using Flynn more.

## Production check

`flynn-host production:check` prints a bordered table and exits 1 when any
status is `fail`. A `warn` does not fail the command. Columns are status,
check, detail, and what to do. Long cells wrap inside the border. On a
terminal the status is green for `ok`, yellow for `warn`, and red for
`fail`. Set `NO_COLOR` to print plain text. Run it on a cluster host:

```text
$ sudo flynn-host production:check
$ sudo flynn-host production:check --zpool-name=flynn-default
```

`WHAT TO DO` names the command or operator step for that row. `NO_COLOR`
turns the status colors off.

`--zpool-name` defaults to `flynn-default`. The command reads tenancy,
workload placement, the blobstore release, runtime profiles, discoverd host
tags, `GET /sinks`, flannel service metadata, the formations and discoverd
sirenia state of each sirenia app, and this host's pool. It does
not migrate the blobstore, set `flynn-max-processes` or
`flynn-max-persistent-singletons`, write `workload_placement` or
`flynn-role`, create or move volumes, detach a vdev, or dial a log sink.

| Line | Meaning |
| --- | --- |
| `service-token` | Reads the billing and letsencrypt plugin releases. Warns when `QUOTA_TOKEN` or `ACME_TOKEN` is expired, unparseable, the wrong scope, or within 30 days of expiry. No row when healthy or not installed. Fix with `plugin:update` or `token:rotate`. |
| `tenancy` | `GET /tenancy` (`cluster_settings.tenancy_mode`). A controller error is `fail` and is not treated as `self_hosted`. |
| `placement` | `GET /cluster/placement` (`cluster_settings.workload_placement`). `mixed` is `ok`. `split` with no host that is not shutting down and tagged `flynn-role=core` is `fail`. `split` with no `flynn-role=app` host is `warn`. A non-omni job on the wrong role is `warn` and names the app. The check does not write the mode or any tag. Set it with `flynn-host placement:mode` and `flynn-host init --role`. |
| `blobstore` | Prints the default name only, never `BACKEND_*` values. Empty `DEFAULT_BACKEND`, a `postgres` name (any case), or a parsed `backend` type that is missing or `postgres` (any case) is hosted `fail` and self-hosted `warn`. `s3`, `minio`, `gcs`, and `azure` are `ok`. The check does not migrate. A green line means that release's parsed type is not `postgres`. It is not proof bytes left Postgres. |
| `blobstore-auth` | Reads the same blobstore release and prints key names only, never values. No `AUTH_KEY` or `CONTROLLER_KEY` and no `ACCESS_TOKEN_KEY` is `fail`: the blobstore refuses every object request with `401`, so builds and deploys fail. `BLOBSTORE_ALLOW_UNAUTHENTICATED=1` with no key is `fail`: every object is open to any caller. That override with keys set is `warn` (the keys are enforced, but removing them would open the store). A missing or invalid `ACCESS_TOKEN_KEY` is `warn` because scoped build tokens are refused. No `AUTH_KEY` with a token key is `fail`. Tenancy does not change this line. Fix the release with `flynn -a blobstore env:set`. |
| `zfs` | `zpool status -P` for this host. `fail` in either tenancy mode when any leaf is a regular file, including a mirror that still lists the file. A block device passes. Tenancy does not change this line. A missing pool or `zpool` error is `fail` either way. A green line is this host only. |
| `reserve` | Hosted is `ok` when the profile list can be read. The scheduler reserves builtin `small`, `medium`, and `large` even when `reserve_resources` is false (`scheduler=hosted_builtin`). `runtime:reserve` only stores the flag. Unknown tenancy is `fail` and is not treated as self-hosted. Self-hosted is `ok` with the flags off. The action tells a large cluster to run `flynn-host runtime:reserve` on profiles that must fit. An empty name does not reserve. One other profile does not stand in for the three builtins. Database runtimes are not read. |
| `host-cap` | `fail` unless every host registered in discoverd has a numeric `flynn-max-processes` tag. `unlimited`, `0`, empty, `off`, `none`, and `auto` are no cap. The check does not write the tag. The same rule applies in hosted and self-hosted mode. |
| `singleton-cap` | `warn` unless every host registered in discoverd has a numeric `flynn-max-persistent-singletons` tag. `unlimited`, `0`, empty, `off`, `none`, `auto`, and a non-integer are no cap. `ok` when every host has a number. Warn does not exit 1. The check does not write the tag. The same warning applies in hosted and self-hosted mode. Set it with `flynn-host tags:set <host> flynn-max-persistent-singletons=<n>`. |
| `postgres-availability` | `GET /cluster/postgres-availability`. `manual` is `ok` in either tenancy mode. `system` with fewer than two registered hosts is `warn` in either tenancy mode and does not exit 1. `system` with two or more hosts is `ok`. The line does not query replication lag. A controller error is `fail` and is not treated as `manual`. Tenancy does not change the stored value. Set it with `flynn-host postgres:availability` or Cluster → Settings. |
| `mysql-availability` | Same rule as `postgres-availability`, from `GET /cluster/mysql-availability`. Set it with `flynn-host mysql:availability` or Cluster → Settings. |
| `redis-availability` | Same rule as `postgres-availability`, from `GET /cluster/redis-availability`. Set it with `flynn-host redis:availability` or Cluster → Settings. |
| `mongodb-availability` | `GET /cluster/mongodb-availability`. `manual` is `ok` in either tenancy mode. `system` with fewer than three registered hosts is `warn` in either tenancy mode and does not exit 1. `system` with three or more hosts is `ok`. The line does not query replica-set health. A controller error is `fail` and is not treated as `manual`. Tenancy does not change the stored value. Set it with `flynn-host mongodb:availability` or Cluster → Settings. |
| `kafka-availability` | Same rule as `mongodb-availability`, from `GET /cluster/kafka-availability`. Set it with `flynn-host kafka:availability` or Cluster → Settings. |
| `clickhouse-availability` | Same rule as `mongodb-availability`, from `GET /cluster/clickhouse-availability`. Set it with `flynn-host clickhouse:availability` or Cluster → Settings. |
| `backup` | No Postgres primary and no MongoDB, Kafka, or ClickHouse resource: `warn`, and the detail stays `tenant volumes are outside flynn-host backup`. A Postgres primary or a MongoDB, Kafka, or ClickHouse resource whose last dump is missing or strictly older than 24h plus 1h is `fail` in both hosted and self-hosted mode. A current dump is `ok`, and the detail still says volumes are outside the tarball. The check reads `GET /providers/postgres/resources`, `GET /providers/mongodb/resources`, `GET /providers/kafka/resources`, and `GET /providers/clickhouse/resources`. A 404 on a provider means that engine has no resources. Any other list error fails the line. It reads `GET /tenant-backups` only when at least one counted resource exists. It does not dump. |
| `sirenia-formation` | For the system Postgres and every sirenia plugin app, compares the release the sirenia primary runs from (its `FLYNN_RELEASE_ID` in discoverd) with the app's current release. `fail` when the current release's formation runs no engines and the primary's release runs them. Older releases of Flynn copied that zero on every deploy and left the engines where they were. Run `flynn-host update` (or deploy the app), which moves them to a new release. Do not scale the current release up by hand, because that doubles the engines. `warn` when the primary is on another release and the current release also runs engines. `ok` prints the number of sirenia apps. Both shapes also appear while a deploy of that app is running. Release ids are shortened to 8 characters. A controller or discoverd error is `fail`. |
| `log-sink` | `GET /sinks` once. It does not call `log-sink:list` on a host and it does not dial. Hosted is `fail` and self-hosted is `warn` unless the list covers system and app logs. A qualifying sink is `kind=syslog`, empty app, `syslog+tls` with a host, `insecure` false, format empty or `rfc6587`, and an empty prefix. Scope `all` (an empty scope is `all`) is enough. So is one `system` sink and one `apps` sink. `syslog://`, `insecure`, `newline`, `prefixed_newline`, a prefix, a per-app sink, and `kind=logaggregator` do not count. A list error, an unknown tenancy mode, or a failed tenancy read is `fail` in either mode. That is not a self-hosted warning. The same mux, disk files, and drop counter run in both modes. `tenancy_mode` only changes this line's severity. |
| `overlay` | Informational. Prints the live flannel network and subnet length. Change it with `flynn-host overlay:set`. |

ZFS is local, so run the check on each host. `log-sink` is the controller
sink list, not the sinks bolted onto one host. The other lines are cluster
state, except `host-cap` and `singleton-cap`, which list only the hosts
currently registered in discoverd.

Set a cap with `flynn-host init --max-processes` or
`flynn-host tags:max-processes`. On a new `host.json`, omitting
`--max-processes` writes `flynn-max-processes=auto`. The host daemon
replaces that with `floor(MemTotal / smallest published runtime memory)`,
at least 1 when both are positive, and not more than the overlay addresses
on the host (253 on a `/24`). The sizes are the live app runtimes and the
database runtimes that are actually stored, for a hosted cluster and a
self-hosted one. Builtin small/medium/large are used only while the value
is still `auto` and the controller is not up yet. A number passed to
`init` or `tags:max-processes` stays put. `tags:max-processes <host> --auto`
opts an existing host into the same refresh. `unlimited` or `0` removes
the cap. The count is not a memory reservation.

That host tag is not the per-type formation cap. `runtime_settings.max_processes`
defaults to 10 and stays 10 on upgrade. `flynn-host runtime:max-processes 50`
raises how many jobs one process type may run. `runtime:max-processes unlimited`
clears it. `0` is rejected. A self-hosted company uses this command; the cap
still applies when an account also has a quota. On a hosted cluster, an app
with an owner is limited by that account's process and memory quota instead,
and an app with no owner keeps the formation cap. `production:check` does
not change this value. Hosted mode already
reserves builtin `small`, `medium`, and `large`. A large self-hosted
cluster turns that reservation on with `flynn-host runtime:reserve`.

Compile capacity is a different pair of numbers, also the same in hosted
and self-hosted mode. `runtime_settings.build_cluster_limit` defaults to 8
and `build_account_limit` defaults to 3. The account cap is how many
`slugbuilder` and `dockerbuilder` jobs one `owner_account` may have pending
or running, across all of that account's apps. It must stay strictly below
the cluster cap, so one account cannot fill every slot. An empty
`owner_account` is one shared bucket, not unlimited and not one bucket per
app. A company that never set owners still cannot run unbounded builds; set
`owner_account` per team, and raise both limits with
`flynn-host runtime:build-limits <cluster> <account>` (for example `16 4`).
There is no unlimited value. `tenancy_mode` does not change these limits.
The first push of an app creates a release row so the compile job has a
release id. That row is removed once the real release is deployed.
Release history and rollback on a hosted or self-hosted cluster see only
the deployed releases.

The scheduler leader refreshes each host's CPU and memory totals about every
10 seconds in either tenancy mode. A reserving job skips a host that has
never reported those totals. A failed read keeps the last totals. Shared
jobs are unchanged.

For S3 and MinIO, move the blobstore with
`flynn-host blobstore:set --migrate --delete`. GCS and Azure use `env:set`
plus `flynn-host blobstore:migrate --delete` under
[Blobstore Backend](#blobstore-backend). The file-backed
pool migration is the mirror-and-detach procedure under
[Storage](#storage).

An empty flannel `NETWORK` is `100.100.0.0/16`. When subnet length is unset,
flannel uses 24 if the network is wider than `/24`, and one bit narrower when
the network is `/24` or smaller. `100.64.0.0/16` is the Vagrant network. The
overlay line prints whatever flannel metadata stores. It does not substitute
a fixed size.

## Production configure

`flynn-host production` lists `check` and `configure`. It does not start the
wizard. `production configure` is `production:configure`. The same steps run
for `self_hosted` and `hosted`. The mode only changes the sentence about what
`production:check` will say. A self-hosted warn can still be a finished wizard
step. The check's fail and warn rules are unchanged.

```text
$ sudo flynn-host production
$ sudo flynn-host production:configure
$ sudo flynn-host production:configure --zpool-name=flynn-default
```

It is safe to re-run. The cluster is the saved progress. There is no progress
file. Each step prints the current value, what it means, and the command it
will run. A step the cluster already satisfies is not applied again. A step
they skipped is asked again. Ctrl-C leaves finished steps in place. A missing
TTY does not migrate, delete, reserve, or install.

The steps are tenancy (unchanged unless they ask), the blobstore, ZFS on this
host, the per-host process cap, runtime reservation, a cluster log sink,
Let's Encrypt, a platform backup, the live overlay ceilings, and DNS and
firewall text. It then runs `production:check`. It does not set workload
placement and does not write `flynn-role`. The printed check includes the
placement line.

`blobstore:set --region` is the S3 or MinIO location. It is not written to
`cluster_settings.region`. GCS and Azure stay on the `flynn -a blobstore
env:set` path, then `blobstore:migrate --delete` only after a yes. The wizard
does not print secret keys and does not scan `files` or `pg_largeobject`. A
new default is not proof the old copies are gone, and a later `--delete` does
not retry rows `SetBackend` already moved.

ZFS reads `zpool status -P` and does not attach, detach, or create a pool. A
file leaf prints the mirror-and-detach procedure under
[Storage](#storage). Run the wizard on each host.

`unlimited`, `0`, empty, `off`, `none`, and `auto` are not a
`flynn-max-processes` cap. The wizard asks for a number and does not invent
one. Hosted placement already reserves builtin `small`, `medium`, and `large`
when `reserve_resources` is false. `runtime:reserve` only stores the flag.
Self-hosted placement follows the stored flag. The wizard offers that step in
both modes.

`flynn-host backup` restores the platform and does not include tenant volumes.
The overlay line prints the live network, subnet length, and the ceilings from
that config: container addresses are `2^(32-subnet length)-3` (253 on a `/24`),
and the host count is flannel's range after the first subnet is skipped (255
hosts on a `/16` with subnet length 24). It does not change flannel.
Change the overlay with `flynn-host overlay:set`. Re-run the same command
if a host does not apply it. See [Overlay](#overlay).

## Overlay

The default overlay is `100.100.0.0/16` with a `/24` per host. Flannel skips
the first subnet, so that is 255 hosts. `FLANNEL_NETWORK` and
`FLANNEL_SUBNET_LEN` (plus `FLANNEL_SUBNET_MIN` and `FLANNEL_SUBNET_MAX`)
apply only when bootstrap creates flannel metadata that does not exist yet.
A later edit of the flannel app env is ignored. The live path is
`flynn-host`.

```text
$ sudo flynn-host overlay:show
$ sudo flynn-host overlay:set --network 100.64.0.0/10
$ sudo flynn-host overlay:set --network 100.64.0.0/10 --subnet-len 24
```

`overlay:show` prints the CIDR, subnet length, allocated leases, free
capacity, and whether each host has applied that CIDR. A host with no stored
overlay is using the `/16` inferred from its bridge subnet. On a default
install that is not behind.

Use a wider network when you need more than 255 hosts and want to keep a
`/24` per host (`100.64.0.0/10` with subnet length 24 is 16383 host subnets).
Use a longer subnet length when each host needs fewer container addresses
and you want more hosts inside the current network. The command refuses a
prefix outside 8–28, a subnet length that is not strictly longer than the
overlay prefix, a network outside `10.0.0.0/8`, `172.16.0.0/12`,
`192.168.0.0/16`, and `100.64.0.0/10`, and a network that contains a current
host API address. It refuses a shrink or a move that would drop an allocated
lease, and it refuses a move where neither network contains the other.

Existing leases stay. New hosts take unused subnets from the updated pool.
Running jobs keep their addresses. The command does not restart flannel or
jobs. A flannel process that is already running keeps allocating at its
current subnet length until that process starts again; it still reuses its
preferred subnet, so the bridge address does not move.

Widening applies the new exclusion and link route on every host, then writes
discoverd metadata. If a host fails, metadata is unchanged. Shrinking writes
metadata first, then narrows host rules. If that apply fails, metadata stays
at the smaller network and the wider exclusion is still safe. Repeating the
same CIDR and subnet length does not write metadata. If a host is behind, the
same command updates that host only.

A failure names the host and tells you to re-run the same `overlay:set`.
That is the recovery path. Do not edit flannel metadata by hand and do not
roll a partially applied change back under leases that already fit. You
cannot shrink under allocated leases.

Masquerade and the user-overlay drop use the real overlay CIDR. Traffic
between subnets inside a network wider than `/16` is not masqueraded. A
default `/16` install does not change those rules until you run the command.

Raft voters stay the initial consensus set (3 or 5). Extra hosts remain
discoverd proxies. Postgres, MariaDB, and MongoDB sirenia replica sets stay
at their current size. `overlay:set` does not call those APIs.
`production:configure` still only prints the overlay.

## Tenancy mode

`self_hosted` is the schema default. That is a company running Flynn for
itself. `flynn-host tenancy:mode hosted` is the line between that and a
cluster whose customers share it. The command writes only
`cluster_settings.tenancy_mode`. It does not set `signup_enabled`, the
dashboard `allow_signup` flag, or `reserve_resources`, and it does not run
`production:check`.

Run [`production:check`](#production-check) and fix every `fail` before
switching. `tenancy:mode hosted` judges those same lines as if the mode were
already `hosted`. A `fail` refuses the change. A `warn` does not. The error
names the failing checks and points back at `production:check`. Scale already
passes real process memory into the account quota, and hosted placement
already reserves CPU and memory for builtin `small`, `medium`, and `large`.
This command does not repeat either of those. It does not reschedule jobs.
Reservation is read when a job is placed.

One-off jobs (`flynn run`, `POST /apps/:id/jobs`) on an app with an owner
count against the same account `max_processes` and `max_memory_mb` as its
formations. Each one-off is checked with every formation process and every
one-off of that account that is still pending, starting, up, or stopping,
and a running one-off also counts when a formation scales up. A one-off
counts at its memory limit (1 GiB when the request sets none). The
controller stamps the admitted MiB on the job as `flynn-one-off-memory-mb`;
tenants cannot set that key. One-offs started before this check existed
carry no stamp and are not counted. A one-off the controller has started but
the scheduler has not recorded yet still counts for two minutes, so
back-to-back requests cannot each pass alone. That hold lives in one
controller process, so two requests that reach different controller web
processes at the same moment can still both pass once.

`flynn-host tenancy:mode self_hosted`, while the cluster is hosted, is
refused when any app that is not a system app (`flynn-system-app=true`)
still has an `owner_account`. That reversal is not safe. The command does
not delete `account_quotas` rows, does not clear `reserve_resources`, and
does not remove owners. A cluster that is already `self_hosted` is not
blocked by this rule, so a company that already runs Flynn for itself can
set the same mode again.

The operator override is `flynn-host tenancy:mode --force self_hosted`. It
sends `{"mode":"self_hosted","force":true}` to `PUT /tenancy`, and the
controller accepts `force` from the cluster key only: a cluster admin user, a
tenant, and an app token get `403`. `force` with `hosted` is a validation
error. Without `force` the refuse above is unchanged. Before it switches, the
controller writes a `tenancy.force_self_hosted` row to the
[audit log](security.md#controller-audit-log) with the previous mode and the
number of owned non-system apps, and the command prints what the switch does:
`owner_account` is not cleared, `account_quotas` rows stay and still apply,
an account with no quota row becomes unlimited immediately while its apps keep
running, `reserve_resources` is not cleared, and running jobs are not
rescheduled. Use it to put a test or misconfigured cluster back to
`self_hosted`. It is not a way to retire customers from a hosted cluster.

In `hosted` mode a tenant route outside the cluster domain needs a domain
the owner account proved with a DNS TXT record. The controller does the
lookup itself and refuses public suffixes, special-use names, and the
cluster's own domains. Controller migration 93 clears every stored
verification, so owners re-run the check after this update. See
[Hosted custom domains](security.md#hosted-custom-domains).

When the reversal is allowed, an account with no `account_quotas` row is
unlimited immediately, including while apps that are still running keep
running. An explicit quota row still applies, in hosted mode and in
self-hosted mode. A nil limit on that row is unlimited.

A formation PUT that shrinks the account process count or the account
memory total, without growing the other one, is accepted even when the
account is already over its quota. Putting the same counts again while
over the limit is still rejected. A change that grows past the limit is
still rejected. Autoscale steps by one and uses that same PUT. It does
not read the quota API first. An app with no owner is not blocked for
having an empty `owner_account`. The ceiling that applies without a
hosted account is `runtime:max-processes`, described above.

Public signup stays off unless `signup_enabled` is already true. Cluster
Settings "Self sign-up" is the dashboard `allow_signup` flag. Public signup
needs that flag and controller `signup_enabled`. If the dashboard cannot
read tenancy, the dashboard flag alone allows signup. `tenancy:mode` does
not write either flag.

`plugin:install hosted` sends the same `{"mode":"hosted"}` body and uses the
same refuse. There is no `tenancy:set`.

## Cluster Requirements

At least three hosts are required to deploy Flynn in a highly available
configuration. We do not recommend single-node clusters for production use.
A three-host cluster will withstand the loss of one node with little to no
impact on availability.

All members of the initial cluster participate in Raft consensus voting, hosts
started after the initial bootstrap act as proxies to the consensus cluster. We
recommend starting with three or five hosts and adding more hosts when
necessary.

Discoverd heartbeats use the same path on a hosted cluster and a self-hosted
one, including a large self-hosted deployment. Steady heartbeats are stamps
in the leader's memory. User jobs on a host share one batched request, so
Raft writes do not grow with web process count. Expiry remains one leader
command with a 10 second TTL. There is no setting for this.

Each host should have a minimum of 2GB of memory, and inter-host network packets
should have a latency of less than 2ms. Deploying a single Flynn cluster across
higher latency WAN links is not recommended, as it can have a significant impact
on the stability of cluster consensus.

## Regions

A region is another Flynn cluster. It is not more hosts on this Raft group or
this overlay.

`flynn-host bootstrap --region` stores a short label on this cluster. Omitted
or empty stores `US`. `flynn-host init` with this cluster's discovery token
joins this cluster. That is the WAN stretch described above. Do not use
`firewall:peer:add` to attach a distant host. That command opens one UFW
address. It does not add a region.

A second region is a separate install: its own discovery token, Raft voters,
overlay, `CLUSTER_DOMAIN`, and controller. Bootstrap it with its own
`--region` label. On each cluster that should list the other, run:

```text
sudo flynn-host region:link --region EU --domain eu.example.com --tls-pin <pin>
```

The controller dials `https://controller.<domain>/cluster/region`, checks the
pin, and accepts a response that is only the peer's region name. It does not
send `CONTROLLER_KEY` and does not store the peer's cluster key. It does not
copy users, passwords, or sessions. `flynn cluster:add` is the laptop
`~/.flynnrc` entry and is not this link. `region:list` prints this cluster and
the links and does not dial them. At most 64 links. `region:unlink` removes
one row. A tenant cannot register a URL. Linking does not require billing or
a user account: `flynn-host` uses the cluster key, including on a self-hosted
cluster that has no accounts yet.

`blobstore:set --region` is the S3 or MinIO location (`us-east-1`). It does
not change `cluster_settings.region`.

Users pass `--region` to `flynn apps:create`, or pick it on the dashboard
create form. The create runs on that cluster as the logged-in user. It does
not create an app here and label it with the other region. If the laptop has
no `~/.flynnrc` entry and login for that domain, run `flynn cluster:add` and
`flynn login`. The controller does not store that password.

The dashboard shell lists this cluster and the links on every page. With one
cluster the region is text, not a menu. With a link, the region is a menu:
the name, and under it either "This cluster" or the other dashboard's host.
Choosing a link opens that cluster's dashboard. The create-app region control
works the same way. The app list is that cluster's apps, not
a merge. The cookie stays on that origin. No account there means that
cluster's login. This cluster does not provision the user.

The scheduler does not gain a region placement tag or a `PlacementRequest`
filter. It does not copy database bytes. Sirenia stays inside the cluster
that holds the volume.

## Workload placement

Workload placement is not a region. It does not add a Raft group, a DNS
zone, or an overlay. Install hosts and added hosts stay in this cluster.

`cluster_settings.workload_placement` is `mixed` or `split`. `mixed` is the
default and is today's scheduler: `flynn-role` is ignored. A cluster that
never runs the command stays `mixed`, including hosts that already have
other tags. Bootstrap, Vagrant, and CI do not set the mode and do not set
`flynn-role`.

```text
$ sudo flynn-host placement:mode
$ sudo flynn-host placement:mode split
$ sudo flynn-host placement mode mixed
```

`placement mode` is `placement:mode`. The body is `{"mode": mode}` only.
The cluster key is required to set it. `GET /cluster/placement` is any
authenticated caller. `split` is refused unless a host that is not shutting
down has `flynn-role=core`. Zero `flynn-role=app` hosts is allowed. The
command then says new user and tenant jobs stay pending until an app host
exists. Existing jobs are left running. Turning `split` on does not stop
jobs, delete volumes, or allocate new empty ones. A job whose volume
already exists stays on that host. Platform Postgres and other persistent
singletons stay put until a separate migrate. The scheduler logs the
mismatch.

Tag the install hosts when you init them, or later with `tags:set`:

```text
$ sudo flynn-host init --peer-ips 192.168.56.20,192.168.56.21,192.168.56.22 --role core
$ sudo flynn-host init --peer-ips 192.168.56.20,192.168.56.21,192.168.56.22 --role app
```

`--role` writes `flynn-role=core` or `flynn-role=app` next to any `--tag`
values. It is not `--tag role=database`. A database node is an app host
that also has the process tag the runtime already uses. A later init that
omits `--role` keeps the `flynn-role` already in `host.json`. `tags:set`
can change it. A replacement host can be `--role core`. Whoever you tag
`core` is an install host, not only the hosts from the first init.

While `split` is on, the scheduler applies the role only as a placement
filter. It does not write formation tags, and it does not stop jobs because
the mode or the role tag changed.

- Every host runs omni processes: discoverd, flannel, the controller
  scheduler, and the router. An added node still runs those. It cannot join
  the overlay without discoverd and flannel. The router keeps binding 80,
  443, and 3000–3500 on every host. DNS is unchanged.
- Core hosts (`flynn-role=core`) run the other control-plane processes:
  controller web and worker, the platform Postgres app named `postgres`,
  blobstore, gitreceive, tarreceive, logaggregator, taffy, status, and
  plugin apps (`flynn-plugin=true`), including postgres-plugin. The plugin
  API stays on the install hosts.
- App hosts (`flynn-role=app`) run user apps and tenant datastore apps
  (`flynn-system-app` plus `flynn-datastore`, or a name such as
  `redis-<uuid>` / `postgresql-<name>`). Those are not plugin apps and not
  the platform app named `postgres`.
- A host with no `flynn-role` runs omni processes only.
- Process tags and runtime tags still have to match. `flynn-max-processes`
  still counts tenant jobs only.

One-off jobs follow the app they belong to. `flynn run` and
`POST /apps/:id/jobs` pick a random host that is not shutting down and
that the same role filter allows, so a user app's one-off runs on an app
host and a control-plane one-off (a system app or plugin app) on a core
host. With no such host the request fails with `503` and the job is not
started; it is never sent to a host of the other role. In `mixed` the
controller still skips hosts that are shutting down. `slugbuilder` and
`dockerbuilder` run on the user app, so a build runs on an app host.
gitreceive stays on a core host.

Platform Postgres does not leave `SINGLETON` or scale to 3 because app
hosts were added. While `split` is on, that promotion counts core hosts.
One core host plus any number of app hosts does not promote it. Plugin
MySQL and MongoDB are unchanged.

`production:configure` does not set this mode. The dashboard Cluster
settings page shows it and the command. The dashboard token is not the
cluster key, and the page does not write the mode.

Raft voters stay the initial consensus set. A `core` tag does not make a
discoverd proxy into a voter, and an `app` tag does not remove one.
Replacing a voter stays the steps in [Replacing Hosts](#replacing-hosts).

## TLS / Let's Encrypt

Bootstrap uses a self-signed certificate. For production, install the Let's
Encrypt plugin and configure ACME on a cluster host so the dashboard,
controller, and app hostnames get trusted certificates:

```text
$ sudo flynn-host plugin:install letsencrypt
$ sudo flynn-host letsencrypt:configure --email=admin@example.com --agree-tos
$ sudo flynn-host letsencrypt:enable-system-routes
$ flynn letsencrypt:enable www.example.com
$ flynn cluster:refresh --clear
```

Do not pass `--insecure` to `flynn` or `git` for the controller. Until ACME
is on system routes, trust the pin + CA from `cluster:add` (`flynn cluster:ca`).
After `letsencrypt:enable-system-routes`, public CAs are enough.

`CLUSTER_DOMAIN` and a wildcard must resolve to the cluster (HTTP-01 on ports
80 and 443). See [Apps — HTTPS](apps.md#https).

## Storage

Flynn uses ZFS for database volumes. A trial install does not hand ZFS a disk.
The installer enables `flynn-host` and does not start it. The first
`systemctl start flynn-host`, or a reboot, creates a sparse file under
`/var/lib/flynn/volumes` and uses that file as the pool. That is enough to try
Flynn. It is not a production pool: the databases and the operating system
share one disk.

[`production:check`](#production-check) fails in either tenancy mode while any
leaf of this host's pool is a regular file. A mirror that still lists the file
fails until that file is detached. A missing pool fails in either mode. The
check does not attach, detach, or delete the file, and switching the cluster
to hosted mode does not change this line. Run the check on every host. A green
`zfs` line is that host only.

### Dedicated disk

Pass `--zpool-create-device` on the install command, before
`systemctl start flynn-host` or a reboot. Use a dedicated disk, not the OS
disk. `-f` overwrites an existing label on that device. The new pool is the
size of that disk.

```text
$ sudo bash /tmp/install-flynn --zpool-create-device /dev/sdb --zpool-create-options "-f"
```

The flag does not migrate an existing pool. If `flynn-default` already exists,
`zpool create` fails and the install stops.

### Moving an existing file pool

Move one host at a time, with `flynn-host` left running. Volumes on that host
stay there. Do not destroy the pool or its datasets, and do not stop the
daemon to do this.

The new disk must be at least as large as the file vdev, not the `ALLOC`
column from `zpool list`. The default file is 70% of the total blocks of the
filesystem that holds `/var/lib/flynn/volumes`, or `100000000000` bytes when
that size cannot be read. That is not 70% of free space. A smaller disk cannot
attach, and a failed attach leaves the file in the pool.

Attach the disk as a mirror of the file path printed by `production:check`
(`file=`). That path is often
`/var/lib/flynn/volumes/zfs/vdev/flynn-default-zpool.vdev` and may be another
path. Do not use `zpool add`. That stripes the disk into the pool, and detach
cannot remove the file afterward.

```text
# Attach /dev/sdb1 (your disk) to the file path the check printed
$ sudo zpool attach flynn-default /var/lib/flynn/volumes/zfs/vdev/flynn-default-zpool.vdev /dev/sdb1

# Wait until the copy is finished. state: ONLINE while scan says the
# resilver is still running is not finished. Detach then drops the only
# complete copy.
$ sudo zpool status -P flynn-default
  pool: flynn-default
 state: ONLINE
  scan: resilvered 59.7M in 0h0m with 0 errors on Mon Nov  2 04:02:58 2015
config:

	NAME                                                          STATE     READ WRITE CKSUM
	flynn-default                                                 ONLINE       0     0     0
	  mirror-0                                                    ONLINE       0     0     0
	    /var/lib/flynn/volumes/zfs/vdev/flynn-default-zpool.vdev  ONLINE       0     0     0
	    /dev/sdb1                                                 ONLINE       0     0     0

errors: No known data errors

# Detach only that file path, then delete it. rm before detach faults the pool.
$ sudo zpool detach flynn-default /var/lib/flynn/volumes/zfs/vdev/flynn-default-zpool.vdev
$ sudo rm /var/lib/flynn/volumes/zfs/vdev/flynn-default-zpool.vdev
```

Wait until `scan: resilvered` and the new disk is not marked resilvering.
After a successful detach, a leftover file on disk is not a failure. Rerun
`production:check` on that host.

### Image cache

Job layers live on the host root filesystem (`/var/lib/flynn/layer-cache` and
per-job image dirs), not in the ZFS pool. flynn-host deletes unused files there
on a timer and when free space is low. That does not replace a dedicated ZFS
pool, and leftover datasets still need `flynn-host volume:gc` or a cluster
update (which runs the same GC before pulling images).

A file-backed zpool (the default sparse vdev) often keeps allocated extents
after ZFS frees them, so `du` of the vdev stays large while `zpool list` ALLOC
is small. `sudo flynn-host disk:reclaim` runs volume GC, image-cache cleanup,
and `zpool trim` so those holes return to the host root filesystem. It does not
delete Postgres or other volumes the scheduler still tracks. TRIM is skipped
when the vdev does not support it.

## Blobstore Backend

Flynn stores binary blobs like compiled applications, git repo archives,
buildpack caches, and Docker image layers using the blobstore component. The
blobstore supports multiple backends: Postgres, [Amazon
S3](https://aws.amazon.com/s3/), [Google Cloud
Storage](https://cloud.google.com/storage/), and [Microsoft Azure
Storage](https://azure.microsoft.com/en-us/services/storage/blobs/).

By default, the blobstore uses the built-in Postgres appliance to store these
blobs. This works well for light workloads and is the default configuration
because it allows Flynn to be deployed anywhere without any external service
dependencies.

We recommend using one of the external blobstore backends listed below for
production, as they are more reliable than the Postgres backend and have
predictable performance without consuming disk space on the Flynn hosts.

[`production:check`](#production-check) fails a hosted cluster, and warns on
a self-hosted cluster, when the default backend is Postgres. That is an empty
`DEFAULT_BACKEND`, the name `postgres` (any case), or a parsed `backend` type
that is missing or `postgres` (any case). The check prints the default backend name only,
never `BACKEND_*` values, and does not run the migrate. A green line means the
current blobstore release's default has a parsed backend type other than
`postgres`. It is not proof the bytes left Postgres.

### Amazon S3

To migrate to the S3 backend, you first need to provision a new bucket and
create credentials for it. In AWS IAM, create a user with access credentials and
add a policy that looks like this:

```text
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": [
                "s3:DeleteObject",
                "s3:GetObject",
                "s3:ListBucket",
                "s3:PutObject",
                "s3:ListMultipartUploadParts",
                "s3:AbortMultipartUpload",
                "s3:ListBucketMultipartUploads"
            ],
            "Resource": [
                "arn:aws:s3:::flynnblobstore",
                "arn:aws:s3:::flynnblobstore/*"
            ]
        }
    ]
}
```

Don't forget to save the credentials when creating the user and set the bucket
name in the `Resource` section before adding the policy.

After creating the S3 bucket and credentials, configure the blobstore to use it as
the backend with bucket, region, and access credentials:

```text
sudo flynn-host blobstore:set --backend=s3 --bucket=flynnblobstore --region=us-east-1 \
  --access-key-id=$AWS_ACCESS_KEY_ID --secret-access-key=$AWS_SECRET_ACCESS_KEY \
  --migrate --delete
```

`blobstore:set` writes the same `BACKEND_<name>` and `DEFAULT_BACKEND` env the
blobstore process already reads (`s3main` is the default `--name` for S3). Inspect
the current backend with `sudo flynn-host blobstore:status` (secrets are redacted).
Rotate keys later with `sudo flynn-host blobstore:credentials --access-key-id=… --secret-access-key=…`.

If the credentials are invalid, the command will fail, and you can check the
logs with `flynn -a blobstore log`.

`--migrate --delete` copies existing objects onto the new backend and removes them
from Postgres. You can also run that step separately:

```text
sudo flynn-host blobstore:migrate --delete
```

The migrate job exits non-zero when it cannot open the backends or list the files,
and when any file fails to move. With `--delete`, a file is removed from the old
backend only after its move is committed, so a failed run never loses data and can
be re-run.

Overwriting an object (gitreceive replaces `/repos/<app>.tar` on each push, and
builds replace their cache tarballs) follows the same rule on S3, GCS, Azure, and
MinIO: the new upload goes to a new backend object, and the replaced object is
deleted only after the new one is committed. A failed or interrupted overwrite
leaves the old content readable. If that delete fails, the replaced object stays
in the bucket; `flynn -a blobstore run /bin/flynn-blobstore cleanup` removes
such leftovers.

A `DELETE` works the same way: the objects are marked deleted first, then
removed from the bucket. When a bucket delete fails, or the objects' backend is
no longer configured, the request returns an error, the objects are no longer
served, and they stay listed for `cleanup`. `cleanup` forgets an object once it
is gone from the bucket (one already missing counts as gone), and exits
non-zero with a count when any object could not be deleted, including objects
on a backend this release has no credentials for. Re-run it after fixing the
cause.

The equivalent `flynn -a blobstore env:set` / `flynn -a blobstore run /bin/flynn-blobstore migrate --delete` path still works.

On Flynn older than v20160924.0 the migrate binary was `/bin/flynn-blobstore-migrate`.

### MinIO and other S3-compatible stores

Any S3-compatible endpoint (MinIO, Garage, Ceph RGW, …) uses `--backend=minio`:

```text
sudo flynn-host blobstore:set --backend=minio --bucket=flynnblobstore \
  --endpoint=minio.example.com:9000 --access-key-id=$MINIO_ACCESS_KEY \
  --secret-access-key=$MINIO_SECRET_KEY --migrate --delete
```

Pass `--insecure` when the endpoint is HTTP. `flynn-host blobstore:status` and
`blobstore:credentials` work the same as for Amazon S3.

### Google Cloud Storage

To migrate to the Google Cloud Storage backend, you first need to create a new
[Service Account in
IAM](https://console.cloud.google.com/iam-admin/serviceaccounts). The account
does not need any roles, but make sure you create a private key and download the
JSON version. After creating the account, create a Cloud Storage bucket and add
the service account ID as a user with Owner access to the bucket permissions.

After creating the Cloud Storage bucket and credentials, configure the blobstore
to use it as the backend, with the bucket name and key file:

```text
flynn -a blobstore env:set BACKEND_GCSMAIN="backend=gcs bucket=flynnblobstore" \
BACKEND_GCSMAIN_KEY="$(cat Project-7633a787c43f.json)"

flynn -a blobstore env:set DEFAULT_BACKEND=gcsmain
```

If the credentials are invalid, the first command will fail, and you can check the
logs with `flynn -a blobstore log`.

Finally, migrate the existing blobs from Postgres to Cloud Storage and
remove them from Postgres:

```text
sudo flynn-host blobstore:migrate --delete
```

### Microsoft Azure Storage

To migrate to the Azure Storage backend, you first need to create a storage
account and container. After setting those up, you should have an account name,
account key, and container name, which can be used to configure the backend:

```text
flynn -a blobstore env:set BACKEND_AZUREMAIN="backend=azure account_key=xxx \
account_name=yyy container=flynnblobstore"

flynn -a blobstore env:set DEFAULT_BACKEND=azuremain
```

If the credentials are invalid, the first command will fail, and you can check the
logs with `flynn -a blobstore log`.

Finally, migrate the existing blobs from Postgres to Azure Storage and
remove them from Postgres:

```text
sudo flynn-host blobstore:migrate --delete
```


## Upload limits

Pushes, Docker image layers and blobstore objects have size limits, and
tarreceive converts only a few layers at once. They stop one build token or
push from filling the gitreceive, tarreceive or blobstore disk (by default the
blobstore is the system Postgres volume) or pinning every CPU with parallel
`mksquashfs` runs. The defaults are generous so large images and repositories
still deploy. Lower them to fit your disks, or raise them for very large
images.

| Setting | App | Default | Limits |
| --- | --- | --- | --- |
| `MAX_PUSH_SIZE` | `gitreceive` | `8g` | The data one `git push` sends (the pack). |
| `MAX_LAYER_SIZE` | `tarreceive` | `32g` | The layer tar one upload sends. `flynn docker:push` sends the whole image as one uncompressed tar, so this is the largest image you can push. |
| `MAX_CONCURRENT_LAYERS` | `tarreceive` | `2` | Layers that unpack and run `mksquashfs` at the same time. Others wait for a slot. |
| `MAX_OBJECT_SIZE` | `blobstore` | `32g` | One stored object: a slug, a squashfs layer, a build cache, a repo archive. An append counts the bytes already stored. |
| `APP_QUOTA` | `blobstore` | `100g` | The live bytes stored for one app: its `/slugs/apps/<app>/` and `/tarreceive/apps/<app>/` objects, its build caches and its repo archive. Shared Docker layers under `/tarreceive/layers/` are not counted. |
| `UPLOAD_IDLE_TIMEOUT` | `blobstore` | `60s` | How long a PUT body may send nothing. A stalled upload is then cut off with `408 Request Timeout`, nothing is stored, and an overwritten object keeps its old content. A duration (`90s`, `5m`) or seconds; `0` or `off` turns it off. |

Sizes are bytes or a number with a binary unit (`512m`, `32g`, `1t`). `0`
or `off` turns a limit off. Set them on the system app; the new release
restarts it and `flynn-host update` keeps the value:

```text
flynn -a gitreceive env:set MAX_PUSH_SIZE=4g
flynn -a tarreceive env:set MAX_LAYER_SIZE=16g MAX_CONCURRENT_LAYERS=1
flynn -a blobstore env:set MAX_OBJECT_SIZE=16g APP_QUOTA=50g UPLOAD_IDLE_TIMEOUT=2m
```

Each component logs its limits when it starts. An upload over a limit is
refused as soon as its declared length is known, before anything is written,
and otherwise when the limit is passed:

- `git push` fails with `error: RPC failed; HTTP 413`, the branch does not
  move and the gitreceive log names the limit.
- `flynn docker:push` fails with `layer exceeds the tarreceive limit ...`.
- A build whose slug is over `MAX_OBJECT_SIZE` fails with
  `blobstore refused the slug (413 ...)`, and one over `APP_QUOTA` with
  `507 Insufficient Storage`; a refused overwrite leaves the old object in
  place.

The app quota counts only finished uploads, so uploads running at the same
time can pass it by up to one object each.

A blobstore upload holds no database transaction or row lock while its body
arrives: the data goes to a new object first, and a short transaction swaps it
in at the end. A slow or stalled uploader therefore neither blocks the next
push's upload of the same cache or slug nor uses up the blobstore's database
connections. The blobstore server also limits request headers to 30 seconds
and closes idle keep-alive connections after 2 minutes, and it deletes the
object of an upload that made no progress for an hour (for example when its
instance stopped mid-upload). Blob GC (`gc.max_inactive_slug_releases`,
default 10 releases kept) frees an app's old slugs.

## DNS and Load Balancing

Flynn has a built-in router that handles all incoming HTTP, HTTPS and TCP
traffic. It is lightweight and runs on every Flynn host, this allows easy
deployment of Flynn without requiring another load balancer in front of Flynn.

We recommend running Flynn with low-TTL round-robin DNS A records pointing at
each host. For automatic failover, health checks should be configured that
automatically remove a host's A record if it is unhealthy.

Alternatively, a TCP load balancer for ports 80 and 443 can be configured in
front of the Flynn router, this may increase overhead and complexity but can
make sense in some environments.

Each router process caps in-flight HTTP requests at 512 per app. A 3-host
cluster is three separate caps of 512, not one cap of 1536 and not a single
router enforcing 1536. Idle keep-alives, TCP ports, and file descriptors stay
uncapped. See [Apps — Routes](apps.md#routes). The router rolls one host at a
time (`one-down-one-up`); during that roll only the updated hosts enforce the
cap.

## Firewalling

A firewall preventing external access must always be configured on or in front
of Flynn hosts. Open these to the internet:

* 80 (HTTP)
* 443 (HTTPS)

Do not open `3000–3500` or `10000–29999` as a range. Each live TCP route is one
public listener and one UFW allow from Anywhere, on every host. `flynn-host`
opens and closes that port from the live route list. `FLYNN_INSTANCE_PORT`
(`3000–3500`) is a separate allocator and is opened only on the hosts running
that instance.

Internal cross-host cluster communication happens on a variety of UDP and TCP
ports and should not be restricted.

The installer owns public 22/80/443 and private cluster CIDRs. After install,
`flynn-host firewall` shows managed UFW rules. Use it to allow a peer that is
not in a private CIDR, or to pin an extra TCP port that is not a route. User
and build jobs are separately blocked from those host ports and from discoverd
by overlay iptables, even though UFW allows the cluster CIDR for host-to-host
traffic.

```text
sudo flynn-host firewall
sudo flynn-host firewall:peer:add <ip>
sudo flynn-host firewall:expose 12000
sudo flynn-host firewall:sync
```

`firewall:peer:remove` and `firewall:unexpose` drop those extra allows.
`--peer-ips` and `--ports` on `firewall:sync` seed extra allows stored for later
syncs. `firewall:sync` does not read the route list. The host daemon does, every
15 seconds.

### Job egress deny list

User, build and datastore jobs cannot open connections to `169.254.0.0/16`
(link-local). That range holds the cloud instance metadata service
(`169.254.169.254` on AWS, GCP, Azure, and most other providers), which hands
out the node's instance credentials and user-data. Datastore engines are
covered because tenants can make them dial an address (ClickHouse `URL`,
`S3` and `MySQL` table engines, database extensions). System jobs are not
affected, so cluster backups, plugin backup and dump tasks (system-class
one-off jobs) and the blobstore keep reaching an S3-compatible endpoint
anywhere, including inside a listed range. Engines themselves need nothing
outside the cluster.

To deny more ranges to user, build and datastore jobs, set
`FLYNN_JOB_EGRESS_DENY` in the
`env` of `/etc/flynn/host.json` on every host to a comma-separated list of IPv4
CIDRs (a bare IP means `/32`), then restart `flynn-host`:

```json
"env": { "FLYNN_JOB_EGRESS_DENY": "168.63.129.16,10.20.0.0/16" }
```

For example, add `168.63.129.16` (the Azure platform endpoint) or a private
range with services tenants must not reach. `169.254.0.0/16` is always
denied, even when the list is set. An invalid entry stops `flynn-host` from
starting job networking, so a typo cannot leave a range open. Do not list
the overlay network: it would cut user jobs off from datastores and engines
off from their peers. A range that holds the cluster nodes is fine: for
datastore engines, node IPs stay under the job isolation rules (discoverd
open, every other port dropped). There is no per-class allow list; to let
jobs reach a range, leave it off `FLYNN_JOB_EGRESS_DENY`. On restart,
`flynn-host` removes rules for ranges you took off the list. The rules are
`FORWARD` DROPs tagged `flynn-egress-deny` (`sudo iptables -S FORWARD | grep
flynn-egress-deny`).

### Exporting datastores

HTTP apps get a stable hostname on the cluster domain. `addons:expose` assigns
the lowest free TCP port in `10000–29999` (or `-p` inside that window):

```text
flynn -a myapp addons:expose postgres
# The cluster firewall will open TCP 10000.
```

`addons:expose` creates a leader TCP route (default hostname
`<service>.<cluster-domain>`, for example `postgres.example.com`) with TLS
passthrough so Postgres/MySQL native SSL still works. `--auto-tls` switches the
route to TLS terminate and attaches a Let's Encrypt cert (HTTP-01 still uses
ports 80/443). See [Databases](databases.html.md) and each engine page.

The host daemon opens that one port on every host (`flynn-expose` in UFW). It
does not open the rest of `10000–29999`, and it does not open the router
pre-bind `3000–3500` unless a live route still uses one of those ports. Ports
outside the pre-bind listen when the route exists. Existing routes in
`3000–3500` keep their ports. `firewall:expose` pins a port that is not a
route so it stays open if the controller is unreachable. After
`addons:unexpose`, the daemon closes the route port. Run
`sudo flynn-host firewall:unexpose PORT` only if you pinned it.

Treat an exported datastore port as public. Prefer TLS and a VPN when you can.
In-cluster apps keep using the `*.discoverd` names in `DATABASE_URL` /
`REDIS_URL` / etc.; they do not need a TCP route.

Outbound Internet access is required to deploy apps using many of the default
buildpacks.

## Automation

Installation and management of Flynn clusters can be automated using a variety
of existing tools. We recommend using tools that you are familiar with to create
a repeatable system for deploying Flynn clusters. This will allow you to quickly
start new clusters for testing, updates, and failure remediation.

When possible, we recommend deploying immutable infrastructure and deploying new
clusters for updates and major configuration changes.

## Backups

Flynn supports full-cluster backup/restore as well as export/import of
individual applications (including their databases).

### Cluster Backup

To take a full-cluster backup, run `flynn-host backup --file backup.tar`.
The tarball holds the cluster root secrets (`flynn.json` release env and the
Postgres dump), so `flynn-host backup --file` and `flynn cluster backup --file`
write it owner-only (mode 0600, tar entries 0600). The file is written to a
temp file beside the destination, synced, and renamed into place, so a failed
backup never replaces an earlier one. Keep it off shared storage.
A file named `backup.tar` is created with the data needed to stand up a new
cluster: `flynn.json` (discoverd/flannel/postgres/controller, plus plugin
apps that were included), `plugins.json` (which plugins were installed), and
a full `pg_dumpall` of the **platform** Postgres appliance (controller,
blobstore files including plugin image layers, and plugin metadata databases
attached to `platform-postgres`). An installed plugin record that still has
a `cluster_backup` block is dumped into the tarball. `MYSQL_PWD` and
`MONGO_PWD` do not create that block. Those appliance archives are not tenant
data. Tenant volumes are not in the tarball. Tenant Postgres, MongoDB, Kafka, and
ClickHouse dumps are objects under
`backups/<owner>/<resource>/<timestamp>/<segment>` and are not applied by
cluster restore. Kafka and ClickHouse volumes are not in the tarball either.
`flynn-host backup` prints that tenant dumps under `backups/` are not applied
by cluster restore. [`production:check`](#production-check) warns when there
is no Postgres primary and no MongoDB, Kafka, or ClickHouse resource, and fails in both hosted and
self-hosted mode when a counted resource's last success is missing or
strictly older than 24 hours plus one hour. A postgres blobstore (`DEFAULT_BACKEND` empty or type `postgres`) is
refused for those dumps in both tenancy modes; run
`flynn-host blobstore:set --migrate --delete` first. That refusal does not
change the blobstore line above (hosted `fail`, self-hosted `warn`). If the current postgres release has no formation row
yet (seen after an updater deploy), backup copies process counts from
another scaled formation on that app instead of failing. Restore does
**not** re-run `flynn-host plugin:install`; plugin apps come back with
postgres. Redis and MySQL tenant data, and every database volume, stay
out of the tarball, so those volumes come back empty. Kafka and ClickHouse
volumes come back empty too. Restore a tenant Postgres, MongoDB, Kafka, or ClickHouse resource
from its blobstore timestamp after the cluster is up. Those objects are not
applied by cluster restore. App slugs and container images stored in the blobstore Postgres
backend are restored from that dump. If blobstore was switched to S3,
MinIO, GCS, or Azure (`flynn-host blobstore:set` / `DEFAULT_BACKEND`),
object bytes stay in that bucket; the backup still includes controller env
so restore reconnects to the same backend. Keep the bucket (and
credentials) when you restore. Leftover blobstore large objects stay in
that platform dump. A green `production:check` blobstore line does not
prove they were removed.

The Vagrant upgrade smoke (`script/vagrant/suite.sh`) exercises this
path after the in-place `--force` updates: backup, `install --clean`, then
`flynn-host bootstrap --from-backup`.

### Cluster Restore

To restore from a full-cluster backup, follow [the manual installation
instructions](installation/manual.md) and modify the `flynn-host bootstrap`
command to include an extra flag: `--from-backup backup.tar`. The cluster size
does not need to be the same, but the `--min-hosts` flag and cluster discovery
flag should be specified. The `CLUSTER_DOMAIN` variable is ignored, the domain
of the previous cluster will be used.

Restoring a singleton backup onto `--min-hosts 3` keeps the backup's
`SINGLETON` flag and process counts so the platform Postgres appliance can
elect a leader during wait/dump. After the controller is up, the scheduler
promotes that appliance to HA (new `SINGLETON=false` release at scale 1, then
data process scale 3) once three hosts are active. Restoring onto a single
host still forces singleton scale. Growing a live singleton cluster to three
hosts uses the same path for platform Postgres. Only releases that already
have `SINGLETON=false` are scaled to 3.

MySQL instances (`mysql-<word>-<digits>`) do not set `SINGLETON`, so they stay
at one process. MongoDB, Kafka, and ClickHouse resource apps are not promoted
by that scheduler path. `flynn-host mongodb:availability system` (and the Kafka
and ClickHouse commands) grows each resource one member at a time, up to three
when three hosts are registered. `manual` leaves the formation alone. Hosted
and self-hosted clusters use the same path.

### App Export

To export a single app, run `flynn -a APPNAME apps:export --file app.tar`. A file
named `app.tar` will be created with the app configuration and image, along with
a copy of all data stored in associated databases. The app export can be
restored to the same cluster under a different name, or a different Flynn
cluster. The export holds the release env (app secrets) and the Postgres and
MySQL dumps, so `--file` writes it owner-only (mode 0600, tar entries 0600)
the same way as a cluster backup: to a temp file beside the destination,
synced, and renamed into place. A failed export, including a full disk on the
final flush, exits non-zero and never replaces an earlier export. Without
`--file` the tar goes to stdout and its mode is whatever the redirect creates.

Slug and image layers are copied by a one-off on the blobstore app, using the
blobstore release. That job is a system job, so it can resolve
`blobstore.discoverd`. It authenticates with the release `AUTH_KEY`. The
blobstore image is busybox plus `flynn-blobstore`; it does not contain curl.

### App Import

To import an app, run `flynn apps:import --file app.tar`. This will create a new app
on the cluster, import the configuration, create databases, and import the data
from the exported app. Layer uploads use the same blobstore-app one-off as export. Layer uploads use the same blobstore-app one-off as export. If you'd like to provide a new name for the app, the
`--name` flag may be specified. By default a new route is created based on the
app name and the cluster domain. To import the old routes in addition to the new
route, add the `--routes` flag.

## SSH Client Key

Some apps require a SSH private key to download dependencies or submodules while
deploying. Flynn supports configuring a single SSH client key for the platform
that will be used whenever SSH is used during builds triggered by `git push`.

To configure the keypair, set the `SSH_CLIENT_KEY` and `SSH_CLIENT_HOSTS`
environment variables on the built-in `gitreceive` app:

```text
flynn -a gitreceive env:set SSH_CLIENT_HOSTS="$(ssh-keyscan -H github.com)"\
   SSH_CLIENT_KEY="$(cat ~/.ssh/id_rsa)"
```

The key is opt-in per app, because buildpack code from every tenant runs in the
build. Set `SSH_CLIENT_KEY_APPS` on `gitreceive` to `*` (single-tenant cluster:
every app) or to a comma-separated list of app names or IDs:

```text
flynn -a gitreceive env:set SSH_CLIENT_KEY_APPS="billing-web,billing-worker"
```

With `SSH_CLIENT_KEY_APPS` unset, no app receives the key, gitreceive does
not write it to disk, and `ssh` submodule URLs are refused. For an opted-in
app, gitreceive fetches `ssh` submodules with the key (strict host key
checking against `SSH_CLIENT_HOSTS`, public addresses only; see
[Apps — Git submodules](apps.md#git-submodules)). For an opted-in app
the slugbuilder stages the key in a root-only file (mode 0600), loads it into
an `ssh-agent` and deletes the file before any buildpack runs. Buildpack code
gets `SSH_AUTH_SOCK` and can authenticate with the key while the build runs, but
cannot read the key bytes (they are not in the environment or on disk), and the
agent is stopped when compilation ends. `SSH_CLIENT_HOSTS` is forwarded on the
same terms. A buildpack that opted-in apps run can still use the key to
authenticate to any host while the build runs, so opt in only apps you trust.

## Monitoring

Flynn provides a status endpoint over HTTP that exposes the health of system
components at `http://status.$CLUSTER_DOMAIN` (for example,
`https://status.1.localflynn.com`). The status endpoint returns a status code
along with a more detailed JSON response. If any core components are unhealthy,
the HTTP status will be 500.

Requests to the status endpoint require a `key` parameter (example:
`https://status.1.localflynn.com/?key=$AUTH_KEY`) when they come from IP
addresses that are not reserved for private use. Forwarded headers
(`X-Forwarded-For`, `X-Real-IP`) are ignored unless the TCP peer itself
is already a private address (Flynn's router). A public client cannot
skip the key by spoofing a private forwarded IP.

The `$AUTH_KEY` may be retrieved with this command:

```text
flynn -a status env:get AUTH_KEY
```

### OpenTelemetry (Grafana, Alloy, collector)

OpenTelemetry metrics are an **optional plugin**. Clusters that do not install
it do not export OTLP. After `flynn-host plugin:install otel`,
`flynn-host otel` forwards **host metrics** (CPU, memory, disk, load, job
counts) to any OTLP/HTTP endpoint (`http://host:4318`, Grafana Alloy, the
OpenTelemetry Collector, Grafana Cloud OTLP). Path `/v1/metrics` is appended.
The plugin `/exporters` API requires the cluster key (HTTP Basic with an empty
username, or Bearer). `flynn-host otel` sends it automatically
(`CONTROLLER_KEY` / `AUTH_KEY`, else the controller discoverd `AUTH_KEY`).
Collector `--auth bearer` / `--auth basic` on `otel:add` are OTLP exporter
headers, not this Flynn API.

```text
sudo flynn-host plugin:install otel

# Metrics to a local collector
sudo flynn-host otel:add http://alloy.example:4318

# Grafana Cloud (or any collector that needs a header)
sudo flynn-host otel:add --header "Authorization: Bearer TOKEN" https://otlp.grafana.net/otlp
sudo flynn-host otel:add --insecure https://collector.example:4318

sudo flynn-host otel
sudo flynn-host otel:remove <id>
```

Job logs stay on syslog. The in-cluster aggregator keeps the newest 10,000
lines per app id in each aggregator process. That ring is a live tail.
It is dropped when the app is deleted, or after `LOG_BUFFER_TTL` (default
`168h`, set with `flynn -a logaggregator env:set LOG_BUFFER_TTL=72h`) with
no new line and no follower.
`flynn log` reads it. It is not a cluster total and it is not retention.
Each host also writes `/var/log/flynn/<app-id>.log`, rotating at 100MiB
with one backup, about 200MB per app on that host. Those files are not a
cluster total either. A hosted cluster and a large self-hosted cluster
both need an operator syslog sink for retention. `tenancy_mode` does not
change the path that copies stdout.

`flynn log-sink` is **per app**. `flynn-host log-sink` is cluster-wide and
accepts `--scope` / `--app` filters. `production:check` counts only
cluster sinks: `syslog+tls`, format `rfc6587`, no prefix, covering system
and app logs. `syslog://`, `--insecure`, `newline`, `prefixed_newline`,
and a prefix template do not. A prefix is a Go template of the job and can
print environment variables and secrets. Stdout and stderr are copied as
the process wrote them.

```text
sudo flynn-host log-sink:add syslog syslog+tls://logs.example:6514/
flynn -a myapp log-sink:add syslog syslog+tls://logs.example:6514/
```

A slow sink drops lines. It does not stall the process. `GET /host/status`
includes `log_drops` for that host process: sink-buffer drops plus on-disk
queue drops. Follower timeouts are not included. The count resets when
`flynn-host` restarts. Do not tail the aggregator's `GET /log`,
`GET /snapshot`, or TCP port 514. Those listeners are unauthenticated, and
any app name selects a ring.

The dashboard still shows live metrics for operators who are already logged in.
`flynn-host metrics` prints a live host snapshot without the dashboard.
`flynn-host alert` stores cluster rules (CPU, memory, disk, load, running jobs)
in the dashboard plugin; `flynn alert` and `flynn metrics` are the app-scoped
equivalents. The dashboard Alerts tab logs each firing window (newest first) and
shades those periods on the matching metric charts.

## Debugging

Flynn is a self-hosting system, this allows you to use the `flynn` and
`flynn-host` tools to introspect, configure, and debug the system. All
components with the exception of the `flynn-host` daemon appear as and can be
managed as apps in Flynn.

### Retrieving logs

Logs can be retrieved using several methods.

The `flynn -a $APP_NAME log` command will retrieve up to the last 10,000 lines
per app id from each aggregator process, and can also follow the stream as new
log lines are emitted. That ring is a live tail. It is not a cluster total.

When a slugbuilder, dockerbuilder, release, or postdeploy job stops, the
controller stores that job's stdout and stderr in Postgres, gzipped, keeping
the last 4 MiB. The git-push transcript (release creation, the release
command, postdeploy, and the deployed line) is stored the same way on the
release. Any controller node reads those rows, and the dashboard shows them
for that deployment after the in-memory 10,000-line log has moved on. Deploys
from before this was recorded still come from the live log, which only has
recent lines. Host files under `/var/log/flynn` remain the on-disk log for
every job.

The `flynn-host log $JOB_ID` command on a server will retrieve the logs for
a specific job. Pass an app name instead (`flynn-host log dashboard`) to get
logs from every job of that app; `-a` includes jobs that are no longer
running. `--follow` streams new lines from running jobs. A list of all jobs,
including those that are no longer running can be retrieved with the `flynn-host
ps -a` command.

Flynn stores logs from jobs in `/var/log/flynn` on each host. There is one log
file for jobs from each app that has been run on the host. All logs from jobs
tagged with the same app ID will go into a file named
`/var/log/flynn/$APP_ID.log`. When a log file reaches 100MiB, it is
rotated and a new file is created. One previous rotated log is kept, for a
maximum of about 200MB of logs per app per host. That is not a cluster total.
Lines dropped because the sink buffer or this file's queue was full are
counted in that host's `GET /host/status` field `log_drops`.

systemd manages the `flynn-host` daemon. Logs are in `/var/log/flynn/flynn-host.log`
(also `journalctl -u flynn-host`).

If the daemon looks stuck (for example a job stays `starting`), run
`sudo pkill -USR2 -x flynn-host` before restarting it. The daemon keeps running
and writes every goroutine's stack to
`/var/log/flynn/flynn-host-goroutines-<time>.txt` (owner-only), and
`flynn-host.log` names the file. Sending `SIGUSR2` to a job's `containerinit` writes the same dump into that
job's log as init lines.

The `flynn-host collect-debug-info` command will collect information about the
system it is run on along with recent logs from all apps and the `flynn-host`
daemon. By default it writes them to an owner-only (0600) tarball on that
host (`--filename` picks the path) and sends nothing over the network. Before
writing, it replaces cluster secrets from `/etc/flynn/host.json`, secret values
from every job's environment, and secret-looking text (`KEY=value` pairs with
secret names, URL passwords, `Authorization` headers, password flags such as
`redis-cli -a`) with `[REDACTED]`. Redaction is best effort, so read the
archive before you share it. `--upload` sends the redacted files to a secret
[GitHub Gist](https://gist.github.com) instead; it needs a personal access
token (`--github-token`, saved to `~/.flynn/github_token`) and lists the files
and destination before sending.

### Internal Databases

The `controller`, `router`, and `blobstore` components store data in a
PostgreSQL cluster managed by Flynn.

`flynn pg:psql` is the postgres plugin, for an app's own database. It is not a
public console, and it is not how you open the platform database. The platform
database is `flynn-host pg:psql` (and `pg:dump` / `pg:restore`) on a cluster
host. That host command runs `psql` as the appliance superuser, starting in
the controller database; `\l` lists every platform database and `\c <name>`
switches to one.

* **Cluster operators** who registered with `flynn cluster:add` / `flynn-host
  cli-add-command` have the controller key. That key is cluster-admin (treat it
  like root). They open the platform database with `flynn-host pg:psql` on a
  cluster host. Do not put that key in application config or share it with
  dashboard-only users.
* **Dashboard users** (`flynn login`) only act on apps they were granted. The
  Team role (View=`app:read`, Deploy=`app:deploy`, Manage=`app:write`,
  Admin=`app:admin`) is what the controller enforces
  on every CLI command. They can `flynn pg:psql` their own app's database, once
  the postgres plugin is installed, when the role includes write-level access.
  They cannot open a console on `controller`, `blobstore`, `postgres`, or other
  system apps. Those consoles are `flynn-host pg`, which uses the cluster key.
* **Application jobs** cannot reach `postgres-api`, `controller`, or
  `blobstore` on the overlay. They may resolve and TCP to the **leader**
  host Flynn put in that app's `DATABASE_URL` / `REDIS_URL` (an isolated
  instance such as `leader.redis-lagoon-59415.discoverd`), not another
  app's datastore and not internal names like `postgres.discoverd`. Each
  Postgres role can CONNECT only to its own database; `PUBLIC` CONNECT is
  revoked, so one app's user cannot open another app's (or the
  controller's) database.

User-app consoles: `flynn -a myapp pg:psql` from the postgres plugin. Platform
database: `flynn-host pg:psql` on a cluster host.

## Updating

There are two ways to update Flynn: in-place and backup/restore. The in-place
updater is new and we do not consider it to be stable. The most reliable
update method is backup/restore.

### Backup/Restore

The backup/restore update method involves taking a full backup of the cluster
and restoring it to a new cluster with the new version of Flynn.

1. Take a backup of the cluster with `flynn-host backup --file backup.tar`.
2. Install the new version of Flynn on a new cluster by following [the manual
   installation instructions](installation/manual.md) up to but not including
   the bootstrap step.
3. Run the `flynn-host bootstrap` command from the installation guide with an
   added flag pointing at the cluster backup file: `flynn-host bootstrap
   --from-backup backup.tar`
4. Update the DNS records that point at the old cluster to point at the new
   cluster.

### In-place update

The in-place updater is new and could cause cluster failure. We recommend taking
a full backup of the cluster first with `flynn-host backup`. There is almost
zero downtime during the cluster update, however database clusters may be
unavailable for a few seconds while they are updated.

To perform an in-place update of **binaries on one host** (no other nodes, no container image rollout), run `flynn-host update`. A newer GitHub release always continues past the flynn-host re-exec (init, CLI, daemon restart, and—on a single-node cluster—image rollout) without `--force`. `--force` repeats an update when the host is already on that version. `flynn-host update --check` only reports whether a newer GitHub release exists; that lookup is cached in `~/.flynn/update-check-cache.json` for one hour (same file, TTL, and `FLYNN_UPDATE_CHECK_*` variables as `flynn update --check`). `--check --force` or `FLYNN_UPDATE_CHECK_TTL=0` refreshes the cache. Installing still fetches a live release.

To update **every host**—push new `flynn-host` binaries to all peers, pull image layers on each node, and deploy system apps—run `flynn-host update --all-nodes` (after taking a backup as recommended above). The router holds host ports 80/443, so the updater stops the old router before starting the replacement, one host at a time on a multi-node cluster (other nodes keep serving; a single-node cluster still has a brief HTTP/HTTPS blip). Redis appliances use the same one-down-one-up strategy so `/data` is reused. The persistent discoverd and flannel jobs are swapped onto the new image one host at a time; the updater retries the discoverd host list for about 30 seconds, and if it still fails (or returns no hosts) the update stops with an error instead of leaving those two apps on the old image or guessing a single-node controller strategy. Each app moves to a release that carries the new image, and the old release's formation is scaled to zero before the app is pointed at the new one, so an update that stops partway is resumed by the next `flynn-host update`. When an app is already on the new image, the updater still scales any other release of that app that has a nonzero formation down to zero (unless the current formation is itself empty) and stops that release's leftover jobs, so a rerun clears a formation an earlier failed update left active. Use `flynn-host update --skip-images` to roll binaries out everywhere without touching images. After system apps, user apps are left running unless you pass `--recycle-user-apps`: that deploys git/slug apps onto the new slugrunner and recycles docker/container-stack and `flynn docker:push` processes on their current image. Restarting `flynn-host` itself also leaves containers running (`KillMode=process`). Without the recycle flag, the next git push or dashboard deploy of a user app picks up the new slugrunner artifact. Apps created in the dashboard or with `flynn apps:create` that have never been deployed are skipped; a missing release on a required system app still fails the update. Missing cluster secrets (`AUTH_KEY` / `CONTROLLER_KEY`, `DISCOVERD_AUTH_KEY`, blobstore `ACCESS_TOKEN_KEY`) are copied onto system-app releases from controller/postgres/gitreceive so an upgrade does not require a manual `env:set`. Plugin apps get the same backfill on `flynn-host plugin:update`.

When the update will pull container images, it first garbage-collects unused volumes (the same keep rules as `flynn-host volume:gc`: running jobs, controller-tracked volumes, and system images) and leftover image cache, then requires at least 5 GiB free on each host. Persistent database volumes the scheduler still tracks are not deleted. If the controller volume list cannot be loaded (for example the cluster key is missing after discoverd stopped publishing `AUTH_KEY`), volume GC skips deletes rather than removing datasets the scheduler still tracks. If a host is still short of space after that, the update stops before changing binaries so the cluster is not left half-upgraded.

While platform postgres peers are replaced, the update logs a `deployment progress` line every 10 seconds with each peer's replication state. A replica copying from its upstream shows `pg_basebackup 45% (5.1 GB of 11.3 GB)`. Once it streams WAL it shows `streaming WAL, catching up 80%, 12.0 MB behind`, where the percent is measured against the largest lag seen during that wait, until `WAL caught up`. After a host reboot, a replica whose checkpoint is on a timeline that is not a child of the primary's exits immediately and takes a new basebackup instead of retrying that data directory. A replica whose upstream host disappears mid-copy (a node reboot during a rolling `reload`) gives up within about 30 seconds (libpq keepalives and `tcp_user_timeout`), and a copy that reports no progress for 3 minutes is killed; once sirenia names a new upstream, a copy from the old one is canceled immediately and the replica reseeds from the new primary. A running replica that neither streams nor replays WAL for 45 seconds while its upstream answers as online (seen after a remaster, when the replica's local WAL ran past the new primary's timeline switch and postgres retried `waiting for WAL to become available` forever) is marked `standby_stalled` in its status, logs `standby made no replication progress while its upstream was online, re-seeding from upstream`, and takes a new basebackup from that upstream. A streaming replica, one still replaying local WAL, and one whose upstream is down or restarting are left alone. A new replica does not copy its basebackup from a stalled standby; it waits until that standby has reseeded. `pg:status` and the update's progress lines show such a peer as `stalled`. Outside an update (after `flynn-host restart`, a node reboot, or a replica reseed), run `flynn-host pg:status` for the same view, or `flynn-host pg:wait` to watch it until every replica has caught up. With the dashboard plugin installed, Cluster → System apps shows the same peers and percentages in a Platform Postgres card.

### Cross-app releases left by older controllers

Controllers before the CORE-18 fix let a caller with deploy or scale rights on
its own app point that app at any release id (`POST /apps/<app>/deploy` or
`PUT /apps/<app>/formations/<release>`). The app then ran the other app's image
with that app's env, and `GET /apps/<app>/release` returned the env. Current
controllers refuse new cross-app deploys and formations, but keep accepting an
app's current release, so rows made earlier stay until an operator clears them.

`flynn-host cross-app:check` lists them from `GET /cluster/cross-app-releases`
(cluster key or cluster admin). A `formation` row is a formation with at least
one process whose release belongs to another app. A `current_release` row is an
app whose current release belongs to another app. Formations scaled to zero and
deleted apps are not listed. System apps are hidden unless `--all` is passed
and never fail the check. The command exits 1 when a non-system row is found
and prints the commands that clear each row:

```text
# formation: scale the other app's release to zero on this app
flynn -a <app> scale --release <release> web=0
# current_release: deploy one of the app's own releases (flynn -a <app> release)
flynn -a <app> release:rollback -y <own-release-id>
```

Run these as a cluster admin: the tenant can no longer scale another app's
release unless it is the app's current release. If the app has no release of
its own, push a new build. Do not clear a `current_release` row with `env:set`,
`release:update`, or `release:add`: those copy the current (other app's) env
into a new release owned by the app. Treat the other app's env as exposed to
the app's owner and rotate its credentials. Run the check before `flynn-host
update --recycle-user-apps`, which redeploys a user app from its current
release the same way.

`flynn-host update --all-nodes` runs the same check after system apps are
deployed and prints a `WARNING` line naming how many apps are affected. It
does not fail the update, and it prints nothing against a controller that does
not have the route yet.

### Rollback after a bad upgrade

In-place rollback is the same updater pointed at an **older published GitHub
release** (or a saved tarball). It does not require a full reinstall.

```text
sudo flynn-host rollback --version vYYYYMMDD.N
# equivalent:
sudo flynn-host update --all-nodes --force --version vYYYYMMDD.N
```

`--version` must be a tag that still exists on GitHub Releases (or pass
`--tarball` with that release’s tarball). Rollback always uses `--force`
because the target is not newer than the running host. It defaults to
`--all-nodes` so the cluster does not sit on mixed Flynn versions; pass
`--this-host` to update only the machine you are on.

Take a `flynn-host backup` first when you can. After the platform is on the
older tag, run `sudo flynn-host plugin:update-all` so installed plugins
match that Flynn calver.

**What rollback restores:** `flynn-host` / `flynn-init` / CLI binaries and,
unless you pass `--skip-images`, system-app container images (controller,
router, blobstore, postgres appliance, …).

**What it does not undo:** user app releases and running user containers,
postgres/volume data, blobstore objects, dashboard users, or plugin
databases. User apps stay on the artifacts they already run until the next
deploy or restart (same as a forward cluster update).

Do not roll back past `v20161108.0` in-place; that still needs
backup/restore. Mixed host versions after a partial rollback are repaired by
running rollback again with `--all-nodes`.

## Adding Hosts

Hosts may be added to an existing cluster by running `flynn-host init` with the
discovery token or list of host IPs that was used to start the cluster. Export
the cluster's `FLYNN_HOST_AUTH_KEY` and `DISCOVERD_AUTH_KEY` (from an existing
host's `/etc/flynn/host.json`) before that init, so the new host has its key
before the daemon listens. A new host without a key and without a join token
accepts `POST /host/auth-key` only from itself. See
[Security](security.md). Those
hosts are still this cluster. To run user apps on the added hosts, tag the
install hosts `--role core`, run `flynn-host placement:mode split`, and join
the further hosts with `--role app`. See [Workload placement](#workload-placement).
A `core` tag does not make a discoverd proxy into a Raft voter.

Care should be taken to ensure that the same version of Flynn is installed on
all hosts. The installed version of Flynn can be checked with `flynn-host
version`, and the version to install can be specified by setting the `--version`
CLI flag to the desired version when running the install script.

# Replacing Hosts

If a member of the cluster that is participating in the consensus set becomes
permanently unavailable, it must be replaced in order to restore fault
tolerance. If you have already added additional hosts to your cluster beyond the
members of the consensus set, you can promote one of these peers to the
consensus set. Otherwise you will first need to start a new host and join it to
the cluster as described in the Adding Hosts section. You can check the Raft
consensus status of your cluster hosts by running `flynn-host list`. If the host
is a member its status will be displayed as `peer`. If not, it will be listed as
`proxy`.

The first step is to demote the old peer from the consensus set. If the peer is
already unavailable, use the `--force` flag. If you are preemptively replacing
a peer that is currently working, the force flag should not be used.

To demote the peer, run `flynn-host demote --force $PEER_IP`.

Once the old peer is removed you can add the new peer to the consensus set with
`flynn-host promote $PEER_IP`. This may take a short time as the new peer
replicates data from the Raft leader before starting to service operations.

When the process is complete a `discoverd` deployment should be run to update
the `DISCOVERD_PEERS` environment variable. You can retrieve the current value
with `flynn -a discoverd env:get DISCOVERD_PEERS`. Replace the address of the
old peer with the new one and update the value with `flynn -a discoverd env:set
DISCOVERD_PEERS=$PEER_IPS`

At this point the host has been replaced successfully and the cluster will
regain full fault tolerance.

## Controller request limits

Each controller `web` process limits authenticated API requests. The
default is 600 per minute for one account and 100,000 per minute for the
cluster install key. The install-key budget is shared by the router, the
scheduler, gitreceive, and plugin jobs. It is sized so those processes
can reconnect after a controller restart on a cluster of a few hundred
hosts. A user over their budget receives HTTP 429 and does not consume
the install-key budget.

The limit is per process. `flynn -a controller scale web=3` triples it.
Hosted and self-hosted clusters use the same defaults. To raise the
install-key budget for a larger route table, set the env and deploy the
controller:

    flynn -a controller env:set CONTROLLER_RATE_LIMIT_CLUSTER=200000

`CONTROLLER_RATE_LIMIT_USER` and `CONTROLLER_RATE_LIMIT_WINDOW` (a Go
duration, default `1m`) work the same way. An empty or non-positive
value keeps the default. There is no off switch.

`GET /ping` and a cluster-key `GET /.well-known/status` stay unlimited,
so the status app does not go unhealthy when the install-key budget is
spent.

Failed password sign-ins have a separate throttle (see
[Security](security.md#sign-in-attempts-and-password-changes)). If many
users sign in from behind one NAT, raise the per-address burst:

    flynn -a controller env:set CONTROLLER_LOGIN_IP_BURST=200

## Controller Keys

The Flynn CLI requires a controller authentication key to interact with
a cluster. The key is generated at cluster creation time, but it can be changed
and new keys can be added. Keys are stored in the environment variable
`AUTH_KEY`, a comma-separated list of authentication keys.

    # Generate a new random key
    NEW_KEY=$(openssl rand -hex 16)

    # Add the new key alongside the existing key
    flynn -a controller env:set -t web AUTH_KEY=$NEW_KEY,$(flynn -a controller env:get AUTH_KEY)

To rotate an authentication key:

    # Generate a new random key
    NEW_KEY=$(openssl rand -hex 16)

    # Add both the old and new keys to just the web process type
    flynn -a controller env:set -t web AUTH_KEY=$NEW_KEY,$(flynn -a controller env:get AUTH_KEY)

    # Update internal apps to use the new key
    flynn -a gitreceive env:set CONTROLLER_KEY=$NEW_KEY
    flynn -a tarreceive env:set CONTROLLER_KEY=$NEW_KEY AUTH_KEY=$NEW_KEY
    flynn -a blobstore env:set AUTH_KEY=$NEW_KEY
    flynn -a taffy env:set CONTROLLER_KEY=$NEW_KEY
    flynn -a redis env:set CONTROLLER_KEY=$NEW_KEY
    flynn -a mariadb env:set CONTROLLER_KEY=$NEW_KEY
    flynn -a mongodb env:set CONTROLLER_KEY=$NEW_KEY
    flynn -a kafka env:set CONTROLLER_KEY=$NEW_KEY
    flynn -a clickhouse env:set CONTROLLER_KEY=$NEW_KEY

    # Set the global key to be the new key
    flynn -a controller env:set AUTH_KEY=$NEW_KEY

    # Unset the web process-specific key
    flynn -a controller env:unset -t web AUTH_KEY
