# Flynn

Flynn is an open source [platform as a service](https://en.wikipedia.org/wiki/Platform_as_a_service). It runs anything that runs on Linux: 12-factor web apps, background workers, and stateful services.

This repository is a **community fork** of [flynn/flynn](https://github.com/flynn/flynn). The original project is unmaintained. The goal of this fork is to keep Flynn running on modern Linux, with current language stacks, current database versions, and the operational features people actually use.

[Discord](https://discord.gg/VU2ZqrPUay) · [GitHub](https://github.com/randy-girard/flynn) · [Releases](https://github.com/randy-girard/flynn/releases)

## What Flynn does

A Flynn cluster is a set of Ubuntu hosts. You deploy apps with `git push` or Docker, attach managed datastores, and the platform handles scheduling, routing, logs, TLS, and rolling updates.

| Area | What you get |
| --- | --- |
| Deploy | `git push` with [Heroku-24 buildpacks](docs/content/apps.md#buildpacks), `git push` from a `Dockerfile` ([container stack](docs/content/docker.md#container-stack)), or `flynn docker push` of a local image |
| Runtime | Process types from a `Procfile`, scale with `flynn scale`, zero-downtime deploys with automatic rollback |
| Routing | HTTP/HTTPS and TCP routes, custom domains, HTTP/2, automatic Let's Encrypt certificates |
| Datastores | PostgreSQL 16, MariaDB 10.11, MongoDB 7.0, Redis, Kafka 3.9 (KRaft), ClickHouse |
| Ops | Dashboard, CLI, ZFS volumes, clustered log aggregation, app export/import |
| Isolation | User jobs cannot reach other jobs or internal APIs on the overlay; they use routes and injected datastore URLs |

Flynn components (controller, router, scheduler, appliances, …) are themselves apps on the cluster. You scale and update the platform with the same APIs you use for your own software.

## Status

This fork is a work in progress. It targets **Ubuntu 24.04 LTS (amd64)** as the host OS and **Go 1.24** as the compiler.

It is suitable for development, staging, and small production workloads. Read [Security](docs/content/security.md) and [Production](docs/content/production.html.md) before putting sensitive data on a cluster. Database appliances are not yet tuned for very large datasets or high write volume.

## Requirements

- **OS:** Ubuntu 24.04 LTS amd64 (the install script still accepts 16.04 and 18.04, but 24.04 is what this fork builds and tests against)
- **Hosts:** 2 GB RAM, 40 GB disk, and 2 CPU cores per node as a minimum; more for appliances and builds
- **HA:** three or more nodes. A single node (`SINGLETON`) is fine for trying Flynn; do not use it as production
- **Network:** all UDP and TCP between cluster members; externally, open **80**, **443**, and optionally **3000–3500** for user TCP routes. Internal Flynn ports must not be on the public internet
- **Storage:** ZFS. The installer can create a zpool on a dedicated disk (`--zpool-create-device`); a sparse file is the default and is not recommended for production

## Install the CLI

The `flynn` CLI runs on Linux, macOS (Intel and Apple Silicon), and Windows.

```bash
curl -fsSL https://github.com/randy-girard/flynn/releases/latest/download/install-flynn-cli | sudo bash
```

Pin a version with `--version`, or set `FLYNN_GITHUB_REPO` / `--repo` if you are installing from a different fork.

After you bootstrap a cluster, add it:

```bash
flynn cluster add <name> <domain> <key>
# or, on a host in the cluster:
sudo flynn-host cli-add-command
```

You can also sign in through the dashboard with `flynn login`. Run `flynn help` for the full command list.

## Install a cluster

On each Ubuntu 24.04 host:

```bash
curl -fsSL https://github.com/randy-girard/flynn/releases/latest/download/install-flynn | sudo bash
```

That installs `flynn-host`, container images, and a systemd unit.

**Three-node cluster** using the discovery service:

```bash
# first node
sudo flynn-host init --init-discovery
# prints https://discovery.flynn.cloud.randygirard.com/clusters/<id>

# other nodes
sudo flynn-host init --discovery https://discovery.flynn.cloud.randygirard.com/clusters/<id>

sudo systemctl start flynn-host
```

On one node, point DNS (`CLUSTER_DOMAIN` A records to every host, plus a wildcard CNAME) and bootstrap:

```bash
sudo \
  CLUSTER_DOMAIN=example.com \
  flynn-host bootstrap \
  --min-hosts 3 \
  --discovery https://discovery.flynn.cloud.randygirard.com/clusters/<id>
```

You can skip discovery and pass `--peer-ips 10.0.0.1,10.0.0.2,10.0.0.3` instead. Step-by-step instructions are in [Manual installation](docs/content/installation/manual.md). Production notes (dedicated ZFS, blobstore backends, backups) are in [Production](docs/content/production.html.md).

### Local development cluster

The root `Vagrantfile` uses the `bento/ubuntu-24.04` box.

- `vagrant up builder` — build VM (`setup.sh` installs Go, Docker, ZFS, and appliance test deps)
- `vagrant up node1 node2 node3` — a three-node cluster on a host-only network

See [Vagrant](docs/content/installation/vagrant.md) and [Development](docs/content/development.html.md).

## Deploy an app

```bash
flynn create myapp
flynn resource add postgres    # optional; see datastores below
git push flynn main            # or: master
```

`git push` uses the **heroku-24** stack by default (buildpacks). To build a `Dockerfile` on the cluster with BuildKit:

```bash
flynn stack set container
git push flynn main
```

To push an image you already built locally:

```bash
flynn -a myapp docker push myimage:tag
flynn -a myapp scale app=1
```

Apps bind HTTP on `$PORT`. Flynn adds `https://$APP.$CLUSTER_DOMAIN` automatically. Custom domains, process types, logs, limits, and Let's Encrypt are covered in [Apps](docs/content/apps.md) and [Basics](docs/content/basics.md).

### Buildpacks (heroku-24)

These buildpacks ship in slugbuilder and are auto-detected:

Go, Node.js, Python, Ruby, PHP, Java (Maven), Gradle, Scala, Clojure, and static files. Custom buildpacks: `BUILDPACK_URL` or a `.buildpacks` file (multi-buildpack).

Language notes live under [docs/content/languages](docs/content/languages).

## Datastores

Provision from an app with `flynn resource add <provider>`. Connection URLs are injected as environment variables (`DATABASE_URL`, `REDIS_URL`, `KAFKA_URL`, …). User jobs reach appliances at the **leader** hostname Flynn put in those URLs, not at internal `*.discoverd` names.

| Provider | Engine | Default topology | Notes |
| --- | --- | --- | --- |
| `postgres` | PostgreSQL **16** | HA (primary + sync + async) | PostGIS, pgRouting, TimescaleDB. `flynn pg psql` / `dump` / `restore` |
| `mysql` | MariaDB **10.11** | HA, started on first provision | `flynn mysql console` / `dump` / `restore` |
| `mongodb` | MongoDB **7.0** | Replica set, started on first provision | `flynn mongodb mongo` / `dump` / `restore` |
| `redis` | Redis (Ubuntu 24.04 package) | Single process | Ephemeral; caching and development only |
| `kafka` | Apache Kafka **3.9** (KRaft, no ZooKeeper) | 3 brokers (1 on singleton) | TLS to clients by default. Create topics before producing: `flynn kafka topics create` |
| `clickhouse` | ClickHouse + Keeper | 3 replicas (1 on singleton) | Create databases with `ON CLUSTER`: `flynn clickhouse databases create` |

Postgres, MariaDB, and MongoDB use the sirenia/replica-set state machines so a primary failure can promote a replica without split-brain. Redis does not. Details: [Databases](docs/content/databases.html.md).

## HTTPS and Let's Encrypt

Enable ACME on the cluster, then request certificates per route:

```bash
sudo flynn-host acme configure --email=admin@example.com --agree-tos
sudo flynn-host acme enable-system-routes   # controller, dashboard, …
flynn route add http --auto-tls www.example.com
```

The domain must resolve to the cluster and pass HTTP-01. You can still attach your own cert with `--tls-cert` / `--tls-key`. See [Apps — HTTPS](docs/content/apps.md#https).

## Architecture (short)

```
┌─────────────┐     HTTPS/TCP      ┌────────────┐
│   clients   │ ─────────────────► │   router   │  (every host)
└─────────────┘                    └─────┬──────┘
                                         │ discoverd
     git push / docker push              ▼
┌─────────────┐                    ┌────────────┐     ┌──────────────┐
│     CLI     │ ─────────────────► │ controller │────►│  scheduler   │
│  dashboard  │                    └────────────┘     └──────┬───────┘
└─────────────┘                                              │
                                                             ▼
                                                   ┌─────────────────┐
                                                   │   flynn-host    │  libcontainer jobs
                                                   │  ZFS · flannel  │  overlay VXLAN
                                                   └─────────────────┘
```

- **flynn-host** is the only process that does not run in a container. It starts jobs, owns volumes, and speaks to the rest of the cluster
- **discoverd** is Raft-backed service discovery and leader election
- **flannel** gives every job an overlay IP
- **controller** is the app/release/formation API (Heroku-inspired)
- **blobstore** holds slugs, git caches, and image layers (Postgres by default; S3, GCS, or Azure for production)

Longer write-up: [Architecture](docs/content/architecture.html.md).

## Documentation

| Topic | Page |
| --- | --- |
| Install a cluster | [Installation](docs/content/installation.html.md) · [Manual](docs/content/installation/manual.md) · [Vagrant](docs/content/installation/vagrant.md) |
| First deploy | [Basics](docs/content/basics.md) · [Apps](docs/content/apps.md) · [Docker](docs/content/docker.md) |
| Datastores | [PostgreSQL](docs/content/databases/postgres.md) · [MariaDB](docs/content/databases/mysql.md) · [MongoDB](docs/content/databases/mongodb.md) · [Redis](docs/content/databases/redis.md) · [Kafka](docs/content/databases/kafka.md) · [ClickHouse](docs/content/databases/clickhouse.md) |
| Operate | [Production](docs/content/production.html.md) · [Security](docs/content/security.md) · [Stability](docs/content/stability.md) |
| Develop | [Development](docs/content/development.html.md) · [Contributing](CONTRIBUTING.md) |
| CLI | [CLI](docs/content/cli.md) · [`cli/README.md`](cli/README.md) |
| Docs index | [docs/README.md](docs/README.md) |

## Develop

Needs Ubuntu 24.04 with Docker, ZFS, and the packages in `setup.sh` (the Vagrant **builder** VM installs them).

```bash
git clone https://github.com/randy-girard/flynn.git
cd flynn
vagrant up builder            # Ubuntu 24.04 build VM
make                          # script/build-flynn (host binaries)
script/bootstrap-flynn        # single-node cluster from local images
make test-unit                # go test; uses Docker on macOS/Windows
```

`make test-integration` boots a nested cluster. Cluster, datastore, overlay, and upgrade changes should also run `script/vagrant-upgrade-smoke.sh`. See [Development](docs/content/development.html.md) and [AGENTS.md](AGENTS.md).

## License

[BSD 3-Clause](LICENSE). Copyright (c) 2013–2018 Prime Directive, Inc. and contributors.

The Flynn name and logo are trademarks of Prime Directive, Inc. See [Trademark guidelines](docs/content/trademark-guidelines.html.md).
