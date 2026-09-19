---
title: Command Line Interface
layout: docs
toc_min_level: 2
---

# Command Line Interface

The `flynn` CLI is the client for the [controller](architecture.html.md#controller). It deploys and manages applications, routes, and datastores.

Host-level commands (`bootstrap`, `acme`, rolling update, plugin install, …) are on `flynn-host`, which runs on cluster nodes. This page covers the user CLI.

## Installation

Pre-built binaries are published on [GitHub Releases](https://github.com/randy-girard/flynn/releases) for 64-bit Linux, macOS, and Windows (amd64 and arm64 where listed). 32-bit x86 is not supported.

```text
curl -fsSL https://github.com/randy-girard/flynn/releases/latest/download/install-flynn-cli | sudo bash
```

Options:

```text
# specific version
curl -fsSL https://github.com/randy-girard/flynn/releases/latest/download/install-flynn-cli | sudo bash -s -- --version v2024.01.27.0

# install into a user-writable directory (no sudo)
curl -fsSL https://github.com/randy-girard/flynn/releases/latest/download/install-flynn-cli | bash -s -- --dir ~/bin
```

Environment variables: `FLYNN_VERSION`, `FLYNN_GITHUB_REPO` (default `randy-girard/flynn`), `FLYNN_INSTALL_DIR` (default `/usr/local/bin`).

`flynn install` (the old cluster installer) is deprecated. Install hosts with the [manual installation](installation/manual.md) script.

## Updating the CLI

`flynn update` (alias `flynn upgrade`) downloads the latest published CLI from [GitHub Releases](https://github.com/randy-girard/flynn/releases), verifies `checksums.sha512`, and replaces the running binary.

```text
flynn update
flynn update --check
flynn update --check --force
flynn update --version v2026.09.15.0
```

`flynn update --check` (and the one-line “newer Flynn is available” note on other commands) looks up the latest GitHub release and caches the result in `~/.flynn/update-check-cache.json` for **1 hour**. The cache key is repository + current version + channel (`stable` by default; `prerelease` if `FLYNN_UPDATE_CHANNEL=prerelease` or the running tag looks like an rc/beta/alpha). A fresh cache does not contact GitHub.

Override the lifetime with `FLYNN_UPDATE_CHECK_TTL` (Go duration such as `30m`, integer seconds, or `0` to always refresh). `flynn update --check --force` also bypasses a fresh cache. `FLYNN_UPDATE_CHECK_CACHE` overrides the file path. Lookup failures never abort CLI startup; the notify-on-help line is skipped instead.

If the CLI is installed in a directory you cannot write (often `/usr/local/bin`), run `sudo flynn update`. Private or rate-limited GitHub access can use `FLYNN_GITHUB_TOKEN` or `GITHUB_TOKEN`. Override the repo with `FLYNN_GITHUB_REPO`.

This updates the user CLI only. Cluster hosts still use `flynn-host update` (including `flynn-host update --check` and `--check --force`), which share the same cache file, TTL, and environment variables.

## Adding a cluster

After bootstrap, the last log line includes a `flynn cluster:add` command. You can also generate it on a host:

```text
sudo flynn-host cli-add-command
```

Then:

```text
flynn cluster:add [-f] [-d] [--git-url <url>] [--dashboard-url <url>] [-p <tlspin>] <name> <domain> <key>
```

The TLS pin is stored in `~/.flynnrc` so the CLI can reject man-in-the-middle certificates. `flynn login` authenticates through the dashboard (OAuth) instead of a controller key.

List and switch clusters with `flynn cluster`. Use `-c <cluster>` or `FLYNN_CLUSTER` to target a non-default cluster. After `flynn-host migrate-domain`, run `flynn cluster:refresh` on each laptop.

## Usage

```text
flynn [-a <app>] [-c <cluster>] [<command>] [<args>...]
```

`-a` selects an app. Many commands also read the `flynn` git remote in the current directory.

Run `flynn`, `flynn --help`, or `flynn help <command>` for flags. Installed plugin commands appear under **Plugins:**. `flynn plugin:list` shows what the current cluster credential can see.

### Apps and deploys

| Command | Purpose |
| --- | --- |
| `apps` / `apps:create` / `apps:destroy` / `apps:info` | App lifecycle |
| `stack` / `stack:set heroku-24\|container` | Buildpack vs Dockerfile `git push` |
| `git:remote` | Git remotes |
| `github` / `github:connect` / `github:deploy` / `github:set` | Connect a GitHub repo and deploy through taffy (cluster GitHub App) |
| `docker:push` | Deploy a local Docker image |
| `release` / `deploy` | Releases and deploy history |
| `scale` | Formation (process counts) |
| `ps` / `ps:kill` / `run` | Jobs (`<process type>.<number>` short names; UUID still works) |
| `metrics` | Latest stored app metrics snapshot (dashboard plugin) |
| `alert` / `alert:add` / `alert:enable` / `alert:disable` / `alert:remove` | App metric alerts (dashboard plugin) |
| `log` | Aggregated stdout/stderr |
| `env` / `limit` / `meta` | Config, named runtime profiles (`limit:profile`), raw `limit:set` (when allowed), metadata |
| `apps:export` / `apps:import` | Backup and restore an app |

### Routing and resources

| Command | Purpose |
| --- | --- |
| `route` | HTTP and TCP(/TLS) routes, `--auto-tls` |
| `resource:add <provider>` | Provision postgres, mysql, mongodb, redis, kafka, clickhouse |
| `resource:expose` / `resource:unexpose` | Export a datastore on a TCP(/TLS) route; prints `flynn-host firewall:expose` |
| `pg:psql` / `mysql:cli` / `mongodb:cli` / `redis:cli` | Consoles, dump, restore (plugin commands after install) |
| `kafka:topics` | Topics and consumer groups (after plugin install) |
| `clickhouse:cli` | Databases and client (after plugin install) |
| `log-sink` | Per-app syslog sinks (`flynn-host log-sink` for cluster logs; `flynn-host otel` after installing the otel plugin). `logsink` is an alias. |
| `volume` | Persistent volumes |
| `provider` | Resource providers |

### Account

| Command | Purpose |
| --- | --- |
| `cluster` / `cluster:add` / `cluster:refresh` | Registered clusters |
| `plugin:list` | Plugins installed on this cluster (`--known` lists official plugins and GitHub repos) |
| `login` | Dashboard OAuth |
| `update` / `upgrade` | Replace this CLI from GitHub Releases |
| `version` | CLI version |

### Datastore export

`flynn resource:expose <provider>` (alias `flynn resource expose`) creates a
leader TCP route for postgres, mysql, mongodb, redis, kafka, or clickhouse and
prints the host command that opens the port:

```text
flynn resource:expose postgres
flynn resource:expose redis --domain redis.example.com --auto-tls
flynn resource:unexpose postgres
```

Default TLS mode is **passthrough** (the backend speaks TLS). `--auto-tls` or
`--tls-cert`/`--tls-key` switches to **terminate**. You can still build the same
thing with `flynn route:add tcp --service postgres --leader --domain
postgres.example.com --tls-mode passthrough`. On each host:

```text
sudo flynn-host firewall:expose PORT
```

See [Production — Firewalling](production.html.md#firewalling) and
[Databases](databases.html.md).

The CLI is a descendant of Heroku's [hk](https://github.com/heroku/hk).

Host-level commands run on cluster nodes (`sudo flynn-host …`):

| Command | Purpose |
| --- | --- |
| `plugin:install` / `plugin:update` / `plugin:update-all` / `plugin:uninstall` / `plugin:list` | First-party plugins (`--known` lists official plugins, repos, and descriptions). Install/update only accept plugin tags whose `vYYYYMMDD.N` matches this Flynn version; `plugin:update-all` updates every installed official plugin to the max compatible tag. |
| `plugin:route <name>` | HTTP/TCP routes for a plugin app |
| `plugin:credentials-*` | GitHub token for private/draft plugin releases |
| `log-sink` / `log-sink:add` | Cluster syslog sinks (`--scope system\|apps\|all`, `--app`) |
| `metrics` | Live host CPU/memory/disk/load snapshot (`--host` for one node) |
| `alert` / `alert:add` / `alert:enable` / `alert:disable` / `alert:remove` | Cluster metric alerts (dashboard plugin) |
| `otel` / `otel:add` | OpenTelemetry metrics (requires `flynn-host plugin:install otel`) |
| `acme:configure` / `acme:status` / `acme:enable-system-routes` | Let's Encrypt account and system-route TLS |
| `github:configure` / `github:status` / `github:setup` | Cluster GitHub App for dashboard/CLI GitHub deploys |
| `runtime-profile` / `runtime-profile:create` / `runtime-profile:allow-custom` | Named CPU/memory environments (`small`/`medium`/`large` plus custom) |
| `firewall` / `firewall:sync` / `firewall:peer-add` / `firewall:expose` / `firewall:unexpose` | Host UFW peer IPs and extra TCP ports (datastore exports) |
| `route:add http --app <app> <domain>[/path]` | Cluster-admin HTTP routes, including path-based routes |
| `domain` / `domain:apex <app>` | Cluster domain and which app serves the apex (root) hostname |
| `backup` / `migrate-domain` / `update` | Cluster backup, domain rename, rolling host update |
| `webhooks` / `webhooks:add` | Host webhooks (job events) |
| `tags` / `tags:set` | Host tags for scheduler placement |
| `fix` | Repair a broken cluster (interactive on a TTY; `--yes` for scripts) |
