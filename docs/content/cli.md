---
title: Command Line Interface
layout: docs
toc_min_level: 2
---

# Command Line Interface

The `flynn` CLI is the client for the [controller](architecture.html.md#controller). It deploys and manages applications, routes, and datastores.

Host-level commands (`bootstrap`, `acme`, rolling update, plugin install, …) are on `flynn-host`, which runs on cluster nodes. This page covers the user CLI, with a shorter [`flynn-host` table](#flynn-host) at the end.

This page is a **curated** reference grouped by task, not a generated dump. Every canonical user-facing `flynn` and `flynn-host` command is listed at least once below; flags, defaults, and the full text for each command come from `flynn help <command>` / `flynn-host help <command>`. Nested verbs are written in the canonical `noun:verb` form (`env:get`, `plugin:install`, `volume:gc`). The space form (`flynn env get`) and the older hyphen spellings of nested nouns (`plugin:credentials-set`, `plugin:credentials-show`, `plugin:credentials-unset`, `firewall:peer-add`, `firewall:peer-remove`) are compatibility aliases: they run the same command and print `… is now …` on stderr, and they are not listed separately here. The canonical form prints nothing.

## Installation

Pre-built binaries are published on [GitHub Releases](https://github.com/randy-girard/flynn/releases) for 64-bit Linux, macOS, and Windows (amd64 and arm64 where listed). 32-bit x86 is not supported.

```text
curl -fsSL https://github.com/randy-girard/flynn/releases/latest/download/install-flynn-cli | sudo bash
```

Options:

```text
# specific version (Flynn tags are vYYYYMMDD.N)
curl -fsSL https://github.com/randy-girard/flynn/releases/latest/download/install-flynn-cli | sudo bash -s -- --version v20260919.0

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
flynn update --version v20260919.0
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

List and switch clusters with `flynn cluster` and `flynn cluster:default <name>`; drop one with `flynn cluster:remove <name>`. Use `-c <cluster>` or `FLYNN_CLUSTER` to target a non-default cluster. After `flynn-host migrate-domain`, run `flynn cluster:refresh` on each laptop.

## Usage

```text
flynn [-a <app>] [-c <cluster>] [<command>] [<args>...]
```

`-a` selects an app. Many commands also read the `flynn` git remote in the current directory.

Run `flynn` or `flynn --help` for parent commands (including installed plugins under **Plugins:**). `flynn help env` or `flynn env --help` lists that command and its subcommands. The same shape applies to plugins: `flynn help redis` / `flynn redis --help` lists `dump`, `cli`, and `restore`. `flynn plugin:list` shows what the current cluster credential can see.

### Apps and deploys

| Command | Purpose |
| --- | --- |
| `apps` / `apps:create` / `apps:destroy` / `apps:info` | App lifecycle (`create`, `delete`, `info` are aliases) |
| `stack` / `stack:set heroku-24\|container` | Buildpack vs Dockerfile `git push` |
| `git:remote` | Add or replace the `flynn` git remote for the current app |
| `github` / `github:connect` / `github:deploy` / `github:set` / `github:disconnect` | Connect a GitHub repo and deploy through taffy (cluster GitHub App) |
| `docker:push` / `docker:login` / `docker:logout` / `docker:set-push-url` | Deploy a local Docker image through tarreceive; manage the push URL and its credentials |
| `release` / `release:show` / `release:add` / `release:update` / `release:rollback` / `release:destroy` | Release history, inspect or edit release JSON, roll back, delete |
| `deploy` / `deploy:timeout` / `deploy:batch-size` | Deploy history and per-app deploy settings (`deployment` is an alias) |
| `scale` / `ps:scale` | Formation (process counts and tags) |
| `ps` / `ps:kill` / `run` / `ps:run` | Jobs (`<process type>.<number>` short names; UUID still works; `kill` is an alias) |
| `metrics` | Latest stored app metrics snapshot (dashboard plugin) |
| `alert` / `alert:add` / `alert:enable` / `alert:disable` / `alert:remove` | App metric alerts (dashboard plugin) |
| `log` | Aggregated stdout/stderr, prefixed `source[web.1]` (`-j` takes a short name or UUID) |
| `env` / `env:get` / `env:set` / `env:unset` | App config (`-t <proc>` scopes to one process type) |
| `limit` / `limit:profiles` / `limit:profile` / `limit:set` | Named runtime profiles; raw `limit:set` for numeric CPU/memory (when allowed), `max_fd`, `temp_disk` |
| `meta` / `meta:set` / `meta:unset` | App metadata |
| `apps:export` / `apps:import` | Backup and restore an app (`export` / `import` are aliases) |

### Routing and resources

| Command | Purpose |
| --- | --- |
| `route` / `route:add http\|tcp` / `route:update` / `route:remove` | HTTP and TCP(/TLS) routes, `--auto-tls`, `--tls-mode`, `--leader`; path-based HTTP routes need `flynn-host route:add` |
| `resource` / `resource:add <provider>` / `resource:remove <provider> [<resource>]` | Provision or remove postgres, mysql, mongodb, redis, kafka, clickhouse |
| `resource:expose` / `resource:unexpose` | Export a datastore on a TCP(/TLS) route; prints `flynn-host firewall:expose` |
| `pg:psql` / `pg:dump` / `pg:restore` | Postgres console, dump, restore (built in) |
| `mysql:cli` / `mongodb:cli` / `redis:cli` (+ `:dump` / `:restore`) | Consoles, dump, restore (plugin commands after install) |
| `kafka:topics` / `kafka:topics:create` / `kafka:consumer-groups` / `kafka:consumer-groups:create` | Topics and consumer groups (after plugin install; see [Kafka](databases/kafka.md#managing-consumer-groups)) |
| `clickhouse:cli` / `clickhouse:databases` / `clickhouse:databases:create` | Databases and client (after plugin install) |
| `scheduler:list` / `scheduler:add` / `scheduler:remove` / … | Cron and interval jobs for an app (after the scheduler plugin is installed) |
| `log-sink` / `log-sink:add` / `log-sink:remove` | Per-app syslog sinks (`flynn-host log-sink` for cluster logs; `flynn-host otel` after installing the otel plugin). `logsink` is an alias. |
| `volume` / `volume:show` / `volume:decommission` | Persistent volumes attached to the app |
| `provider` / `provider:add <name> <url>` | Resource providers (`provider:add` is how plugins register themselves; rarely typed by hand) |

### Account

| Command | Purpose |
| --- | --- |
| `cluster` / `cluster:add` / `cluster:default` / `cluster:remove` / `cluster:refresh` | Registered clusters in `~/.flynnrc` |
| `cluster:backup` / `cluster:migrate-domain` / `cluster:log-sink` | Hidden compatibility commands; they still run but print that the operation moved to `flynn-host backup`, `flynn-host migrate-domain`, and `flynn-host log-sink` |
| `plugin:list` | Plugins installed on this cluster (`--known` lists official plugins and GitHub repos; `plugins` is an alias) |
| `login` | Dashboard OAuth. The token is limited to the apps and roles granted in the dashboard (see [App roles](#app-roles)). |
| `git-credentials` | Git credential helper (installed into git config by `cluster:add`; not typed by hand) |
| `update` / `upgrade` | Replace this CLI from GitHub Releases |
| `install` | Deprecated cluster installer stub; use the [manual installation](installation/manual.md) script |
| `version` | CLI version |

### App roles

Dashboard Team roles are the same grants the controller and CLI enforce after
`flynn login`. Selecting a role on an app mints those permissions in the access
token.

| Role | Grant | CLI / controller |
| --- | --- | --- |
| View | `app:read` | Read every app function (overview, logs, metrics, jobs, team list). No mutations. |
| Deploy | `app:deploy` | `POST /apps/:id/deploy` (and read). Cannot scale, env, routes, or team. |
| Manage | `app:write` | Config, scale, routes, releases, and deploy. Cannot manage team. |
| Admin | `app:admin` | Full app access, including Team invites and collaborator roles. |

Cluster administrators can create additional named roles that pick **function and action** grants (`app:logs:read`, `app:scale:write`, `app:jobs:run`, `app:team:write`, …). The four grants above remain aliases: `app:read` is all view actions, `app:write` is all mutations except team, `app:admin` is everything. Custom roles appear in the app Team picker and expand to the same grants in the token. `cluster:admin` (the
controller key, or a dashboard cluster administrator) is not an app role; it
bypasses app grants.

### Logs

`flynn log` prints each line as `source[name]: message`. `name` is the allocated
short process name (`web.1`, `web.4821`, `typ.N`) when the job has one; older
lines fall back to `processType.host-job-id`. System lines use source `flynn`
(for example `flynn[web.1]: Starting web process`). Filter a process with
`flynn log -j web.4821` or the job UUID — filtering uses the host job id, not
the display name.

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

## flynn-host

Host-level commands run on cluster nodes (`sudo flynn-host …`). `flynn-host` and `flynn-host --help` list parent commands; `flynn-host help plugin` or `flynn-host plugin --help` lists `install`, `list`, `credentials`, and the rest. Bare `flynn-host plugin` is `plugin:list`; bare `flynn-host volume` is `volume:list`.

| Command | Purpose |
| --- | --- |
| `init` / `bootstrap` / `daemon` | Write `/etc/flynn/host.json` (`--peer-ips`, `--discovery`, `--init-discovery`, `--external-ip`), bootstrap Layer 1 (`--min-hosts`, `--from-backup`), run the host daemon (systemd) |
| `download` | Fetch `flynn-host` binaries, config, and images for a release from GitHub (`--version`, `--github-repo`; used by the installer) |
| `update` | Rolling host update from GitHub Releases (`--all-nodes`, `--skip-images`, `--check`, `--check --force`, `--force`) |
| `backup` / `migrate-domain` / `cli-add-command` | Cluster backup tarball, domain rename, print the `flynn cluster:add` line for this cluster |
| `list` / `promote` / `demote` / `discover` | Raft membership (`peer` vs `proxy`), promote a node to a peer, demote one (`demote -f` / `--force` when the node is already gone), resolve discoverd services |
| `ps` / `inspect` / `log` / `stop` / `signal` / `run` | Jobs on this host (`ps -a` includes finished jobs; `log <app>` aggregates every job of an app) |
| `volume:list` / `volume:create` / `volume:delete` / `volume:gc` / `destroy-volumes` | ZFS volumes (`gc` removes datasets no job or controller record uses; `destroy-volumes` wipes the local volume store, `--include-data` to destroy backend data) |
| `plugin:install` / `plugin:update` / `plugin:update-all` / `plugin:uninstall` / `plugin:list` | First-party plugins (`--known` lists official plugins, repos, and descriptions). Install/update only accept plugin tags whose `vYYYYMMDD.N` matches this Flynn version; `plugin:update-all` updates every installed official plugin to the max compatible tag. |
| `plugin:route <name>` | HTTP/TCP routes for a plugin app |
| `plugin:credentials:set` / `plugin:credentials:show` / `plugin:credentials:unset` | GitHub token for private/draft plugin releases. Host is `github` (github.com) or a GitHub Enterprise hostname. `set` reads a paste on a TTY, `--token-file`, or piped stdin (never argv). `show` prints set/unset and a stored API URL, never the token. |
| `log-sink` / `log-sink:add` / `log-sink:list` / `log-sink:remove` | Cluster syslog sinks (`--scope system\|apps\|all`, `--app`) |
| `metrics` | Live host CPU/memory/disk/load snapshot (`--host` for one node) |
| `alert` / `alert:add` / `alert:enable` / `alert:disable` / `alert:remove` | Cluster metric alerts (dashboard plugin) |
| `otel` / `otel:add` / `otel:remove` | OpenTelemetry metrics exporters (requires `flynn-host plugin:install otel`) |
| `acme` / `acme:configure` / `acme:status` / `acme:enable` / `acme:disable` / `acme:enable-system-routes` / `acme:disable-system-routes` | Let's Encrypt account, cluster ACME on/off, and system-route TLS |
| `github` / `github:setup` / `github:configure` / `github:status` / `github:disable` | Cluster GitHub App for dashboard/CLI GitHub deploys |
| `runtime-profile` / `runtime-profile:create` / `runtime-profile:update` / `runtime-profile:remove` / `runtime-profile:allow-custom` | Named CPU/memory environments (`small`/`medium`/`large` plus custom) |
| `firewall` / `firewall:sync` / `firewall:peer:add` / `firewall:peer:remove` / `firewall:expose` / `firewall:unexpose` | Host UFW peer IPs and extra TCP ports (datastore exports) |
| `route:add http --app <app> <domain>[/path]` | Cluster-admin HTTP routes, including path-based routes |
| `domain` / `domain:apex <app>` | Cluster domain and which app serves the apex (root) hostname (`--clear` resets) |
| `webhooks` / `webhooks:add` / `webhooks:remove` | Host webhooks (job events; `-H` adds headers) |
| `events` / `events:visible` | Which host event codes the dashboard swimlane shows |
| `tags` / `tags:set` / `tags:del` | Host tags for scheduler placement |
| `fix` | Repair a broken cluster (interactive on a TTY; `--yes` for scripts) |
| `collect-debug-info` | Logs and host state for bug reports (`--tarball` for a local archive) |
| `version` | Host binary version (`--release` prints the release tag) |
