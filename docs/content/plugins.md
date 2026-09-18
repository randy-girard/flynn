---
title: Plugins
layout: docs
toc_min_level: 2
---

# Plugins

Plugins are first-party cluster apps the operator installs with **`flynn-host`**,
not the user `flynn` CLI. A plugin can be a resource provider (Redis, MariaDB, …)
or any other system app (`kind: app`). Flynn ships
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

## Local sibling checkouts

Development layout (relative to the Flynn repo):

| Alias | Path from `flynn/` | Provider (`flynn resource add`) |
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
```

Install reads `flynn-plugin.json` only. Manifest **`setup`** prompts run on a TTY
(or from `FLYNN_PLUGIN_SETUP_<ENV>` / `setup.default` / `setup.generate` when
stdin is not a TTY). **`resources`** attaches existing providers (for example
`postgres`) on first install. **`routes`** creates HTTP routes (`${CLUSTER_DOMAIN}`
is expanded). If cluster ACME is already enabled (`flynn-host acme:configure`
and `flynn-host acme:enable`), HTTP plugin routes get Let's Encrypt at install
automatically (same as `flynn route add http --auto-tls`). That covers the
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
`redis mysql mongodb kafka clickhouse dashboard www discovery otel` (every
first-party plugin except the template). Smoke starts a dummy OTLP/HTTP
listener on the host (`:14318`) so the otel plugin has something to POST
`/v1/metrics` to; it is not a real collector.

After install, `flynn`, `flynn --help`, and `flynn help` against that cluster
list **resource-provider** plugin commands under a **Plugins:** section (from
the manifest stored on the plugin app, not a compiled-in `flynn` handler).
`kind: app` system plugins are installed and listed by `flynn plugins` but
do not add a user `flynn` command unless they set `cli.user`. `flynn resource
add <provider>` works for `kind: resource-provider`.

```text
flynn plugins
flynn plugin:list --known
sudo flynn-host plugin:list
sudo flynn-host plugin:list --known
```

## User CLI

The `flynn` binary does not ship Redis (or other extracted plugin) commands.
Those commands appear only after `flynn-host plugin:install` stamps
`flynn-plugin-cli` metadata on the plugin app.

`flynn-plugin.json` `cli` holds:

- `command` / `usage` — name and one-liner for `flynn help`
- `doc` — full docopt usage for `flynn help <command>` and argv parsing
- `actions` — how each subcommand runs
- `actions[].args` — cluster job argv in the plugin/resource image
- `actions[].flynn` — built-in laptop command scoped to the plugin app
  (`flynn redis` job CLIs stay on the user CLI; HTTP plugins that need
  Flynn’s route CLI on a cluster host use
  `flynn-host plugin:route <name> add http --auto-tls`).
  A `kind: app` plugin only appears on `flynn help` when `"user": true`.
- `passthrough` — append the user argv after the plugin command (nested CLIs)
- `release_env` — copy the appliance release env into the job (TLS material)

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

MariaDB, MongoDB, Kafka, and ClickHouse still have compiled `flynn` handlers
until those plugins publish the same `doc`/`actions` contract; they stay hidden
until the matching plugin is installed.

## Production

With no local checkout, an alias pulls the plugin’s published GitHub Release
(never `plugin-build` on the cluster):

```text
sudo flynn-host plugin:install redis --ref v20260914.0
sudo flynn-host plugin:install dashboard --auto-tls
sudo flynn-host plugin:install discovery
sudo flynn-host plugin:install www --auto-tls
sudo flynn-host plugin:update dashboard --ref v20260916.3
sudo flynn-host plugin:uninstall dashboard
sudo flynn-host plugin:install https://github.com/randy-girard/flynn-plugin-redis.git --ref v20260914.0
```

`--ref` is the GitHub release tag. Omit it to use the latest **published**
release (drafts and prereleases are skipped). **`plugin update`** is the
operator command once the plugin app exists: it deploys a new release,
scales the previous release to zero, runs **`hooks.upgrade`** when declared
(not **`hooks.install`**), and does not re-ask setup prompts. Re-running
**`plugin install`** on an existing app does the same in-place update.
The default org is `randy-girard` from `official-plugins.json`; override with `--github-org`,
`FLYNN_PLUGIN_GITHUB_ORG`, or `/etc/flynn/plugins.json`:

```json
{
  "github_org": "randy-girard",
  "redis": {
    "url": "https://github.com/randy-girard/flynn-plugin-redis.git",
    "ref": "v20260914.0"
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
`flynn.plugin.base` (`owner/repo@version`, same layer id as Flynn). Flynn
reconstructs the repo-relative path (`script/install.sh`) when it unpacks the
release, then runs the hook. Uploads of the squashfs layers go into the cluster
blobstore so other hosts never talk to GitHub.

When a Flynn GitHub Release is created, check **dispatch_plugins** on
**Build and Release** to queue those plugin workflows from the same run
(`gh workflow run` on Dispatch plugin releases). Publishing a draft from the
GitHub UI also starts that workflow. Configure the Flynn repo (or org) with:

* **Variable** `PLUGIN_RELEASE_REPOS` — one `owner/repo` per line (commas and
  `#` comments are allowed). That list is CI dispatch only; `flynn-host`
  install names live in `pkg/plugin/official-plugins.json`.
* **Secret** `PLUGIN_RELEASE_TOKEN` — PAT or GitHub App token with **Actions:
  write** and **Contents: read** on those plugin repos (`GITHUB_TOKEN` cannot
  start workflows in another repository).

Each plugin is built with `version` and `flynn_version` set to the Flynn tag
so the overlay uses that ubuntu-noble layer. Re-run **Dispatch plugin
releases** from Actions if a plugin job was skipped or failed, or leave
**dispatch_plugins** unchecked and run that workflow later. A plugin tag
that is already **published** with a squashfs asset is skipped; drafts and
failed uploads are dispatched again so the plugin job can resume.

Private repos and **draft** releases need a token (Contents: Read):

```text
sudo flynn-host plugin:credentials-set github --token-file /root/github.token
```

Or set `FLYNN_PLUGIN_GITHUB_TOKEN` / `GITHUB_TOKEN` on the host for one shot.
`credentials show` prints `set` or `unset`, never the secret. GitHub Enterprise:
`--api https://git.example.com/api/v3` stored per hostname.

## Backup and restore

`flynn-host backup` writes a `plugins.json` inventory (name, kind, install
source, wait URL, CLI) next to `flynn.json`. Plugin apps, providers, artifacts,
and squashfs layers live in the postgres dump (controller + default blobstore),
so **`flynn-host bootstrap --from-backup` does not run plugin install again.**
Restore waits for each plugin’s ping URL, then continues.

Plugin **volume** data (Redis AOF, Kafka topics, ClickHouse tables) is still
not in the cluster backup; those engines come back empty. Reinstalling a
plugin after restore would only be needed if you restored a backup that
never had the plugin, or if you are adding a plugin that was not installed
when the backup was taken.
