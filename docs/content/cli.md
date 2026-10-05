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

`flynn update` downloads the latest published CLI from [GitHub Releases](https://github.com/randy-girard/flynn/releases), verifies `checksums.sha512`, and replaces the running binary.

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

After bootstrap, `flynn-host cli-add-command` prints a `flynn cluster:add` line (TLS pin, no cluster key) and a `flynn login` reminder. You can also generate it on a host:

```text
sudo flynn-host cli-add-command
```

Then:

```text
flynn cluster:add [-f] [-d] [--git-url <url>] [--dashboard-url <url>] [-p <tlspin>] [--token <token>] [--email <email>] [--password <password>] <name> <domain>
flynn login [--email <email>] [--password <password>]
```

`cluster:add` stores the domain, TLS pin, and URLs in `~/.flynnrc`. It does not store the controller cluster key. `--token` stores a personal access token for CI. `--email` / `--password` log in as part of add. Context (`flynn context:use <handle>`) is a remembered default owner for `apps:create` and collaborator commands. It is not an access check.

The TLS pin is stored in `~/.flynnrc` so the CLI can reject man-in-the-middle certificates. `cluster:add` also writes the Flynn CA to `~/.flynn/ca-certs/<name>.pem` and points git at it (`sslCAInfo`), so `git push` and the CLI do not need `--insecure`. Print the CA with `flynn cluster:ca`. After system-route Let's Encrypt, `flynn cluster:refresh --clear` uses public Web PKI. `flynn login` authenticates against `https://auth.<domain>` (password grant, or `--oauth` for browser OAuth). After browser login the cluster redirects to `http://127.0.0.1:8085/` (the CLI callback). On a local cluster, `/etc/hosts` has no wildcards: if `controller.<domain>` works but `auth.<domain>` does not, add `auth.<domain>` with the same IP. Tokens are stored per cluster name in `~/.flynn/tokens/<cluster>/flynn-cli.json`. Commands that talk to the cluster require a login.

List and switch clusters with `flynn cluster` and `flynn cluster:default <name>`; drop one with `flynn cluster:remove <name>`. Use `-c <cluster>` or `FLYNN_CLUSTER` to target a non-default cluster. After `flynn-host migrate-domain`, run `flynn cluster:refresh` on each laptop. Pin updates print the new fingerprint and require confirmation; pass `--yes` for scripts. If the controller presents a publicly signed certificate (Let’s Encrypt), refresh verifies it with system CAs before pinning. `--clear` drops the pin so the CLI uses normal TLS verification.

## Usage

```text
flynn [-a <app>] [-r <remote>] [-c <cluster>] [<command>] [<args>...]
```

Global options go **before** the command. After the command they belong to that command (`flynn ps -a` lists all jobs, `flynn scale -r <release>` is a release, `flynn log -r` is raw output).

| Option | Meaning |
| --- | --- |
| `-a <app>` | App name for this invocation |
| `-r <remote>` | Git remote in the current repo; app and cluster come from that remote's URL |
| `-c <cluster>` | Cluster in `~/.flynnrc` |

Precedence (highest first): `-a`, `-r`, `FLYNN_APP`, `FLYNN_REMOTE`, `git config flynn.remote`, the single Flynn remote in the working repo. Flags beat environment, and for each pair the app name beats the remote. If `-a` and `-r` disagree (or `FLYNN_APP` and `FLYNN_REMOTE` disagree), the command fails. `-r` supplies the cluster when `-c` is not given. App-scoped plugin commands (`pg:psql`, `redis`, `scheduler`, …) honour the same overrides. Account-scoped commands (`pipeline`, `enterprise org`, …) reject `-a` / `-r`. Cluster-wide plugin commands (`billing`, `enterprise license`, …) live on `flynn-host`.

Run `flynn` or `flynn --help` for parent commands (including installed plugins under **Plugins:**). `flynn help env` or `flynn env --help` lists that command and its subcommands. Every command also accepts `:help` as `--help` (`flynn env:help`) and `:list` as the bare command (`flynn env:list` is `flynn env`). The same shape applies to plugins: `flynn help redis` / `flynn redis --help` / `flynn redis:help` lists `dump`, `cli`, and `restore`; `flynn redis` and `flynn redis:list` list that app's redis resources. `flynn plugin` / `flynn plugin:list` shows what the current cluster credential can see.

### Apps and deploys

| Command | Purpose |
| --- | --- |
| `apps` / `apps:create` / `apps:destroy` / `apps:info` | App lifecycle (`create`, `delete`, `info` are aliases). `apps:create` and `git push` print the app's HTTP URLs. `apps:info` lists those URLs and an in-progress **Deploy** line (`running` / `pending`) while the deployer is still rolling out that app. `flynn apps` lists apps the current credential may see: a user token only owned/collaborator apps; the cluster key (cluster-admin) omits platform, system, and plugin apps unless you pass `--all`. Dashboard grants cannot list apps (`403`). Dashboard paths, system apps, plugins, planned plugins, and public-site hosts (`blog`, `docs`, `tos`) are reserved. |
| `stack` / `stack:set heroku-24\|container` | Buildpack vs Dockerfile `git push` |
| `git:remote` | Add or replace the `flynn` git remote for the current app |
| `github` / `github:connect` / `github:deploy` / `github:set` / `github:disconnect` | Connect a GitHub repo and deploy through taffy (requires `flynn-host plugin:install github`) |
| `pipeline` / `pipeline:create` / `pipeline:add` / `pipeline:remove` / `pipeline:info` / `pipeline:delete` / `pipeline:set` / `pipeline:promotions` / `pipeline:preview` / `pipeline:promote` | Environments and artifact promotion (requires `flynn-host plugin:install pipeline`). Account-scoped commands take a pipeline name or id and reject `-a` / `-r`; `pipeline:promote` uses `-a` for the source app. |
| `docker:push` / `docker:login` / `docker:logout` / `docker:set-push-url` | Deploy a local Docker image through tarreceive; manage the push URL and its credentials |
| `release` / `release:show` / `release:add` / `release:update` / `release:rollback` / `release:destroy` | Release history, inspect or edit release JSON, roll back, delete |
| `deploy` / `deploy:timeout` / `deploy:batch-size` | Deploy history and per-app deploy settings (`deployment` is an alias). `STATUS running` means the deploy is still in progress; `complete` is finished. |
| `scale` / `ps:scale` | Formation (process counts and tags) |
| `ps` / `ps:kill` / `run` / `ps:run` | Jobs (`<process type>.<number>` short names; UUID still works; `kill` is an alias) |
| `metrics` | Latest stored app metrics snapshot (dashboard plugin) |
| `alert` / `alert:add` / `alert:enable` / `alert:disable` / `alert:remove` | App metric alerts (dashboard plugin) |
| `log` | Aggregated stdout/stderr, prefixed `source[web.1]` (`-j` takes a short name or UUID) |
| `env` / `env:get` / `env:set` / `env:unset` | App config (`-t <proc>` scopes to one process type). `env:set` / `env:unset` return after creating the release; if a deploy is already running, the new one is queued and runs next |
| `limit` / `limit:profiles` / `limit:runtime` / `limit:set` | Named runtimes; raw `limit:set` for numeric CPU/memory (when allowed), `max_fd`, `temp_disk`. |
| `meta` / `meta:set` / `meta:unset` | App metadata |
| `apps:export` / `apps:import` | Backup and restore an app (`export` / `import` are aliases) |
| `apps:create --owner <handle>` / `apps:transfer <app> <handle>` | Set the owning account on create, or transfer an app. The cluster key with no owner leaves the app unowned. |

### Routing and resources

| Command | Purpose |
| --- | --- |
| `route` / `route:add http\|tcp` / `route:update` / `route:remove` | HTTP and TCP(/TLS) routes, `--tls-mode`, `--leader`; path-based HTTP routes need `flynn-host route:add` |
| `letsencrypt:enable` / `letsencrypt:disable` / `letsencrypt:status` | Automatic HTTPS for a hostname or HTTP route id (requires the Let's Encrypt plugin) |
| `resource` / `resource:add <provider>` / `resource:remove [<provider>] [<resource>]` | Provision or remove postgres, mysql, mongodb, redis, kafka, clickhouse. `resource:add` returns after the instance is scheduled; the datastore keeps starting in the background (`flynn redis:wait`, `pg:wait`, `mysql:wait`, or `flynn resource`). `resource:remove` detaches the resource and returns; the isolated instance is destroyed in the background. `resource:remove` accepts the NAME from `flynn resource` (`pg-orchid-xkhthp`) with or without the provider. A leader cannot be removed while followers are still linked. Provider errors (instance not found, still has followers) are returned as-is instead of `unknown_error`. `postgres` is the tenant plugin (`flynn-host plugin:install postgres`), not the platform appliance. `--runtime` sizes the new instance from a database runtime (`flynn-host db-runtime`, default `small`). Those are not app process runtimes. Raw `--cpu`, `--memory`, and `--disk` work only after `flynn-host db-runtime:allow-custom` |
| `resource:attach` / `resource:detach` | Attach or detach an existing resource (`--as` sets the env name for postgres) |
| `resource:expose` / `resource:unexpose` | Export a datastore on a TCP(/TLS) route; prints `flynn-host firewall:expose` |
| `pg` / `pg:list` / `pg:info` / `pg:create` / `pg:follow` / `pg:wait` / `pg:promote` / `pg:unfollow` / `pg:upgrade` / `pg:dump` / `pg:restore` / `pg:psql` | Tenant Postgres, after `flynn-host plugin:install postgres`. Not built into this CLI. `pg` and `pg:list` list that app's postgres resources. `pg:help` is `pg --help`. `pg:wait` prints live replica copy progress. `pg:upgrade` shows the same while it follows, waits, and promotes. `pg:dump` / `pg:restore` dump that instance. The platform database is `flynn-host pg:psql` |
| `autoscale` / `autoscale:enable` / `autoscale:disable` / `autoscale:set` / `autoscale:info` | Web-dyno scale on router HTTP p95 (after `flynn-host plugin:install autoscale`) |
| `mysql:cli` / `mongodb:cli` / `redis:cli` (+ `:dump` / `:restore`) | Consoles, dump, restore (plugin commands after install) |
| `kafka:topics` / `kafka:topics:create` / `kafka:consumer-groups` / `kafka:consumer-groups:create` | Topics and consumer groups (after plugin install; see [Kafka](databases/kafka.md#managing-consumer-groups)) |
| `clickhouse:cli` / `clickhouse:databases` / `clickhouse:databases:create` | Databases and client (after plugin install) |
| `scheduler` / `scheduler:list` / `scheduler:add` / `scheduler:remove` / … | Cron and interval jobs for an app (after the scheduler plugin is installed). `scheduler` and `scheduler:list` are the same; `scheduler:help` is `scheduler --help` |
| `log-sink` / `log-sink:add` / `log-sink:remove` | Per-app syslog sinks (`flynn-host log-sink` for cluster logs; `flynn-host otel` after installing the otel plugin). `logsink` is an alias. |
| `volume` / `volume:show` / `volume:decommission` | Persistent volumes attached to the app |

### Account

| Command | Purpose |
| --- | --- |
| `cluster` / `cluster:add` / `cluster:default` / `cluster:remove` / `cluster:refresh` / `cluster:ca` | Registered clusters in `~/.flynnrc`. `cluster:add` stores a TLS pin and the Flynn CA (`~/.flynn/ca-certs/<name>.pem`); it does not store the cluster key. git uses `http.<git-url>.sslCAInfo` so `git push` does not need `--insecure`. `cluster:ca` prints that PEM. After Let's Encrypt on system routes, `cluster:refresh --clear` uses public Web PKI. |
| `cluster:backup` / `cluster:migrate-domain` / `cluster:log-sink` | Hidden compatibility commands; they still run but print that the operation moved to `flynn-host backup`, `flynn-host migrate-domain`, and `flynn-host log-sink` |
| `plugin` / `plugin:list` | Plugins installed on this cluster (`VERSION` is the installed GitHub tag; `--check` compares to the newest compatible published tag and shows `UPDATE`/`STATUS`; `--known` lists the first-party catalog and GitHub repos, including `enterprise`; private plugins such as `billing` appear only when GitHub credentials can read that repo; `plugins` is an alias). `plugin:list` is the same as `plugin`; `plugin:help` is `plugin --help` |
| `whoami` | Print the authenticated user, personal account, and whether the credential is a cluster admin |
| `token` / `token:create` / `token:list` / `token:revoke` | Personal access tokens. Create prints the secret once |
| `context` / `context:list` / `context:use <handle>` | Remember a default owner handle per cluster in `~/.flynnrc`. Not an access check |
| `collaborator` / `collaborator:list` / `collaborator:add` / `collaborator:remove` | Account collaborators, or app collaborators when `-a` is set. Roles: view, deploy, manage, admin. `collaborator:add <email>` creates a non-admin user when the email is unknown |
| `account:suspend` / `account:unsuspend` / `account:quota:set` | Suspend an account or set explicit quotas. Zero and negative limits are rejected |
| `login` | Password grant against `https://auth.<domain>` (or `--oauth` for browser OAuth). Tokens are stored per cluster. The token is limited to the apps and roles granted to that user (see [App roles](#app-roles)). |
| `git-credentials` | Git credential helper (installed into git config by `cluster:add`; not typed by hand). `cluster:add` records a native Flynn CLI. On a Mac with a Vagrant checkout, that is `/usr/local/bin/flynn` (`make vagrant-cli`), not `build-dev/bin/flynn` (a Linux symlink after image builds). |
| `update` | Replace this CLI from GitHub Releases |
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

Those four roles are **fixed** in OSS Flynn. Operators cannot create custom
roles or change built-in permissions. Granular function/action grants
(`app:logs:read`, `app:scale:write`, …) remain the internal expansion of the
aliases and are still enforced on tokens; composing new bundles is an
enterprise-plugin feature. Install it from the catalog
(`sudo flynn-host plugin:install enterprise`) or a checkout
(`sudo flynn-host plugin:install ../flynn-plugin-enterprise`). After install,
`flynn enterprise org` / `team` / `list` manage organizations and Enterprise
accounts (account-scoped; they reject `-a` / `-r`). License, SSO, custom roles,
audit, and policy are `flynn-host enterprise` commands. The dashboard
**Cluster → Enterprise** pages host that UI. On Flynn hosted, a paid **billing**
plan that includes `rbac`/`sso` can unlock those features without pasting a
`flynn-ent` key. `cluster:admin` (the controller
key, or a dashboard cluster administrator) is not an app role; it bypasses app
grants.

Hosted billing (`flynn-host billing`, **Cluster → Billing**) is a private plugin.
It is not in `plugin:install billing`; install `../flynn-plugin-billing` on
company-operated clusters only. `flynn billing` prints the `flynn-host` equivalent.

### Logs

`flynn log` prints each line as `source[name]: message`. `name` is the allocated
short process name (`web.1`, `web.4821`, `typ.N`) when the job has one; older
lines fall back to `processType.host-job-id`. System lines use source `flynn`
(for example `flynn[web.1]: Scaling up web process with command \`bin/web\``). Filter a process with
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

Host-level commands run on cluster nodes (`sudo flynn-host …`). `flynn-host` and `flynn-host --help` list parent commands; `flynn-host help plugin` or `flynn-host plugin --help` or `flynn-host plugin:help` lists `install`, `list`, `credentials`, and the rest. Bare `flynn-host plugin` and `flynn-host plugin:list` list plugins; bare `flynn-host volume` and `flynn-host volume:list` list volumes. Every command accepts `:help` as `--help` and `:list` as the bare command.

| Command | Purpose |
| --- | --- |
| `init` / `bootstrap` / `daemon` | Write `/etc/flynn/host.json` (`--peer-ips`, `--discovery`, `--init-discovery`, `--external-ip`), bootstrap Layer 1 (`--min-hosts`, `--from-backup`, `--admin-email`, `--admin-password`), run the host daemon (systemd) |
| `download` | Fetch `flynn-host` binaries, config, and images for a release from GitHub (`--version`, `--github-repo`; used by the installer) |
| `update` | Rolling host update from GitHub Releases (`--all-nodes`, `--skip-images`, `--recycle-user-apps`, `--check`, `--check --force`, `--force`, `--version`) |
| `rollback` | Restore a previous GitHub tag (`--version` required; implies `--all-nodes --force`). Does not undo user deploys, volumes, or plugin data. |
| `backup` / `migrate-domain` / `cli-add-command` | Cluster backup tarball, domain rename, print the `flynn cluster:add` line for this cluster |
| `pg:psql` / `pg:dump` / `pg:restore` | Platform Postgres appliance (controller database). Tenant instances use `flynn pg` from the postgres plugin |
| `list` / `promote` / `demote` / `discover` | Raft membership (`peer` vs `proxy`), promote a node to a peer, demote one (`demote -f` / `--force` when the node is already gone), resolve discoverd services |
| `ps` / `inspect` / `log` / `stop` / `signal` / `run` | Jobs on this host (`ps -a` includes finished jobs; `log <app>` aggregates every job of an app) |
| `volume:list` / `volume:create` / `volume:delete` / `volume:gc` / `destroy-volumes` | ZFS volumes (`gc` removes datasets no job or controller record uses; `destroy-volumes` wipes the local volume store, `--include-data` to destroy backend data) |
| `disk:reclaim` | Free unused host disk: volume GC, leftover image dirs, then ZFS TRIM so a file-backed pool can punch holes (`--host` for one node) |
| `tenancy:mode` / `tenancy:network-policy` | Show or set `self_hosted` or `hosted` with the cluster key. Network policy prints dry-run nftables and does not change job namespaces. `self_hosted` prints nothing. Hosted mode does not enable signup |
| `user:create` / `user:list` / `user:info` / `user:admin` / `user:disable` / `user:enable` / `user:token` / `user:bootstrap-admin` | Create and grant cluster administrators with the local cluster key. This is the only way to add cluster admins after bootstrap. |
| `plugin:install` / `plugin:update` / `plugin:update-all` / `plugin:uninstall` / `plugin:list` | First-party plugins (`--known` lists the catalog, repos, and descriptions, including `enterprise`; private plugins such as `billing` appear only when GitHub credentials can read that repo). `plugin:install hosted` installs the hosted group (dashboard, enterprise, billing) and sets tenancy mode to `hosted` without enabling signup. `plugin:list` shows the installed `VERSION`; `--check` queries GitHub for the highest compatible tag and prints `UPDATE` and `STATUS` (`current` or `update`). Install/update only accept plugin tags whose `vYYYYMMDD.N` matches this Flynn version; `plugin:update-all` updates every installed plugin (catalog, third-party GitHub source, or `flynn-plugin-<name>`) to the max compatible tag. Layers already on the host (`/var/lib/flynn/layer-cache`) skip GitHub download; Flynn OS layers skip blobstore re-upload when Flynn already has a LayerURL. Catalog plugins log that they receive cluster secrets; third-party sources prompt (or require `--yes` when stdin is not a TTY). `plugin:uninstall` confirms before dropping an exclusive platform-postgres database (`--yes` or a TTY); update never replaces that database. |
| `plugin:route <name>` | HTTP/TCP routes for a plugin app |
| `plugin:credentials:set` / `plugin:credentials:show` / `plugin:credentials:unset` | GitHub token for private/draft plugin releases. Host is `github` (github.com) or a GitHub Enterprise hostname. `set` reads a paste on a TTY, `--token-file`, or piped stdin (never argv). `show` prints set/unset and a stored API URL, never the token. |
| `log-sink` / `log-sink:add` / `log-sink:list` / `log-sink:remove` | Cluster syslog sinks (`--scope system\|apps\|all`, `--app`) |
| `metrics` | Live host CPU/memory/disk/load snapshot (`--host` for one node) |
| `alert` / `alert:add` / `alert:enable` / `alert:disable` / `alert:remove` | Cluster metric alerts (dashboard plugin) |
| `otel` / `otel:add` / `otel:remove` | OpenTelemetry metrics exporters (requires `flynn-host plugin:install otel`) |
| `letsencrypt` / `letsencrypt:configure` / `letsencrypt:status` / `letsencrypt:enable` / `letsencrypt:disable` / `letsencrypt:enable-system-routes` / `letsencrypt:disable-system-routes` | Let's Encrypt account, cluster ACME on/off, and system-route TLS (`acme:*` remains as an alias) |
| `blobstore` / `blobstore:status` / `blobstore:set` / `blobstore:credentials` / `blobstore:migrate` | Inspect the blobstore backend, switch to S3-compatible storage, rotate access keys, migrate objects (`--delete` removes them from the old backend). Writes `BACKEND_<name>` and `DEFAULT_BACKEND` on the blobstore app. |
| `github` / `github:setup` / `github:configure` / `github:status` / `github:disable` | Cluster GitHub App credentials (dashboard and `flynn github:*` need `plugin:install github`) |
| `enterprise` / `enterprise:license` / `enterprise:sso` / `enterprise:roles` / `enterprise:audit` / `enterprise:policy` / `enterprise:support` | Cluster license, SSO, custom roles, audit, and policy (requires `plugin:install enterprise`). Organizations and Enterprise accounts stay on `flynn enterprise org` / `team`. |
| `billing` / `billing:processor` / `billing:plans` / `billing:catalog` / `billing:subscribe` / `billing:usage` | Hosted Stripe billing (private plugin; install from a path). `flynn billing` is not an alias. |
| `runtime` / `runtime:create` / `runtime:update` / `runtime:remove` / `runtime:allow-custom` / `runtime:reserve` | Named CPU/memory runtimes for **app processes** (`small`/`medium`/`large` plus custom). New runtimes share host capacity (caps only). `runtime:create --reserve` or `runtime:reserve <id>` guarantees Request on the host for that runtime. This is not the database instance catalog. |
| `db-runtime` / `db-runtime:create` / `db-runtime:update` / `db-runtime:remove` / `db-runtime:ensure` / `db-runtime:drop-engine` / `db-runtime:allow-custom` | CPU, memory, and disk presets per database engine, published when that database plugin is installed. Not app process runtimes. flynn-host is allowed because it is on the cluster. Plugin install/uninstall hooks call `ensure` / `drop-engine`. The dashboard can create a custom runtime only for a cluster admin. |
| `firewall` / `firewall:sync` / `firewall:peer:add` / `firewall:peer:remove` / `firewall:expose` / `firewall:unexpose` | Host UFW peer IPs and extra TCP ports (datastore exports) |
| `route:add http --app <app> <domain>[/path]` | Cluster-admin HTTP routes, including path-based routes |
| `domain` / `domain:apex <app>` | Cluster domain and which app serves the apex (root) hostname (`--clear` resets) |
| `webhooks` / `webhooks:add` / `webhooks:remove` | Host webhooks (job events; `-H` adds headers) |
| `events` / `events:visible` | Which host event codes the dashboard swimlane shows |
| `tags` / `tags:set` / `tags:del` | Host tags for scheduler placement |
| `fix` | Repair a broken cluster (interactive on a TTY; `--yes` for scripts) |
| `collect-debug-info` | Logs and host state for bug reports (`--tarball` for a local archive) |
| `version` | Host binary version (`--release` prints the release tag) |
