---
title: Plugins
layout: docs
toc_min_level: 2
---

# Plugins

Plugins are first-party cluster apps the operator installs with **`flynn-host`**,
not the user `flynn` CLI. A plugin can be a resource provider (Redis, MariaDB, …)
or any other system app (`kind: app`). Flynn core does not special-case a plugin
by name: install reads `flynn-plugin.json` in the plugin repo.

Postgres stays in Flynn and is not a plugin.

## Local sibling checkouts

Development layout (relative to the Flynn repo):

| Alias | Path from `flynn/` | Provider (`flynn resource add`) |
|-------|--------------------|----------------------------------|
| `redis` | `../flynn-plugin-redis` | `redis` |
| `mariadb` / `mysql` | `../flynn-plugin-mariadb` | `mysql` |
| `mongodb` | `../flynn-plugin-mongodb` | `mongodb` |
| `kafka` | `../flynn-plugin-kafka` | `kafka` |
| `clickhouse` | `../flynn-plugin-clickhouse` | `clickhouse` |

Override aliases and the GitHub org in `/etc/flynn/plugins.json` (see
[Production](#production)). `PLUGIN_REPO_ROOT` (default `..`) is the parent of
local `flynn-plugin-*` checkouts. A local checkout with `flynn-plugin.json`
wins; if it is missing, the alias falls back to GitHub.

From the Flynn checkout, on a cluster host:

```text
sudo flynn-host plugin install ../flynn-plugin-redis
sudo flynn-host plugin install redis
```

If `dist/image.json` (and layers) are missing, install runs that repo’s
`script/plugin-build` first. Already-built `dist/` is reused unless `--rebuild`.
`--no-build` fails instead of compiling. GitHub URL installs never build on the
cluster.

On macOS, `script/plugin-build` uses Docker Desktop (linux/amd64). Vagrant
cluster nodes are not the image builder: smoke builds on the laptop if needed,
syncs `flynn-plugin-*` into `/opt/flynn-plugins/`, then runs `flynn-host plugin
install` on node1.

After install, `flynn help` against that cluster lists the plugin’s CLI command
from the manifest stored on the plugin app (not from a compiled-in `flynn`
handler). `flynn resource add <provider>` works for `kind: resource-provider`.

```text
sudo flynn-host plugin list
```

## User CLI

The `flynn` binary does not ship Redis (or other extracted plugin) commands.
Those commands appear only after `flynn-host plugin install` stamps
`flynn-plugin-cli` metadata on the plugin app.

`flynn-plugin.json` `cli` holds:

- `command` / `usage` — name and one-liner for `flynn help`
- `doc` — full docopt usage for `flynn help <command>` and argv parsing
- `actions` — how each subcommand runs **on the cluster**

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
sudo flynn-host plugin install redis --ref v20260914.0
sudo flynn-host plugin install https://github.com/randy-girard/flynn-plugin-redis.git --ref v20260914.0
```

`--ref` is the GitHub release tag. Omit it to use the latest **published**
release (drafts and prereleases are skipped). The default org is
`randy-girard`; override with `--github-org`, `FLYNN_PLUGIN_GITHUB_ORG`, or
`/etc/flynn/plugins.json`:

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

The release must include `flynn-plugin.json`, `image.json`, and `{id}.squashfs`
(the plugin **Build and Release** workflow already publishes those). Flynn
uploads the layers into the cluster blobstore so other hosts never talk to
GitHub.

Private repos and **draft** releases need a token (Contents: Read):

```text
sudo flynn-host plugin credentials set github --token-file /root/github.token
```

Or set `FLYNN_PLUGIN_GITHUB_TOKEN` / `GITHUB_TOKEN` on the host for one shot.
`credentials show` prints `set` or `unset`, never the secret. GitHub Enterprise:
`--api https://git.example.com/api/v3` stored per hostname.

## Backup and restore

`flynn cluster backup` writes a `plugins.json` inventory (name, kind, install
source, wait URL, CLI) next to `flynn.json`. Plugin apps, providers, artifacts,
and squashfs layers live in the postgres dump (controller + default blobstore),
so **`flynn-host bootstrap --from-backup` does not run plugin install again.**
Restore waits for each plugin’s ping URL, then continues.

Plugin **volume** data (Redis AOF, Kafka topics, ClickHouse tables) is still
not in the cluster backup; those engines come back empty. Reinstalling a
plugin after restore would only be needed if you restored a backup that
never had the plugin, or if you are adding a plugin that was not installed
when the backup was taken.
