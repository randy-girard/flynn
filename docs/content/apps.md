---
title: Apps
layout: docs
toc_min_level: 2
---

# Apps

Applications can be deployed to Flynn using [Buildpacks](#buildpacks) or
[Docker](docker.md). This page provides information about the management and
configuration of apps on Flynn.

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

## Deployment

Each time new code is pushed or the app configuration is changed, a new release
is created. Flynn deploys releases using a zero-downtime strategy, the new
release is started and the old release is only stopped if the new one comes up
correctly. If the new release does not come back up or something else goes
wrong, the deploy is automatically rolled back and the old release stays
running.

### Cancelling Deploys

Deploys via `git push` can be cancelled by killing the push process with
`Ctrl-C` or by signalling the process to terminate. The build will be cancelled
immediately and the code will not be deployed.

### Building specific Git branches

To deploy a different branch of the same repository, create a new app using the 
same git repository but with different remotes:

```
flynn apps:create myapp-staging --remote staging
flynn -a staging env:set FOO=bar
git push staging staging:master
```

### GitHub deploys

A cluster administrator creates one GitHub App for the cluster and saves its
credentials. After that, an app owner can connect a repository and deploy a
branch from the dashboard or the CLI. Automatic deploys use the same
**taffy** + **gitreceive** / **flynn-receiver** path as a git clone, not a
second build stack.

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
wait-for-checks off.

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

## Logs

Flynn automatically logs everything that app processes write to the standard
output and standard error streams. These logs can be retrieved with `flynn log`,
and can be followed in real time with `flynn log -f`. Each line is prefixed with
the source and short job name, for example `app[web.4821]` or `flynn[web.1]`
for system lines. Filter a process with `flynn log -j web.4821` (or the job UUID).

About every 30 seconds each running container also writes a system line of
cgroup usage in `metric=value` form, prefixed with `metrics`:

```text
metrics cpu_percent=12.35 memory_bytes=67108864 memory_limit_bytes=536870912 memory_percent=12.5 net_rx_bytes=10 net_tx_bytes=20 io_read_bytes=3 io_write_bytes=4 pids=7
```

Grep with `flynn log | grep '^metrics '`. Process start/stop/scale lines
(`Starting web process`, `Scaling down web process`) use the same system stream.
They are emitted asynchronously by the host and are best-effort: a `flynn log
-f` follower that stops reading is skipped after one second rather than
stalling the host, so a saturated follower may miss lines while other clients
and the on-disk log still receive them.

`flynn metrics` prints the latest stored app snapshot the dashboard uses for
alerts. Add rules with `flynn alert:add` (email or webhook). Cluster-wide
thresholds and live host samples use `flynn-host alert` and `flynn-host metrics`.
See [CLI](cli.md) and [Production — Monitoring](production.html.md#monitoring).

### External Logs

Apps can stream logs to syslog with `flynn log-sink:add syslog …` (one app) or
operators can forward **all user apps**, **one app**, or **Flynn system jobs**
from a host:

```text
flynn -a myapp log-sink:add syslog syslog://logs.example:514/
sudo flynn-host log-sink:add syslog --scope apps syslog://logs.example:514/
sudo flynn-host log-sink:add syslog --scope system syslog://logs.example:514/
```

Optional cluster metrics (CPU, memory, disk, load) go to an OTLP collector
after installing the **otel** plugin. See
[Production — Monitoring](production.html.md#monitoring).

## Routes

Flynn automatically configures a `https://$APPNAME.$CLUSTERDOMAIN` route that
points at instances of the `web` process type for each app. Apps must bind to
and accept HTTP requests at the port provided in the `PORT` environment variable
to receive traffic.

### Custom Domains

To add an additional HTTP route, use `flynn route:add http`:

```text
flynn route:add http www.example.com
```

DNS will also need to be configured for the domain, in this example
`www.example.com` should be set to a CNAME to `$APPNAME.$CLUSTERDOMAIN`.

### Path-based HTTP routes

A path on an HTTP route (`example.com/api`) can only be created by a cluster
administrator:

```text
sudo flynn-host route:add http --app myapp example.com/api
```

`flynn route:add` rejects path-based routes. The user CLI can still list and
remove them.

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
name) on the app's current release is also allowed. Cluster administrators
may still create routes for platform apps (dashboard, www, discovery,
controller). TCP routes use the same ownership rule.

### HTTPS

The router can automatically terminate HTTPS traffic, the certificate chain and
key are specified with the `--tls-cert` and `--tls-key` flags when creating or
updating the route. Enabling HTTPS for a route also enables HTTP/2
automatically.

Datastores use TCP routes instead of HTTP. `flynn resource:expose postgres`
creates a TLS-capable TCP route (default **passthrough**) and prints
`sudo flynn-host firewall:expose PORT`. `--auto-tls` on a TCP route sets
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

First, configure ACME with your contact email and agree to the Let's Encrypt
Terms of Service:

```text
flynn-host acme:configure --email=admin@example.com --agree-tos
```

This command registers your ACME account and enables ACME for the cluster.
You can check the current ACME configuration status with:

```text
flynn-host acme:status
```

Use `--staging` while testing (untrusted certificates) or `--directory-url` for
another ACME CA.

#### Enabling Let's Encrypt on System Routes

To enable Let's Encrypt on all system app routes (controller, dashboard, etc.),
run the following command:

```text
flynn-host acme:enable-system-routes
```

After this, public certificates replace the bootstrap self-signed cert. Clear
the CLI TLS pin with `flynn cluster:refresh --clear`.

To disable Let's Encrypt on all system app routes:

```text
flynn-host acme:disable-system-routes
```

#### Using Automatic TLS for Application Routes

Once ACME is enabled, you can create routes with automatic TLS:

```text
flynn route:add http --auto-tls www.example.com
```

Or enable automatic TLS for an existing route:

```text
flynn route:update http/2b3b2004-38f1-4e68-b856-7d8af3e4c6e1 --auto-tls
```

To disable automatic TLS for a route:

```text
flynn route:update http/2b3b2004-38f1-4e68-b856-7d8af3e4c6e1 --no-auto-tls
```

Issued certificates are renewed automatically about 30 days before they expire.
Failed issuances are retried later with backoff so Let's Encrypt is not contacted
on every error.

**Note:** The domain must be publicly accessible and DNS must be properly
configured before requesting a certificate. Let's Encrypt validates domain
ownership using HTTP-01 challenges.

### Service Discovery

Flynn registers each web process type in service discovery so the **router**
and **system apps** can find backends. User-deployed jobs cannot reach other
jobs on the overlay — including other apps and other process types of the
same app — unless they go through a route you have added (`flynn route`).

Provisioned datastore URLs use `leader.<service>.discoverd` (for example
`leader.postgres.discoverd` in `DATABASE_URL`). User jobs may resolve those
leader names only. Internal names such as `postgres.discoverd`,
`postgres-api.discoverd`, `blobstore.discoverd`, and `$APP-$TYPE.discoverd`
do not resolve for user jobs.

System apps keep a full overlay mesh so appliances, the controller, and the
router can operate.

## Limits

Process types use named runtimes for CPU and memory. Each runtime's CPU and
memory values are applied as the process **cap**. Reservation of that CPU and
memory on the host is **optional and off by default**. When you turn it on
(`flynn-host runtime:reserve`), the scheduler only places a process on a host
that still has that much free Request; otherwise the process stays pending.

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

Host-level CPU/memory reservation is off by default. Enable it with
`sudo flynn-host runtime:reserve` if a process must wait until a host has
that much free Request instead of packing onto a busy node.

CPU shares are relative: when a host is under load, a job with 2000 milliCPU
gets twice the CPU time as a job with 1000.

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
