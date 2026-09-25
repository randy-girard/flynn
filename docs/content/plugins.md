---
title: Plugins
layout: docs
toc_min_level: 2
---

# Plugins

Plugins are first-party cluster apps the operator installs with **`flynn-host`**,
not the user `flynn` CLI. A plugin can be a resource provider (Redis, MariaDB, …)
or any other system app (`kind: app` or `kind: scheduler`). Flynn ships
`pkg/plugin/official-plugins.json` (embedded in `flynn-host`) so short names
know which GitHub repo to use. Install itself is still generic: it reads
`flynn-plugin.json` from that repo and does not special-case behavior by
plugin name.

```text
sudo flynn-host plugin:list --known
flynn plugin:list --known
sudo flynn-host plugin:install mysql
```

A path, git URL, `--github-org`, or `/etc/flynn/plugins.json` overrides the
catalog. Postgres stays in Flynn and is not a plugin.

## Public catalog vs private plugins

`plugin:list --known` is the **public** first-party catalog. Those names
(`redis`, `dashboard`, …) are what `flynn-host plugin:install <name>`
resolves. Flynn also has **private** first-party plugins (for example
`enterprise`). They exist, but they are **not** in that catalog and
**cannot** be installed out of the box with `plugin:install <name>`.
`--known` lists them in a separate footer so operators know the names.
A local checkout or `/etc/flynn/plugins.json` override can still point at a
private source if the operator already has one.

The **enterprise** plugin (`../flynn-plugin-enterprise`) is that private
plugin. After a path install it advertises `http://enterprise.discoverd/`,
which unlocks dashboard granular RBAC, and serves **Cluster → Enterprise**
pages (roles, OIDC SSO, audit, policy, license). Catalog CLI:
`flynn enterprise`, `enterprise:role-add`, `enterprise:sso-set`,
`enterprise:audit`. Uninstalling it returns the cluster to the four built-in
app roles.

Plugin HTTP APIs and CLIs should trust the Flynn cluster CA (or Let's Encrypt
on system routes) the same way `flynn` does. Do not document `--insecure` or
TLS skip-verify as the normal way to talk to plugin or controller endpoints.

## Local sibling checkouts

Development layout (relative to the Flynn repo):

| Alias | Path from `flynn/` | Provider (`flynn resource:add`) |
|-------|--------------------|----------------------------------|
| `redis` | `../flynn-plugin-redis` | `redis` |
| `mariadb` / `mysql` | `../flynn-plugin-mariadb` | `mysql` |
| `mongodb` | `../flynn-plugin-mongodb` | `mongodb` |
| `kafka` | `../flynn-plugin-kafka` | `kafka` |
| `clickhouse` | `../flynn-plugin-clickhouse` | `clickhouse` |
| `dashboard` | `../flynn-plugin-dashboard` | (none; `kind: app`) |
| `discovery` | `../flynn-plugin-discovery` | (none; `kind: app`) |
| `www` | `../flynn-plugin-www` | (none; `kind: app`) |
| `otel` / `opentelemetry` | `../flynn-plugin-otel` | (none; `kind: app`) |
| `letsencrypt` / `acme` / `le` | `../flynn-plugin-letsencrypt` | (none; `kind: app`) |
| `scheduler` | `../flynn-plugin-scheduler` | (none; `kind: scheduler`) |
| `github` | `../flynn-plugin-github` | (none; `kind: app`; dashboard **Deploy** and **Cluster → GitHub**) |
| `enterprise` (private) | `../flynn-plugin-enterprise` | (none; `kind: app`; **Cluster → Enterprise**. Not installable as `plugin:install enterprise`) |

The **otel** exporter API (`GET`/`POST`/`DELETE /exporters`) requires the
cluster key. `flynn-host otel` sends it (HTTP Basic, empty username).
Collector `--auth` on `otel:add` is for the OTLP endpoint only.

The discovery plugin serves the `/clusters` API on the cluster. `GET /.well-known/cluster` requires the injected `CONTROLLER_KEY` and does not publish the join token on the public route. Instance register/list require the cluster token as `Authorization: Bearer`. `flynn-host init --init-discovery` still uses unauthenticated `POST /clusters` (single-cluster reuse). `flynn-host init --discovery` and `flynn-host bootstrap --discovery` send that token as the bearer.

Override aliases and the GitHub org in `/etc/flynn/plugins.json` (see
[Production](#production)). `PLUGIN_REPO_ROOT` (default `..`) is the parent of
plugin checkouts that contain `flynn-plugin.json` (`flynn-plugin-*`). A local
checkout with `flynn-plugin.json` wins; if it is missing, the alias uses the
official catalog (then `flynn-plugin-<name>`).

From the Flynn checkout, on a cluster host:

```text
sudo flynn-host plugin:install ../flynn-plugin-redis
sudo flynn-host plugin:install redis
sudo flynn-host plugin:install ../flynn-plugin-dashboard
sudo flynn-host plugin:install ../flynn-plugin-discovery
sudo flynn-host plugin:install ../flynn-plugin-www
sudo flynn-host plugin:install ../flynn-plugin-otel
sudo flynn-host plugin:install ../flynn-plugin-scheduler
sudo flynn-host plugin:install ../flynn-plugin-github
sudo flynn-host plugin:install ../flynn-plugin-enterprise
```

Install reads `flynn-plugin.json` only. Catalog plugins log that their jobs
receive `CONTROLLER_KEY`, `DISCOVERD_AUTH_KEY`, and access-token keys
(cluster-admin equivalent) and continue. A third-party source (path, git
URL, or `--github-org` that is not Flynn's catalog) prompts on a TTY;
non-interactive installs must pass **`--yes`**. Manifest **`setup`** prompts run on a TTY
(or from `FLYNN_PLUGIN_SETUP_<ENV>` / `setup.default` / `setup.generate` when
stdin is not a TTY). **`resources`** attaches existing providers (for example
`postgres`) on first install. **`routes`** creates HTTP routes (`${CLUSTER_DOMAIN}`
is expanded). If cluster ACME is already enabled (`flynn-host letsencrypt:configure`
and `flynn-host letsencrypt:enable`), HTTP plugin routes get Let's Encrypt at install
automatically. Operators can also turn HTTPS on later with
`flynn letsencrypt:enable <hostname>` (same as the old `flynn route:add http --auto-tls`
flag, which remains as a hidden alias). That covers the
dashboard plugin and any other HTTP plugin; Flynn does not special-case a
name. Set **`auto_tls`** on an HTTP route to request TLS even when you are
not passing `--auto-tls`: without ACME, install logs a warning and leaves
the route HTTP. Pass **`flynn-host plugin:install --auto-tls`** to fail if
ACME is not enabled.

After install, operators manage those routes with the same shape as
`flynn route`, scoped to the plugin:

```text
sudo flynn-host plugin:route dashboard
sudo flynn-host plugin:route dashboard add http --auto-tls
sudo flynn-host plugin:route dashboard add http --auto-tls dashboard.example.com
sudo flynn-host plugin:route dashboard update http/<id> --auto-tls
sudo flynn-host plugin:route redis add tcp --leader --domain redis.example.com --tls-mode passthrough
```

The **www** plugin also registers the cluster apex (`$CLUSTER_DOMAIN` with no
subdomain) so `https://flynncluster.com` and `https://www.flynncluster.com` can
serve the same homepage. Operators pick a different apex app with
`flynn-host domain:apex <app>`. See [Apps — cluster apex](apps.md#cluster-apex-root-domain).

`<plugin>` is the installed app name (or its `cli.command`). Flynn does not
special-case dashboard. Omit `<domain>` on `add http` when the plugin has
exactly one HTTP route (typical after install). `kind: app` system plugins
are not published on the user `flynn` CLI (no `flynn dashboard …`); operators
use `flynn-host plugin`. A `kind: app` plugin that should be a user command
sets `"cli": { "user": true }`. Resource-provider plugins stay on `flynn`
the same way as Redis. Installed plugin apps stay `flynn-system-app`; the
dashboard lists them for cluster administrators and keeps them hidden from
scoped collaborator tokens.
**`webhooks`** registers the same host endpoints as
`flynn-host webhooks:add` (URL/headers expand `${KEY}`; `secret_env` sets
`X-Flynn-Webhook-Secret` from generated release env). Optional **`hooks.install`**
still runs on the host for anything the manifest cannot express. Optional
**`hooks.ready`** runs after the wait URL succeeds (or after routes when there
is no wait) so the plugin app can already be serving. GitHub installs
unpack **release assets only** (not a git checkout), so a declared hook must be
published next to `image.json`. GitHub asset names cannot contain slashes:
`script/install.sh` is uploaded as `script-install.sh` (basename `install.sh`
is also accepted). Install fails if the hook is declared but missing; it is
not skipped.

Uninstall reverses install without special-casing a plugin name:

```text
sudo flynn-host plugin:uninstall dashboard
sudo flynn-host plugin:uninstall redis
sudo flynn-host plugin:uninstall redis --force
```

It runs optional **`hooks.uninstall`**, removes host webhooks whose IDs were
created for that plugin, then deletes the plugin app. `DeleteApp` already
drops HTTP/TCP routes and exclusive resources. Resource-provider plugins
with provisioned resources still attached to other apps refuse unless
`--force`. The controller has no delete-provider API, so the provider row
may remain. A declared uninstall hook that is missing fails; if the original
source cannot be resolved, uninstall logs a warning and continues.

If `dist/image.json` (and layers) are missing, install runs that repo’s
`script/plugin-build` first. Already-built `dist/` is reused unless `--rebuild`.
`--no-build` fails instead of compiling. GitHub URL installs never build on the
cluster.

On macOS, `script/plugin-build` uses Docker Desktop (linux/amd64). Vagrant
cluster nodes are not the image builder: smoke builds on the laptop if needed,
syncs plugin checkouts (`flynn-plugin-*`) into `/opt/flynn-plugins/`, then
runs `flynn-host plugin:install` on node1. Default `PLUGIN_SMOKE_APPS` is
`redis mysql mongodb kafka clickhouse dashboard www discovery otel scheduler` (every
first-party plugin except the template and Let's Encrypt; Vagrant has no ACME). Smoke starts a dummy OTLP/HTTP
listener on the host (`:14318`) so the otel plugin has something to POST
`/v1/metrics` to; it is not a real collector.

After install, `flynn`, `flynn --help`, and `flynn help` against that cluster
list **resource-provider** and **scheduler** plugin **parent** commands under a **Plugins:**
section (from the manifest stored on the plugin app, not a compiled-in `flynn`
handler). Nested plugin verbs (`redis:dump`, `kafka:topics:create`) show up on
`flynn help <plugin>` / `flynn <plugin> --help`, not on the root list. `kind: app`
system plugins are installed and listed by `flynn plugin:list`
but do not add a user `flynn` command unless they set `cli.user`. `flynn
resource:add <provider>` works for `kind: resource-provider`. After the scheduler plugin
is installed, `flynn -a <app> scheduler` lists, adds, and removes cron/interval
jobs for that app. Upgrade smoke schedules `echo scheduler-smoke` every 10s on
the uploaded `upgrade-smoke` app and waits for `last_run_at` / `last_job_id`.

```text
flynn plugin:list
flynn plugin:list --check
flynn plugin:list --known
sudo flynn-host plugin:list
sudo flynn-host plugin:list --check
sudo flynn-host plugin:list --known
```

`plugin:list` prints the installed `VERSION` (the GitHub tag stamped at install). `--check` asks GitHub for the highest tag compatible with this Flynn release and adds `UPDATE` (that tag) and `STATUS` (`current`, `update`, or `-` when the plugin has no GitHub source). Plugins with `STATUS=update` can be upgraded with `flynn-host plugin:update <name>` or `flynn-host plugin:update-all`. `--known` is the public catalog only; private first-party plugins appear in a footer and cannot be installed with `plugin:install <name>`.

## User CLI

The `flynn` binary does not ship Redis (or other extracted plugin) commands.
Those commands appear only after `flynn-host plugin:install` stamps
`flynn-plugin-cli` metadata on the plugin app.

`flynn-plugin.json` `cli` holds:

- `command` / `usage` — name and one-liner for `flynn help`
- `doc` — full docopt usage for leaf `flynn help <command>:<action>` and argv parsing
- `actions` — how each subcommand runs
- `actions[].args` — cluster job argv in the plugin/resource image
- `actions[].flynn` — built-in laptop command scoped to the plugin app
  (`flynn redis` job CLIs stay on the user CLI; HTTP plugins that need
  Flynn’s route CLI on a cluster host use
  `flynn-host plugin:route <name> add http --auto-tls`).
  A `kind: app` plugin only appears on `flynn help` when `"user": true`.
- `passthrough` — append the user argv after the plugin command (nested CLIs)
- `release_env` — copy the appliance release env into the job (TLS material)
- `cluster` — run the job against the plugin system app (no `flynn -a` on the
  caller's current app). Used by enterprise and pipeline.

`flynn` and `args` are mutually exclusive on one action. Web system plugins
should not ship a user `flynn` command; operators manage HTTP/TCP routes with
`flynn-host plugin:route <name>`. Set `"user": true` only for a `kind: app`
plugin that is meant for app developers the same way as a resource provider.

Sirenia appliances may set `app.strategy`, `app.scale` (use `0` for the data
process until first provision), and `generate_env` (random secrets such as
`MYSQL_PWD`, preserved across plugin upgrades).

The laptop never executes plugin binaries. With the user’s existing controller
credentials, `flynn redis …` asks the controller to run a job using the
provisioned resource’s release image and argv from the stored spec. Templates
allow `${app.ENV}`, `${resource}`, `${resource.ENV}`, and
`${app.ENV|leader.${resource}.discoverd}`; env values are inserted once and not
re-expanded.

Every resource-provider plugin (Redis, MariaDB, MongoDB, Kafka, ClickHouse)
publishes the same `doc`/`actions` contract. Those commands stay hidden on
`flynn help` until the matching plugin is installed on the cluster.

## Dashboard addon pages

The dashboard plugin is a host/shell. Plugin UIs live on each plugin’s
cluster-level `web` process (Heroku add-on style): the dashboard shows a
resource card, and opening it renders pages the plugin serves.

`flynn-plugin.json` may include a `dashboard` block. Install stamps it on the
plugin app as `flynn-plugin-dashboard` meta (and inside `flynn-plugin-record`)
so the dashboard discovers capabilities from the cluster catalog, not a
compiled-in list.

```json
{
  "dashboard": {
    "base_url": "http://redis.discoverd/dashboard",
    "surfaces": ["app.resources"],
    "card": {
      "title": "Redis",
      "description": "In-memory cache and datastore",
      "icon": "redis"
    },
    "routes": [
      {"path": "/", "title": "Overview"},
      {"path": "/console", "title": "Console"},
      {"path": "/backup", "title": "Backup"},
      {"path": "/metrics", "title": "Metrics"}
    ]
  }
}
```

- `base_url` is a discoverd HTTP URL. The dashboard reverse-proxies it at
  `/api/plugin-ui/:plugin/...` (Resources, Deploy, and Cluster mounts) and does
  not give the browser `CONTROLLER_KEY`.
- `surfaces` is one or more of `app.resources` (Resources tab card),
  `app.deploy` (Deploy tab mount), `cluster.settings`, or `cluster.nav`.
  GitHub-style integrations use deploy/settings, not a Resources card.
- The plugin authenticates a short-lived dashboard SSO JWT (ES256, JWKS at
  `http://dashboard.discoverd/.well-known/jwks.json`) that carries app id,
  user, and permissions.
- Uninstalled plugins do not appear as a broken iframe; the dashboard shows
  an install hint (`sudo flynn-host plugin:install <name>`).
- Datastore plugins emit **per-app process metrics as events** (app id,
  resource id, timestamp, series). The dashboard stores them on the same path
  as app metrics swimlanes so a plugin Metrics page can chart that app’s
  resource. Metric names belong in the plugin README so alerts can hook them.

Postgres is core Flynn (not a plugin repo). The dashboard hosts a first-party
postgres module that follows this same card/route contract so it can move
later.

## Production

With no local checkout, an alias pulls the plugin’s published GitHub Release
(never `plugin-build` on the cluster):

```text
sudo flynn-host plugin:install redis --ref v20260914.0.0
sudo flynn-host plugin:install dashboard --auto-tls
sudo flynn-host plugin:install discovery
sudo flynn-host plugin:install www --auto-tls
sudo flynn-host plugin:update dashboard --ref v20260916.3.1
sudo flynn-host plugin:update-all
sudo flynn-host plugin:uninstall dashboard
sudo flynn-host plugin:install https://github.com/randy-girard/flynn-plugin-redis.git --ref v20260914.0.0
sudo flynn-host plugin:install https://github.com/acme/flynn-plugin-widget.git --yes --ref v20260922.0.0
```

`--ref` is the GitHub release tag. Flynn releases are always **`vYYYYMMDD.N`**.
A plugin built against that Flynn is tagged **`vYYYYMMDD.N`** when first shipped
for that Flynn, or **`vYYYYMMDD.N.B`** when only the plugin changes. Every
`vYYYYMMDD.N.*` plugin requires Flynn `vYYYYMMDD.N` images. A cluster on
`v20260919.0` installs and updates only plugins whose tag date.N matches
(patch B may increase). **`plugin:install`** / **`plugin:update`** refuse a
plugin release that does not match the running Flynn version.
**`plugin:update`** without `--ref` picks the highest compatible
`vYYYYMMDD.N.B` for this Flynn, never a newer Flynn date.N.
**`plugin:update-all`** does that for every installed official plugin,
continues past individual failures, and prints a per-plugin result.
Omit `--ref` on a single update to use that same compatible calver
(drafts and prereleases are skipped). **`plugin:update`** is the
operator command once the plugin app exists: it deploys a new release,
scales the previous release to zero, copies missing cluster secrets
(`CONTROLLER_KEY`, `DISCOVERD_AUTH_KEY`, access-token keys) from
controller/postgres/gitreceive, runs **`hooks.upgrade`** when declared
(not **`hooks.install`**), and does not re-ask setup prompts. Re-running
**`plugin:install`** on an existing app does the same in-place update.
The default org is `randy-girard` from `official-plugins.json`; override with `--github-org`,
`FLYNN_PLUGIN_GITHUB_ORG`, or `/etc/flynn/plugins.json`:

```json
{
  "github_org": "randy-girard",
  "redis": {
    "url": "https://github.com/randy-girard/flynn-plugin-redis.git",
    "ref": "v20260914.0.0"
  }
}
```

A string value still works (`"redis": "/opt/flynn-plugins/flynn-plugin-redis"`
or a git URL). `repo` may be `owner/name` when the GitHub repo does not match
`flynn-plugin-<alias>`.

The release must include `flynn-plugin.json`, `image.json`, the plugin **delta**
squashfs, and any **`hooks.install` / `hooks.upgrade` / `hooks.uninstall`**
scripts declared in the manifest (flat names such as `script-install.sh`).
plugin-build copies those scripts into `dist/` so **Build and Release** uploads
them. Flynn ubuntu-noble is **not** re-uploaded from each plugin (the same
~200MiB file from every plugin job 502s `uploads.github.com`). Hosts fetch that
OS layer from the Flynn GitHub Release named in artifact meta
`flynn.plugin.base` (`owner/repo@version`, same layer id as Flynn) **only when
the cluster does not already have that Flynn image**. If `/etc/flynn/images.json`,
`FLYNN_IMAGES_JSON`, `/var/lib/flynn/layer-cache`, or installed Flynn artifacts
already contain a layer, **plugin:install** and **plugin:update** copy it from
the local cache (OS **and** plugin delta) and skip GitHub. The Flynn OS layer
is not copied into the plugin blobstore prefix when Flynn already publishes a
LayerURL; jobs use that URL (or the host layer-cache, same as `flynn-host
update`). Overlay/delta still uploads once so other hosts can fetch it.
Flynn reconstructs the repo-relative
path (`script/install.sh`) when it unpacks the release, then runs the hook.

When a Flynn GitHub Release is created, **dispatch_plugins** is on by default
on **Build and Release** so those plugin workflows are queued from the same
run (`gh workflow run` on Dispatch plugin releases). Uncheck it to skip
fan-out. Publishing a draft from the GitHub UI also starts that workflow.
Configure the Flynn repo (or org) with:

* **Variable** `PLUGIN_RELEASE_REPOS` — one `owner/repo` per line (commas and
  `#` comments are allowed). That list is CI dispatch only; `flynn-host`
  install names live in `pkg/plugin/official-plugins.json`.
* **Secret** `PLUGIN_RELEASE_TOKEN` — PAT or GitHub App token with **Actions:
  write** and **Contents: read** on those plugin repos (`GITHUB_TOKEN` cannot
  start workflows in another repository).
* **Flynn secret** `DISCORD_RELEASE_CHANNEL_WEBHOOK_URL` (or
  `DISCORD_FLYNN_RELEASE_CHANNEL_WEBHOOK_URL`) — Discord webhook for the Flynn
  release channel. After a published Flynn GitHub Release (not a draft), CI
  posts `@everyone`, the change notes, and a link to the release. Flynn prefers
  the repo secret so a plugin-channel **variable** cannot override it.
* **Plugin variable** `DISCORD_RELEASE_CHANNEL_WEBHOOK_URL` — Discord webhook
  for the plugins release channel, set on each plugin repo. Same post shape as
  Flynn. The GitHub publish step fails if Discord does not return HTTP 200/204.
  Omit the variable/secret to skip.

Each plugin is built with its own `version` (`vYYYYMMDD.N.B`) and
`flynn_version` set to the Flynn tag so the overlay uses that ubuntu-noble
layer. Flynn dispatch uses `{flynn_tag}.0`; a plugin-only change increments
the last number without a new Flynn release. Re-run **Dispatch plugin
releases** from Actions if a plugin job was skipped or failed, or uncheck
**dispatch_plugins** and run that workflow later. A plugin tag
that is already **published** with a squashfs asset is skipped (including a
legacy two-part tag matching the Flynn version); drafts and
failed uploads are dispatched again so the plugin job can resume.

Private repos and **draft** releases need a token (Contents: Read). The host
argument is required: `github` means github.com, or pass a GitHub Enterprise
hostname. `set` never takes the token on the command line.

```text
sudo flynn-host plugin:credentials:set github
sudo flynn-host plugin:credentials:set github --token-file /root/github.token
sudo cat /root/github.token | sudo flynn-host plugin:credentials:set github
sudo flynn-host plugin:credentials:show github
sudo flynn-host plugin:credentials:unset github
```

On a TTY with no `--token-file` and no pipe, `set` prompts you to paste the
token (input is hidden). A pipe still reads the token from stdin. With no TTY,
no pipe, and no `--token-file`, `set` errors instead of hanging. `show` prints
`set` or `unset` and a stored API URL, never the secret. `unset` says whether
credentials were removed or nothing was stored. Or set
`FLYNN_PLUGIN_GITHUB_TOKEN` / `GITHUB_TOKEN` on the host for one shot. GitHub
Enterprise: store per hostname with `--api https://git.example.com/api/v3`.
The hyphen forms (`plugin:credentials-set`) remain aliases.

## Backup and restore

`flynn-host backup` writes a `plugins.json` inventory (name, kind, install
source, wait URL, CLI) next to `flynn.json`. Plugin apps, providers, artifacts,
and squashfs layers live in the postgres dump (controller + default blobstore),
so **`flynn-host bootstrap --from-backup` does not run plugin install again.**
Restore waits for each plugin’s ping URL, then continues. A missing formation
on the current postgres release does not abort the backup (process counts
are taken from another scaled formation on that app).

Plugin **volume** data (Redis AOF, Kafka topics, ClickHouse tables) is still
not in the cluster backup; those engines come back empty. Reinstalling a
plugin after restore would only be needed if you restored a backup that
never had the plugin, or if you are adding a plugin that was not installed
when the backup was taken.
