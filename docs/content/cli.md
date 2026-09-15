---
title: Command Line Interface
layout: docs
toc_min_level: 2
---

# Command Line Interface

The `flynn` CLI is the client for the [controller](architecture.html.md#controller). It deploys and manages applications, routes, and datastores.

Host-level commands (`bootstrap`, `acme`, rolling update, plugin install, …) are on `flynn-host`, which runs on cluster nodes. This page covers the user CLI.

## Installation

Pre-built binaries are published on [GitHub Releases](https://github.com/randy-girard/flynn/releases) for Linux, macOS (amd64 and arm64), and Windows.

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

## Adding a cluster

After bootstrap, the last log line includes a `flynn cluster add` command. You can also generate it on a host:

```text
sudo flynn-host cli-add-command
```

Then:

```text
flynn cluster add [-f] [-d] [--git-url <url>] [--dashboard-url <url>] [-p <tlspin>] <name> <domain> <key>
```

The TLS pin is stored in `~/.flynnrc` so the CLI can reject man-in-the-middle certificates. `flynn login` authenticates through the dashboard (OAuth) instead of a controller key.

List and switch clusters with `flynn cluster`. Use `-c <cluster>` or `FLYNN_CLUSTER` to target a non-default cluster.

## Usage

```text
flynn [-a <app>] [-c <cluster>] <command> [<args>...]
```

`-a` selects an app. Many commands also read the `flynn` git remote in the current directory.

Run `flynn help` or `flynn help <command>` for flags.

### Apps and deploys

| Command | Purpose |
| --- | --- |
| `create` / `delete` / `apps` / `info` | App lifecycle |
| `stack` / `stack set heroku-24\|container` | Buildpack vs Dockerfile `git push` |
| `remote` | Git remotes |
| `docker push` | Deploy a local Docker image |
| `release` / `deployment` | Releases and deploy history |
| `scale` | Formation (process counts) |
| `ps` / `kill` / `run` | Jobs |
| `log` | Aggregated stdout/stderr |
| `env` / `limit` / `meta` | Config, resource limits, metadata |
| `export` / `import` | Backup and restore an app |

### Routing and resources

| Command | Purpose |
| --- | --- |
| `route` | HTTP and TCP routes, `--auto-tls` |
| `resource add <provider>` | Provision postgres, mysql, mongodb, redis, kafka, clickhouse |
| `pg` / `mysql` / `mongodb` / `redis` | Consoles, dump, restore (plugin commands after install) |
| `kafka` | Topics and consumer groups |
| `clickhouse` | Databases and client |
| `volume` | Persistent volumes |
| `provider` | Resource providers |

### Account

| Command | Purpose |
| --- | --- |
| `cluster` | Registered clusters |
| `login` | Dashboard OAuth |
| `version` | CLI version |

The CLI is a descendant of Heroku's [hk](https://github.com/heroku/hk).
