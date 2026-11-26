---
title: Apps
layout: docs
toc_min_level: 2
---

# Apps

Applications can be deployed to Flynn using [Buildpacks](#buildpacks) or
[Docker](docker.md). This page provides information about the management and
configuration of apps on Flynn.

## Names

App names are lowercase letters, digits, and hyphen-separated groups
(`my-app`), at most 100 characters. A name that looks like an app id (a
UUID, with or without dashes) is rejected on create and rename.

User apps cannot use names that collide with dashboard paths (`plugins`,
`system`, `new`), Flynn system apps (`postgres`, `controller`,
`discoverd`), official plugins (`redis`, `www`, `dashboard`), planned
plugins, or public company-site hosts (`blog`, `docs`, `status`, `tos`,
`security`). Bootstrap and `flynn-host plugin:install` can still create
those apps as system or plugin apps.

## Listing

`flynn apps` is not a dump of every app on the cluster.

A logged-in cluster administrator (`flynn login`) listing `GET /apps` used to
return the full catalog, including bootstrap apps (`controller`,
`postgres`, `router`, `gitreceive`, `blobstore`, …) and plugin apps. The
default listing now hides those so a leaked or casually shared credential
does not advertise cluster internals. Operators who need the catalog pass
`flynn apps --all` (the controller query is `GET /apps?all=true`).
`flynn-host` and other operator tools still request that full catalog.

A **user token** (`flynn login`, `flynn token:create`) may
call `GET /apps`, but the handler returns only apps the user owns
(`owner_account=user:<id>`) or is a collaborator on. App-scoped dashboard
grants cannot list apps at all (`403`). `--all` does not expand a user
token to the operator catalog.

`POST /apps` with a user token puts the app in the caller's own account
unless `owner_account` names an org where the caller is owner or admin, or
an account where it is an admin collaborator. Any other owner is `403`, so
one tenant cannot use another account's quota, app list, app names, or
datastore-guard "same account" rule. `created_by` is always set from the
token. The cluster key and `cluster:admin` (bootstrap, plugins, operator
`flynn import`) may still name any account and creator.

The same rules apply on a hosted cluster and on a large self-hosted
cluster. Tenancy mode is not the switch. `GET /apps` without `count` is
still one JSON array. `count` (maximum 1000), `before`, and `before_id`
request one page, newest first, with visibility applied before the limit.
A page shorter than `count` is the end. `flynn apps` follows those pages.

See [Security](security.md#app-catalog) and [CLI — Apps](cli.md).

## Deleting

`flynn delete` (`apps:destroy`) queues the deletion and waits up to 60
seconds for the result. The controller worker removes the app in this
order:

1. deprovision the add-ons only this app uses (an add-on shared with
   another app is detached, not deprovisioned)
2. delete the app's git repo and build cache from the blobstore
3. delete its routes
4. delete its releases, and through them its slugs
5. delete the app record

The add-on and blobstore steps call other services and are the ones that can
fail, so they come first. If a provider refuses or is down, or the blobstore
does not answer within 30 seconds, the app keeps its routes, releases and
current release, and it keeps running (add-ons deprovisioned before the
failure stay deprovisioned). `flynn delete` prints the error, and
`flynn apps:info` shows a `Delete: failed, retrying: <time>: <error>` line.
The worker retries in the background. Each retry does only what is left,
and the app record goes away once a retry gets through.

## Configuration

As suggested in [_The Twelve-Factor App_](http://12factor.net/config), Flynn
uses environment variables to configure applications.

The `flynn env` command is used to read and write environment variables.

```text
flynn env:set SECRET=thisismysecret
```

Setting environment variables in Flynn creates a new release, which will restart
all of the app's processes with the new configuration.

### External Databases

Flynn apps can communicate with the [built-in databases](databases.html.md) as
well as databases hosted outside of Flynn. Pass the configuration for the
external database in as an environment variable with `flynn env`.

## Buildpacks

Flynn uses [buildpacks](https://devcenter.heroku.com/articles/buildpack-api) to
prepare and build apps deployed with `git push` when the app is on the
`heroku-24` stack (the default). Flynn will automatically select a standard
buildpack for most supported languages.

To deploy from a `Dockerfile` instead, switch to the container stack with
`flynn stack:set container`. See the [Docker](docker.md) documentation for
details.

The buildpack can be manually specified in cases where auto-detection is not
possible, or overridden when the standard buildpacks are not suitable.

The [multi buildpack](https://github.com/heroku/heroku-buildpack-multi) is
included in Flynn and can be used to specify a custom buildpack in addition to
allowing the use of multiple buildpacks during a single deploy.

On **arm64** hosts, classic Heroku Node (and some ffmpeg) packs ship x86_64
binaries. Apple Silicon Vagrant nodes install `qemu-user-static` 9+, and the
heroku-24 image adds amd64 glibc, so those binaries can run. `git push` prints
a notice when qemu translation is in use. See
[Node.js](languages/nodejs.md) and [Vagrant](installation/vagrant.md).

To specify a custom buildpack, create and commit a `.buildpacks` file with one
or more URLs of buildpacks to use:

```text
https://github.com/kr/heroku-buildpack-inline
```

If you don't want to add a file to your app's repository to specify the
buildpack, you can also set the `BUILDPACK_URL` environment variable to specify
a custom buildpack:

```text
flynn env:set BUILDPACK_URL=https://github.com/ryandotsmith/null-buildpack
```

### Excluding files with `.slugignore`

A `.slugignore` file in the repository root lists files to delete before the
buildpack runs, so they stay out of the slug. Each line is one shell glob,
relative to the app root:

```text
# comments and blank lines are ignored
*.md
docs/
/spec/fixtures
```

A leading `/` still means the app root, not the filesystem root. A line is a
single pattern: spaces, quotes, `$()` and leading dashes are matched as
literal characters. `*` does not match dotfiles and `**` is not special. A
pattern with a `..` component is skipped with a message in the build output.
Patterns are applied by the unprivileged build user and only inside the app
directory: a match reached through a symlink to somewhere else is left alone
(a matched symlink is removed itself), and a `.slugignore` that is a symlink is
ignored.

## Deployment

Each time new code is pushed or the app configuration is changed, a new release
is created. Flynn deploys releases using a zero-downtime strategy, the new
release is started and the old release is only stopped if the new one comes up
correctly. If the new release does not come back up or something else goes
wrong, the deploy is automatically rolled back and the old release stays
running. The rollback removes the new release's formation. If it could not,
the next successful deploy of the app scales that release to zero once the
new release has jobs up for every process type it runs. A formation that may
still be running the app, or that belongs to a datastore, is left alone (see
[Architecture](architecture.html.md#controller)). A failed deploy of a
`sirenia` datastore (such as the system Postgres) never
stops the engines that run its primary: once the primary has moved to the new
release, the rollback keeps that release's engines, rolls back the other
process types and fails the deployment with an error saying so. The next
deploy moves the engines to one release. A deploy command (`git push`, `flynn release
deploy`, `flynn-host update`) waits until the deployment is `complete` or
`failed`, including the rollback, and exits non-zero when it failed.

### Release phase

A Procfile `release` process type is not scaled. After the new release is
created and **before** traffic is swapped to it, Flynn runs that command once
(typical use: database migrations) and waits for it to exit. On `git push`,
the command's stdout and stderr are printed in the push output. A non-zero
exit fails the deploy and leaves the previous release serving traffic.

```
release: bundle exec rake db:migrate
```

The dashboard Resources tab shows the Procfile command (or, for Docker
deploys, the container command) for each process type. `release` is listed as
running once per deploy. Provisioning a datastore from that tab closes the
side panel and keeps the new instance on the list with a provisioning status
until it is live; the instance page is linked after that.

A new app's first `git push` reads `flynn-app.json` at the repository root
before the build, when the app is still untouched. That provisions the root
addons, env, formation, and `scripts.postdeploy`. A later push does not read
the file again. See [App manifest](app-manifest.md).

While a **git push** or dashboard/CLI **code** deploy is in progress, every app
tab shows **Deploy in progress**. That banner appears as soon as `git push`
starts (compile) and stays through the Procfile `release` command and traffic
swap. It clears when the rollout finishes, including while the receiver is
still exiting. The dashboard paints that banner as an info alert. Overview
shows a Deployment card with **deploying** (or **compiling** during git push).
Activity adds the deployment as it is created and updates its status through
**queued**, **deploying**, and **complete** without a reload. The Deploy tab
card lists kind, strategy, old → new release, and scale. **Config deploys**
(env changes, `addons:create` / attaching a database) do not show this header. `flynn apps:info` prints a **Build** line
during compile and a **Deploy** line while a code deployer is running.
`flynn deploy` lists `running` until the deployer finishes (`complete` or
`failed`). The dashboard Deploy tab polls the same status, including `git
push` deploys started elsewhere. Env changes and
resource attach/remove still create a config deploy (jobs restart with the new
env). Deploys stack: a second `env:unset` (or git push) is queued and runs
when the previous deploy finishes, instead of failing with "already one in
progress". Only one deploy rolls at a time. `env:set` / `env:unset` return
after the release is created; the deploy continues in the background.

### Build capacity

`git push` waits after the controller accepts the compile. The receiver
stays up while the job is pending and while it runs, streams the git
archive and config vars on stdin, and streams logs back. The pending job
is visible on the app. If the cluster cap or that account's cap is full,
the push fails immediately and no job is created. The first push of an
app creates a release row so the compile job has a release id. That row
is removed once the real release is deployed. `flynn release` and
`flynn release rollback` see only the deployed releases.

The caps are cluster settings, the same on a hosted cluster and a
self-hosted one. The defaults are 8 compiles in flight for the cluster and
3 for one `owner_account` (across that account's apps). An empty
`owner_account` is one shared bucket. One account cannot hold every slot.
`flynn-host runtime:build-limits` shows and sets the pair. Pending,
blocked, starting, and up `slugbuilder` and `dockerbuilder` jobs count.
A compile still waiting after 30 minutes is cancelled. The run timeout
starts when the process starts and is also at most 30 minutes.

### One build at a time

An app builds one push at a time. While a `git push`, a GitHub deploy
(`flynn github:deploy`, the dashboard, or a webhook) or a preview deploy of
the app is building, a second one is refused before it builds anything:

```
remote: -----> Push refused: a git push to this app is in progress (started 14:02:11 UTC). Wait for it to finish, then pull and push again.
 ! [remote rejected] main -> main (pre-receive hook declined)
```

A push whose branch moved after git read it (another push landed between
the two requests of one push) is refused the same way, with `another push
landed first`. Pull, then push again. Other apps are not affected.

A pipeline's preview remote (`git push preview <branch>`, see the pipeline
plugin) builds one push at a time for the whole pipeline, because every
branch pushed there shares one repo cache. While a push to it is building,
a push of any branch to the same remote is refused:

```
remote: -----> Push refused: a git push to this pipeline is in progress (started 14:02:11 UTC). Wait for it to finish, then pull and push again.
```

A branch that moved after git read it is refused with `another push landed
first`, before any preview app is created. Each preview app is also locked
like any other app, so a GitHub preview deploy and a push of the same
branch do not build at once.

The lock is a lease in the controller (`PUT /apps/:app/build-lock` for an
app, `PUT /build-locks/pipeline:<pipeline>` for a preview remote; both take
the cluster key only). Its holder renews it every 40 seconds, so a
gitreceive or taffy job that dies frees the app or pipeline within two
minutes. During a rolling update a gitreceive that is newer than the
controller pushes to a preview remote without the pipeline lease, as before.

### Push limits

A push larger than the cluster's push limit (8 GiB unless an operator
changed it), or one that stops sending for a minute, is refused before
anything is built, with the limit and the setting that changes it:

```
remote: -----> Push refused: it is larger than the gitreceive limit of 8 GiB (MAX_PUSH_SIZE).
 ! [remote rejected] main -> main (push larger than MAX_PUSH_SIZE (8 GiB))
```

See [Upload limits](production.html.md#upload-limits).

### Cancelling Deploys

Deploys via `git push` can be cancelled by killing the push process with
`Ctrl-C` or by signalling the process to terminate. The build will be cancelled
immediately, including a compile that is still pending, and the code will not
be deployed.

### Git submodules

`git push` includes submodules: gitreceive fetches the commit each gitlink
records from the URL in the pushed `.gitmodules`. Because that URL is chosen
by whoever pushes and gitreceive is a system job on the cluster network, the
URL must be one of:

- `https://` to a host whose every DNS address is public. git connects only
  to the addresses that were checked (`http.curloptResolve`) and does not
  follow redirects.
- `ssh://user@host/path` or `user@host:path`, also to public addresses, only
  for apps listed in gitreceive `SSH_CLIENT_KEY_APPS`, with the cluster
  `SSH_CLIENT_KEY` set and the host's key in `SSH_CLIENT_HOSTS` (see
  [SSH Client Key](production.html.md#ssh-client-key)). If either is missing,
  the push fails with `ssh submodule URL for app <app> requires ...`, naming
  what is missing.

Each gitlink in the pushed commit is matched to the `.gitmodules` section
whose `path` is that gitlink's path, and fetched from that same section's
`url`. The section name does not have to equal the path, as with
`git submodule add --name libs/foo <url> vendor/foo`, which writes
`[submodule "libs/foo"]` with `path = vendor/foo`. Only `path` and `url` are
read; `update`, `branch` and other keys are ignored and never run. The push
fails, before any submodule URL is checked or contacted, when:

- a gitlink has no section with its path (`submodule <path>: no .gitmodules
  section has path = <path>`), or that section has no `url`;
- two sections have the same `path`, or one section sets `path` or `url`
  more than once;
- a `path` is absolute, has an empty, `.` or `..` component, has a `.git`
  component (in any case), or contains control characters;
- a section name contains control characters or a `..` component.

Every submodule URL is then checked before the first submodule is fetched.
Relative URLs (`../lib.git`) are refused, since gitreceive has no superproject
remote to resolve them against; use an absolute `https://` URL.

`file://`, local paths, `http://`, `git://`, remote-helper URLs (`ext::`),
URLs that start with `-`, `*.discoverd`, single-label names, and private,
loopback, link-local, CGNAT (including the Flynn overlay) and NAT64 addresses
are refused, and the push fails with `submodule <path>: cannot fetch <url>`.
Each submodule is fetched at depth 1 for the recorded commit (falling back to
branches and tags if the server refuses a fetch by commit ID), with a 300
second limit and a 1 GiB limit per file. Nested submodules are not fetched.
To use a private repository on an internal server, vendor it into the app
repository instead.

### Building specific Git branches

To deploy a different branch of the same repository, create a new app using the
same git repository but with different remotes:

```
flynn apps:create myapp-staging --remote staging
flynn -r staging env:set FOO=bar
git push staging staging:master
```

`-a` still accepts a remote name when `-r` is omitted (`flynn -a staging env:set FOO=bar`). If both are given they must name the same app.

### GitHub deploys

Install the GitHub plugin, then a cluster administrator creates one GitHub App
for the cluster and saves its credentials. After that, an app owner can connect
a repository and deploy a branch from the dashboard or the CLI. Automatic
deploys use the same **taffy** + **gitreceive** / **flynn-receiver** path as a
git clone, not a second build stack.

```text
sudo flynn-host plugin:install github
```

Print the exact GitHub App permissions, events, and webhook URLs:

```text
sudo flynn-host github:setup
```

Create the GitHub App (Settings → Developer settings → GitHub Apps) with:

- **Repository permissions:** Contents (read), Metadata (read), Commit statuses (read), Checks (read)
- **Subscribe to events:** Push, Check suite, Check run, Status
- **Webhook URL:** `https://controller.<cluster-domain>/github/webhook` (gitreceive also accepts `https://git.<cluster-domain>/github/webhook`)
- **Webhook secret:** a random string, pasted into Flynn with the App ID and PEM private key

Save it on a host:

```text
sudo flynn-host github:configure --app-id=123456 \
  --private-key-file /root/flynn-github.pem \
  --webhook-secret "$WEBHOOK_SECRET" \
  --slug flynn-deploy
sudo flynn-host github:status
```

Install the GitHub App on the GitHub account or organization that owns the
repos, then connect one to a Flynn app (default branch is the repo default,
usually `main` or `master`):

```text
flynn -a myapp github:connect acme/myapp
flynn -a myapp github:connect --auto-deploy --wait-checks acme/myapp
flynn -a myapp github:deploy
flynn -a myapp github:deploy --branch release
flynn -a myapp github:set --auto-deploy=on --wait-checks=on
```

`--wait-checks` is Heroku-style: Flynn records the pushed SHA and deploys only
after GitHub Checks and commit statuses succeed. Repos without CI should leave
wait-for-checks off. When the enforced [cluster policy](#cluster-policy) sets
`require_github_checks`, every GitHub deploy waits for checks regardless of
this setting.

One GitHub App serves the whole cluster, so each installation belongs to a
controller account. You can connect only a repo in an installation a cluster
admin made available to your account; the dashboard lists only those. A cluster
admin connecting an app through the dashboard claims an unclaimed installation
for that app's account, and `PUT /github/installations/<id>/owner` (controller
key) sets or clears it. Deploys re-check the owner. `GET /github/installations`
and `GET /github/installations/<id>/repos` show a non-admin only installations
owned by their personal account or by the account of an app they can
`github:write`; another account's installation is a 404. The controller tells
the plugin who is calling with a signed identity (see
[Plugins](plugins.md#caller-identity-on-controller-proxied-calls)).

When the github plugin is not installed or not healthy, the controller answers
these routes itself and keeps no installation owners. A cluster admin still
sees and connects every installation. Anyone else sees, and can connect, only
an installation that an app they can `github:write` already uses; with no such
app the list is empty, and a repo outside those installations is refused the
same way whether or not it exists. A cluster admin makes the first connection
for an account.

An app has one GitHub connection, stored under its id whether you name the app
or give its id (`flynn -a <name> github:connect`, the dashboard, or
`/apps/<id-or-name>/github`).

The plugin's cluster routes (`/github/app`, installations, export, import)
accept only the controller key. The per-app `gitreceive:build` token a push
carries reaches only `GET` and `PUT /apps/<id>/github` for its own app.

The dashboard Cluster → GitHub page has the same setup steps and credential
form. The app Deploy page connects a repo and starts a deploy.

## Processes

You can get a list of an app's individual processes using `flynn ps`. Each job
has a short name like `web.4821` (`<process type>.<number>`). Pass that name to
`flynn ps:kill` or `flynn log`. The cluster UUID still works if you have it.

```text
# Get a list of processes
$ flynn ps
NAME      TYPE  STATE  CREATED        ID
web.4821  web   up     6 seconds ago  host0-cf39a906-38d1-4393-a6b1-8ad2befe8142

# Kill a process
$ flynn ps:kill web.4821
Job web.4821 killed.
```

`flynn scale web=3` sets how many jobs of that process type to run. The
cluster default stops a type at 10. A self-hosted operator raises that, or
clears it, with `flynn-host runtime:max-processes`. An upgraded cluster
stays at 10 until that command is used. On a hosted cluster, an app that
belongs to an account is limited by the account's process count and memory
quota instead of the per-type cap. An app with no owner still uses the
per-type cap. A nil account limit is unlimited. A scale that shrinks the
account's process count or memory total, and does not grow the other, is
accepted while the account is already over its quota. The same counts are
still rejected, and a growth past the limit is still rejected. The
autoscale plugin uses this formation PUT for web processes. It does not
need a hosted account, and an empty owner is not a reason to stop. A
`release` process runs once per deploy and cannot be scaled.
The per-host `flynn-max-processes` tag is a separate limit on how many
tenant jobs one machine will run.

## Logs

Flynn automatically logs everything that app processes write to the standard
output and standard error streams. These logs can be retrieved with `flynn log`,
and can be followed in real time with `flynn log -f`. Each line is prefixed with
the source and short job name, for example `app[web.4821]` or `flynn[web.1]`
for system lines. Filter a process with `flynn log -j web.4821` (or the job UUID).
The dashboard Logs tab's process dropdown lists Procfile types only (not
console TTY sessions).

Once a minute, on the minute boundary, each running container also writes one
system line of cgroup usage in `metric=value` form, prefixed with `metrics`:

```text
metrics cpu_percent=12.35 memory_bytes=67108864 memory_limit_bytes=536870912 memory_percent=12.5 net_rx_bytes=10 net_tx_bytes=20 io_read_bytes=3 io_write_bytes=4 pids=7
```

Database resource jobs append their engine samples to the same line (see
[Plugins](plugins.md#resource-metrics-line)), for example
`metrics cpu_percent=0.44 … pids=29 flynn-postgres source=postgresql-swamp-20980 addon=d656964b37dc4e69 sample#tables=11 …`.

Grep with `flynn log | grep '^metrics '`. Process start/stop/scale lines
(`Scaling up web process with command \`bin/web\``, `Scaling down web process`)
use the same system stream. A redeploy logs the new process starting (with its
command) as well as the old process scaling down. They are emitted asynchronously
by the host and are best-effort. A full syslog sink buffer drops the line
instead of stalling the process; the host resends it later from its log file. Stalled `flynn log -f` followers share one
one-second budget, so several of them cannot add a second each. A saturated
follower may miss lines while other clients and the on-disk log still receive
them. `GET /host/status` field `log_drops` counts sink-buffer and on-disk
queue drops on that host. It is not a cluster total and it resets when
`flynn-host` restarts.

`flynn metrics` prints the latest stored app snapshot the dashboard uses for
alerts. Add rules with `flynn alert:add` (email or webhook), or from **Add alert**
on the dashboard Alerts tab and metrics charts (app, cluster, and, when those
plugins are installed, Redis and Postgres). The dashboard stores every Redis
and Postgres series those plugins post so alert threshold charts and the
add-on Metrics pages share the same history. The dashboard Alerts
tab lists each firing window (newest first), and the same windows shade the
matching metric charts. Redis and Postgres add-on pages also keep a Slow
commands / Slow queries tab next to Metrics. Cluster-wide thresholds and live host samples use
`flynn-host alert` and `flynn-host metrics`.

Event alerts notify when something happens instead of when a value crosses a
threshold. `flynn alert:add --event H13 -t web --email ops@example.com` emails
when a web process crashes. A process has crashed when it exits non-zero or is
killed by a signal Flynn did not send (for example `kill -9` on the host or the
kernel OOM killer), and its job exit status is then 128 plus the signal number.
Stops Flynn requests (deploys, scale-down, `flynn-host stop`) are not crashes.
`--event A10` fires on each finished deployment
(complete or failed), `H21` on an OOM kill, `H14` when a process fails to
start, and `P13` on a datastore auto-failover. The codes are the ones on the
dashboard event chart; the dashboard alert editor lists them as **Event ·**
entries. Each matching event notifies once, and further matches inside the
cooldown (default 300s) are skipped, so a crash loop sends one notification per
cooldown. `flynn-host alert:add --event <code>` creates the same rule for every
app (optionally `--host <id>`), including host-only codes such as `D20` (disk
out of space).
See [CLI](cli.md) and [Production — Monitoring](production.html.md#monitoring).

### External Logs

`flynn log` shows the newest 10,000 lines per app id held by each
logaggregator process. Host files under `/var/log/flynn` keep about 200MB
per app on that host (100MiB plus one rotation). Neither number is a
cluster total, and neither is retention. The aggregator drops an app's
lines when the app is deleted, and drops the lines of an app that has
logged nothing for `LOG_BUFFER_TTL` (default 7 days) once nobody follows
it, so `flynn log` on an app that has been quiet that long shows nothing
until it logs again. A hosted cluster and a large
self-hosted cluster use the same sink path. Add a cluster syslog sink when
the logs have to outlive the ring.

Apps can stream logs to syslog with `flynn log-sink:add syslog …` (one app) or
operators can forward **all logs**, **all user apps**, **one app**, or **Flynn system jobs**:

```text
sudo flynn-host log-sink:add syslog syslog+tls://logs.example:6514/
flynn -a myapp log-sink:add syslog syslog+tls://logs.example:6514/
sudo flynn-host log-sink:add syslog --scope apps syslog+tls://logs.example:6514/
sudo flynn-host log-sink:add syslog --scope system syslog+tls://logs.example:6514/
```

`production:check` accepts `syslog+tls` with format `rfc6587` and no prefix.
`syslog://`, `--insecure`, newline framing, and a prefix do not satisfy it.
A prefix template can print a job's environment, including secrets. The
process stdout is still copied unchanged. A per-app sink does not cover
the cluster. See [Production — Production check](production.html.md#production-check).

Optional cluster metrics (CPU, memory, disk, load) go to an OTLP collector
after installing the **otel** plugin. See
[Production — Monitoring](production.html.md#monitoring).

## Routes

Each router process serves at most 512 in-flight HTTP requests for one app.
The app is the route `parent_ref` (`controller/apps/<app id>`), so a custom
domain and an `admin-web` service share that cap. Routers are one process per
host and do not share a counter, so the default is not multiplied by host
count. On a 3-host cluster each router enforces 512. Spread across the hosts,
an app can have 512 in flight on each host at the same time. A host that
receives all of the traffic still stops that app at 512. Other apps on that
router are still served. System apps use the same cap.

At the cap the router writes `503` with `Retry-After: 1` and the body
`App HTTP concurrency limit exceeded`, then returns. It does not queue and it
does not dial a backend. A normal response, a server-sent stream, and an
upgraded connection each hold one slot until they finish. Idle keep-alive
connections do not. TCP routes, unmatched hosts, and ACME HTTP-01 challenges
are not counted. This does not cap every connection or every file descriptor.

A create that omits `max_concurrent_requests` copies the value already stored
on that app's HTTP routes, or 512 when the app has none. An update that omits
it leaves the stored value. Sending a different number changes every live HTTP
route of the app in one transaction. A create that names a different number is
rejected. The allowed range is 1 through 1000000. While a raise is still syncing,
a router keeps enforcing the lower value until every route of that app on
that process has the new value. A rejection is a router log line
`http concurrency limit` (`flynn -a router log`). It is not counted in router
latency metrics, which are backend response time.

Flynn automatically configures a `https://$APPNAME.$CLUSTERDOMAIN` route that
points at instances of the `web` process type for each app. Apps must bind to
and accept HTTP requests at the port in the `PORT` environment variable.
Flynn injects `PORT` (and `PORT_0`, `PORT_1`, …) from the process type's
TCP ports. The default `web` process uses **8080**, not a random Heroku-style
port. `git push` prints the app's URLs when the release is deployed.

### Custom Domains

To add an additional HTTP route, use `flynn route:add http`:

```text
flynn route:add http www.example.com
```

DNS will also need to be configured for the domain, in this example
`www.example.com` should be set to a CNAME to `$APPNAME.$CLUSTERDOMAIN`.

On a hosted cluster the app's owner account must first verify the domain
(or a parent such as `example.com`) with a `_flynn-verify` TXT record. The
controller looks the record up itself, and again before a new route once the
last check is a day old, so leave the record in place. See
[Hosted custom domains](security.md#hosted-custom-domains).

### Path-based HTTP routes

A path on an HTTP route (`example.com/api`) can only be created by a cluster
administrator:

```text
sudo flynn-host route:add http --app myapp example.com/api
```

`flynn route:add` rejects path-based routes. The user CLI can still list and
remove them. An app token may update a path route an administrator made, for
example its service, but only a cluster administrator can change the path or
turn a root route into a path route.

### Cluster apex (root domain)

Apps are normally `https://$APPNAME.$CLUSTERDOMAIN`. The **apex** is the
cluster domain itself (`https://$CLUSTERDOMAIN`, for example
`https://flynncluster.com` next to `https://www.flynncluster.com`). Only one
app can own it. The www plugin registers both `www.$CLUSTERDOMAIN` and the
apex on install. Change the owner later from a host:

```text
sudo flynn-host domain
sudo flynn-host domain:apex www
sudo flynn-host domain:apex dashboard
sudo flynn-host domain:apex --clear
```

Point DNS for the bare domain at the same addresses as the cluster (A/AAAA or
ALIAS), the same way you already do for `$CLUSTERDOMAIN`.

### Additional Process Types

Flynn supports serving web traffic from multiple process types. These additional
process types must be defined in the `Procfile` and end in `-web`. For example,
this Procfile:

```text
web: ./server
admin-web: ./admin-server
```

Routes for the additional process type can be configured by specifying the
`--service` flag:

```text
flynn route:add http --service myapp-admin-web admin.example.com
```

`--service` must name a discoverd service this app owns. Flynn registers
buildpack `web` / `*-web` process types as `$APPNAME-$TYPE` and docker
container deploys as `$APPNAME-web`. A custom `service` (or port service
name) on the app's current release is also allowed. A release may only
declare service names the app owns: `$APPNAME` or `$APPNAME-*`, not a
platform service (`controller`, `blobstore`, `acme-challenge`, ...) or a name
another app owns. The controller rejects any other name with a validation
error, and flynn-host refuses to start a user job that declares one. If you
rename an app, update custom service names on its release to the new prefix
before the next deploy. Cluster administrators
may still create routes for platform apps (dashboard, www, discovery,
controller). TCP routes use the same ownership rule.

### Sticky sessions

`flynn route:add http --sticky` (or `route:update <id> --sticky`) pins each
client to one backend with an encrypted `_backend` cookie (`Path=/`). The
router sets it again only when it sends the client to a different backend,
for example after that backend went away. The cookie is:

- `HttpOnly`: page script cannot read it.
- `SameSite=Lax`: a link from another site keeps the session, but cross-site
  subrequests (images, `fetch`, form POSTs) do not carry it. An app embedded
  cross-site in an iframe is not sticky there.
- `Secure` only when the request reached the router over HTTPS. Over plain
  HTTP it is sent without `Secure` so HTTP routes stay sticky. A
  client-sent `X-Forwarded-Proto` does not count; the router replaces it with
  the listener's own scheme. Browsers do not send a `Secure` cookie over HTTP
  and do not let an HTTP response replace it, so a browser that reached the
  app over HTTPS is not sticky on the same domain over plain HTTP. Redirect
  HTTP to HTTPS on sticky routes that serve both.

Cookies set before these attributes existed still pin the client.

### HTTPS

The router can automatically terminate HTTPS traffic, the certificate chain and
key are specified with the `--tls-cert` and `--tls-key` flags when creating or
updating the route. Enabling HTTPS for a route also enables HTTP/2
automatically.

Datastores use TCP routes instead of HTTP. `flynn addons:expose postgres`
creates a TLS-capable TCP route (default **passthrough**) on the lowest free
port in 10000–29999 and prints that the cluster firewall will open that port.
`--auto-tls` on a TCP route sets
**terminate** and attaches a Let's Encrypt cert (HTTP-01 still uses 80/443).
Do not terminate TLS for Postgres or MySQL. See
[Databases — External TLS access](databases.html.md#external-tls-access).

```text
flynn route:update http/2b3b2004-38f1-4e68-b856-7d8af3e4c6e1 --tls-cert cert.pem --tls-key cert.key
```

The certificate file should contain PEM-encoded certificate blocks for the
desired certificate followed by any intermediate certificates necessary to chain
to a trusted root.

### Automatic TLS with Let's Encrypt

Flynn can automatically provision and renew TLS certificates using Let's Encrypt
(ACME). This feature must be enabled at the cluster level before it can be used.

#### Enabling Let's Encrypt at the Cluster Level

Install the Let's Encrypt plugin, then configure ACME with your contact email
and agree to the Let's Encrypt Terms of Service:

```text
sudo flynn-host plugin:install letsencrypt
flynn-host letsencrypt:configure --email=admin@example.com --agree-tos
```

This command registers your ACME account and enables ACME for the cluster.
You can check the current ACME configuration status with:

```text
flynn-host letsencrypt:status
```

Use `--staging` while testing (untrusted certificates) or `--directory-url` for
another ACME CA.

#### Enabling Let's Encrypt on System Routes

To enable Let's Encrypt on all system app routes (controller, dashboard, etc.),
run the following command:

```text
flynn-host letsencrypt:enable-system-routes
```

After this, public certificates replace the bootstrap self-signed cert. Clear
the CLI TLS pin with `flynn cluster:refresh --clear`.

To disable Let's Encrypt on all system app routes:

```text
flynn-host letsencrypt:disable-system-routes
```

#### Using Automatic TLS for Application Routes

Once ACME is enabled, add an HTTP route and turn HTTPS on with the Let's Encrypt
plugin:

```text
flynn route:add http www.example.com
flynn letsencrypt:list
flynn letsencrypt:enable www.example.com
```

You can also pass a route id (`http/<uuid>`):

```text
flynn letsencrypt:enable http/2b3b2004-38f1-4e68-b856-7d8af3e4c6e1
```

To disable automatic TLS for a hostname or route:

```text
flynn letsencrypt:disable www.example.com
```

Issued certificates are renewed automatically about 30 days before they expire.
Failed issuances are retried later with backoff so Let's Encrypt is not contacted
on every error.

**Note:** The domain must be publicly accessible and DNS must be properly
configured before requesting a certificate. Let's Encrypt validates domain
ownership using HTTP-01 challenges.

Every router answers `/.well-known/acme-challenge/` itself on every host, over HTTP and
HTTPS, before route lookup, so an app cannot serve that path. It asks each
`acme-challenge` instance in turn (skipping tenant and build job IPs) and
returns the first `200`. If no instance has the token it returns the first
instance's answer (usually `404`), and `502` if none answered. Each attempt has
a 5 second limit, so a stuck instance delays the answer and does not hold it.
Only `GET` and `HEAD` are accepted (`405` otherwise). With no usable instance,
or while the router cannot start its discoverd watch, the answer is `503`. A
failed watch start is retried after 5 seconds, not on every request.

### Service Discovery

Flynn registers each web process type in service discovery so the **router**
and **system apps** can find backends. User-deployed jobs cannot reach other
jobs on the overlay — including other apps and other process types of the
same app — unless they go through a route you have added (`flynn route`).

Provisioned datastore URLs use the **leader** hostname Flynn put in
`DATABASE_URL` / `REDIS_URL` / similar (often `leader.<service>.discoverd`).
User jobs may resolve **only the leader names attached to that app** — the
hosts in its own env, not another app's Redis or Postgres. Internal names
such as `postgres.discoverd`, `postgres-api.discoverd`,
`postgres-plugin.discoverd`, `blobstore.discoverd`, and
`$APP-$TYPE.discoverd` do not resolve for user jobs. Tenant Postgres is the
postgres plugin; its URL is not the platform appliance.

System apps keep a full overlay mesh so appliances, the controller, and the
router can operate.

## Limits

Process types use named runtimes for CPU and memory. Database instance sizes are a separate catalog, `flynn-host db-runtime` (see [Databases](databases.html.md#database-runtimes)). Each app runtime's CPU and
memory values are applied as the process **cap** (the max it may use). A
runtime does **not** hold that capacity on the host unless reservation is on
for that runtime.

Reservation is the production placement rule for a large cluster, hosted or
self-hosted. The scheduler places a reserving app process only when a host
has that much free CPU and memory left among other reserving jobs. A process
that does not fit is **blocked**. It is not left pending, and it does not
start and then get killed. Shared runtimes still pack onto the least-loaded
host and do not consume the reserved sum.

Self-hosted clusters leave reservation off unless an operator turns it on.
A large self-hosted cluster runs `flynn-host runtime:reserve` on each app
runtime that must fit. Hosted mode reserves the builtin `small`, `medium`,
and `large` runtimes even when that stored flag is off. A custom runtime
reserves in hosted mode only when its flag is on. In hosted mode, system and
platform jobs never reserve, whatever their runtime: the controller,
scheduler, router, discoverd, blobstore, the platform `postgres`, plugin
apps, and the rest of the control plane (a `flynn-system-app` that split
placement keeps on core hosts). Their jobs do not count against the
reserved sum and are never blocked by it, so a host full of tenant
reservations still gets its router or controller replacement and
`flynn-host update` can still roll the system apps. Leave headroom for them
when you size a host. Tenant databases (`redis-<uuid>` appliances, isolated
database instances) are a tenant's workload and still reserve. Self-hosted
mode applies a runtime's stored flag to every job, system jobs included.
`tenancy:mode` does not
change the stored flag. Switching back to self-hosted drops the builtin
override and leaves the flags as they are. That switch is refused while a
non-system app still has an `owner_account`. It is not a safe reversal.
See [Production — Tenancy mode](production.html.md#tenancy-mode).

An empty runtime name does not reserve. Database jobs use the database
runtime catalog and leave the app runtime empty, so this rule does not place
them. A new single-node database is placed on the host with the fewest such
jobs, in hosted and self-hosted mode. See [Databases](databases.html.md).
A job that already has a volume on a host stays on that host. It does not
go through this capacity check, and it does not go through the
`flynn-max-persistent-singletons` cap.

A reserving job skips a host that has never reported both a memory total and
a CPU total. Hosted builtin `small`, `medium`, and `large` use that rule,
and so does any runtime with reservation on, including a self-hosted cluster.
Shared jobs still pack onto that host. One missing total skips that dimension
only.

The scheduler leader refreshes those totals about every 10 seconds, off the
placement loop, including after a host gains or loses CPU or memory without
a new host id. A failed read keeps the last good totals. Followers do not
poll. The first sample is still taken when the scheduler starts watching
the host.

This is not a guarantee the host cannot run out of memory. The scheduler
counts the runtime request, or the limit when the request is 0, once. The
cgroup hard limit is twice the runtime memory. CPU is a weight, not a quota.
Turning reservation on does not rewrite existing releases: the scheduler
reads the live decision and, when the request is 0, the stored limit.

A blocked job counts toward the formation and does not hold a reservation.
It is tried again when host sync runs (about every 10 seconds), when a
refresh increases a host's totals, when a host is added, or when a host's
tags change.

Enable a guarantee when creating or updating a runtime:

```text
sudo flynn-host runtime:create --reserve --memory 1GB --cpu 1000 isolated
sudo flynn-host runtime:reserve small
sudo flynn-host runtime:reserve --disable small
```

The cluster bootstraps three builtins:

| Runtime | Memory | CPU |
| --- | --- | --- |
| `small` | 512MB | 500 milliCPU |
| `medium` | 1GB | 1000 milliCPU (matches the default when no runtime is set) |
| `large` | 2GB | 2000 milliCPU |

List runtimes and apply one:

```text
flynn limit
flynn limit:profiles
flynn limit:runtime web small
```

Operators add or change runtimes with `flynn-host runtime:create` and
`flynn-host runtime:update`. Builtin `small` / `medium` / `large` cannot
be removed.

Raw numeric CPU/memory (`flynn limit:set web memory=2GB`) is off by default
(`allow_custom_limits`). Enable it with
`sudo flynn-host runtime:allow-custom` if operators and app
collaborators should set those numbers. File descriptors and temp disk still
use `limit:set`:

```text
flynn limit:set web max_fd=12000 temp_disk=200MB
```

#### Cluster policy

The controller stores a cluster deploy policy at `GET/PUT /cluster/policy`
(cluster key or cluster admin). The enterprise plugin writes it. While the
plugin is installed (`enforced: true` on `GET`), every new release for a
non-system app is checked against it. Uninstalling the plugin keeps the
stored policy for a later reinstall but stops enforcing it.

| Field | Effect |
| --- | --- |
| `default_runtime` | Runtime given to a new process type that has no runtime and no CPU/memory set, instead of `small`. Must name an existing runtime. |
| `disable_custom_cpu` | Rejects a release when a process type has CPU/memory limits but no runtime. Apply one with `flynn limit:runtime <type> <runtime>`. Older releases without a runtime need this before their next deploy. |
| `image_allowlist` | Rejects a release whose runtime image is not listed. Entries match the build stack (`heroku-24`, `container`), the image name (`slugrunner-24` or `slugrunner`), or the `flynn docker:push` repository. An empty list allows every image. |
| `allowed_buildpacks` | Limits which buildpacks a `git push` build may use. Entries are bundled buildpack names (`multi`, `static`, `ruby`, `nodejs`, `clojure`, `python`, `java`, `gradle`, `scala`, `php`, `go`), `inline` for a `bin/detect` + `bin/compile` in the app, or a buildpack git URL for `BUILDPACK_URL` / `.buildpacks` (matched as given or without its `#ref`). Detection only tries allowed bundled buildpacks; a disallowed URL or inline buildpack fails the build. Allowing `multi` allows any URL listed in `.buildpacks`. An empty list allows every buildpack. |
| `require_github_checks` | GitHub plugin deploys need passing GitHub Checks and commit statuses. Auto-deploys wait for checks as if `--wait-checks` were on; `flynn github:deploy` and the dashboard Deploy button fail unless checks for the commit have passed. `git push` and `flynn docker:push` deploys are not affected. |

The stack and image names come from release and artifact metadata set by the
deploy tools, so the allowlist limits which deploy paths are used; it is not a
signature check on image contents.

Host-level CPU/memory reservation is off per runtime. Enable it on the
runtimes that must wait until a host has that much free Request; other
runtimes keep sharing leftover capacity under the same maxes.

CPU shares are relative: when a host is under load, a job with 2000 milliCPU
gets twice the CPU time as a job with 1000.

### Restart limit

The scheduler restarts a crashed process, waiting longer between attempts as
crashes repeat. By default it keeps trying forever. Operators can set a
cluster-wide limit on consecutive restarts (`max_restarts`):

```text
sudo flynn-host runtime:max-restarts 10
sudo flynn-host runtime:max-restarts unlimited
sudo flynn-host runtime:max-restarts
```

The dashboard sets the same value under Cluster → Runtime → Restart limit.

Once a process has been restarted that many times in a row and crashes again,
the scheduler leaves it stopped. A process that runs for 5 minutes resets its
count. Scaling the process type (`flynn scale`), deploying a new release, or
changing the limit starts stopped processes again. A scheduler leader change
also retries them, because the count is kept in memory. System apps always
restart.

### Periodic job restart

Long-running processes can accumulate memory. The scheduler therefore
restarts any job that has been running longer than a cluster-wide interval
(`job_restart_interval`, default `24h`, minimum `1h`). Set `off` (or `0`) to
disable it:

```text
sudo flynn-host runtime:job-restart-interval 72h
sudo flynn-host runtime:job-restart-interval off
sudo flynn-host runtime:job-restart-interval
```

To also restart a user app process type that has only one instance, turn on the
cluster-wide single instance option. The old job is stopped before its
replacement starts, so that app has brief downtime:

```text
sudo flynn-host runtime:single-instance-restart on
sudo flynn-host runtime:single-instance-restart off
```

Restarts are staggered:

- Each job comes due at a random point between 80% and 100% of the interval,
  fixed per job, so instances of a scaled app that started together are not
  restarted together. No job runs longer than the interval.
- Only one job per app is replaced at a time, oldest first. The next one waits
  until the app is back at its full process count with nothing starting or
  stopping, so web capacity and database quorum drop by at most one.
- Process types with a single instance are skipped by default, because replacing
  them would cause downtime. Scale such a process to 2 or more to have it
  restarted, or turn on the cluster-wide option described below.
- At most 5 jobs are restarted per scheduler pass cluster-wide, so enabling the
  setting on an old cluster rolls through apps gradually.
- Only user apps are restarted (web, worker, and other process types). System
  apps, plugin apps, and datastores (postgres, mysql, mariadb, redis, mongodb,
  kafka, clickhouse) are never restarted by this rule, even when scaled to 2 or
  more instances.
- A planned restart is not counted as a crash, so it never trips the
  [restart limit](#restart-limit) or backoff.

Each restart records a `job_restart` controller event with the app, process
type, job id, job age, and reason (`max_age`).

### Builder process limits

`git push` copies `slugbuilder` (buildpack stack) or `dockerbuilder` (container
stack) onto the app release. Those process types are hidden from dashboard JWTs
(jobs, logs, formations, and the Scale panel). Set their limits with the cluster
controller key; dashboard users cannot change them.

```text
flynn limit:set slugbuilder memory=4GB
flynn limit:set dockerbuilder memory=4GB
```

You can also specify a default builder memory limit globally on the apps that
handle `git push` deploys:

```text
flynn -a gitreceive env:set SLUGBUILDER_DEFAULT_MEMORY_LIMIT=2GB
flynn -a taffy env:set SLUGBUILDER_DEFAULT_MEMORY_LIMIT=2GB
flynn -a gitreceive env:set DOCKERBUILDER_DEFAULT_MEMORY_LIMIT=4GB
```
