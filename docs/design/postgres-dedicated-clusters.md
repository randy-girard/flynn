# Dedicated PostgreSQL Clusters & Heroku-Style Database Management

## Current Architecture

Flynn deploys **one shared PostgreSQL cluster** during bootstrap:

- A single `postgres` app with 3 database processes (primary + sync + async) and 2 API processes (`web`)
- All `flynn resource add postgres` calls hit the same API, which creates a user/database pair on the shared cluster
- Sirenia manages HA: automatic failover, streaming replication, synchronous commit
- `flynn-postgres-api` is a thin HTTP service that does `CREATE USER` / `CREATE DATABASE` / `DROP DATABASE`

## Goal

Offer Heroku-style PostgreSQL management:

1. Create standalone postgres clusters independent of apps
2. Attach clusters to apps with managed `_URL` env vars
3. Support follower databases (read replicas)
4. Support version upgrades via replication changeover

## What It Takes to Deploy a Separate Cluster Per Resource

### 1. Dynamic Appliance Deployment

Currently the postgres appliance is deployed once during bootstrap via `manifest_template.json`. A new controller endpoint (e.g., `POST /postgres/clusters`) would need to:

1. Create a new Flynn app (e.g., `postgres-<short-id>`)
2. Create a release using the existing postgres image artifact
3. Generate a unique password and set env vars (`FLYNN_POSTGRES`, `PGPASSWORD`, `SIRENIA_PROCESS`, `SINGLETON`)
4. Set up the formation (3 postgres + 2 web for HA, or 1+1 for dev)
5. Wait for sirenia to report ReadWrite
6. Register a provider with the controller pointing to the new API URL

### 2. Discoverd Service Naming

Each cluster needs a unique discoverd service name (e.g., `postgres-<uuid>`) so `leader.<service>.discoverd` resolves correctly. The `FLYNN_POSTGRES` env var already controls this.

### 3. Resource Tracking Changes

The controller needs new data models:

- `database_clusters` table: `id`, `app_id`, `plan`, `version`, `state`, `created_at`
- `database_attachments` table: `app_id`, `cluster_id`, `env_prefix`, `resource_id`

### 4. Volume Management

Each cluster's 3 postgres processes each need their own persistent volume. Flynn's volume system already handles this per-job, so no fundamental changes needed.

## Tradeoffs: Shared vs. Dedicated

| Factor | Shared Cluster | Dedicated Cluster |
|--------|---------------|-------------------|
| Resource Usage | ~5 processes total | ~5 processes **per app** |
| Memory | ~300MB shared | ~300MB per cluster (~900MB across 3 hosts) |
| Isolation | Noisy neighbor risk | Full isolation |
| Security | Shared catalog (mitigated by REVOKE CONNECT fix) | Separate processes and data dirs |
| Failover blast radius | One failure affects all apps | Isolated to one app |
| Operational complexity | One cluster to monitor | N clusters to monitor |
| Provisioning speed | Instant (CREATE DATABASE) | 30-60s (spin up processes, wait for replication) |
| Extension management | Shared | Per-cluster |
| Version upgrades | All-or-nothing | Per-app |

## Heroku-Style Design

### 1. Standalone Cluster Creation

```
flynn pg:create [--plan <plan>] [--version <version>]
```

- Creates a new Flynn app (`postgres-<short-id>`)
- Deploys the postgres image with sirenia strategy
- Waits for ReadWrite
- Registers as a provider in the controller
- Returns a cluster ID like `postgres-elegant-walrus`

### 2. Attaching Clusters to Apps

```
flynn pg:attach <cluster-name> --app <app> [--as <NAME>]
```

- Provisions a database/user on the target cluster (same as today's `createDatabase`)
- Creates an attachment record linking cluster to app
- Sets managed env vars: `<NAME>_URL`, `<NAME>_HOST`, etc.
- Default `<NAME>` is `DATABASE` (so `DATABASE_URL`); additional attachments get auto-names like `POSTGRESQL_AMBER`
- Managed env vars cannot be overwritten via `flynn env set`

**Managed env var enforcement** requires:
- Modifying the release creation path in the controller to check if a key is owned by an attachment
- Adding a `protected_keys` concept to the resource/release model

### 3. Follower Databases (Read Replicas)

```
flynn pg:follow <cluster-name> [--plan <plan>]
```

Maps to sirenia's existing replication model:
- Creates a new app with a single postgres process
- Configures as async standby using `primary_conninfo`
- Registers as a separate (read-only) cluster attachable to apps
- Sets `default_transaction_read_only = on`

**Key challenge**: Sirenia manages replication within a single app's formation. Cross-app replication requires either:
- A new "external standby" mode in sirenia connecting to a different service's leader
- Or bypassing sirenia and configuring PostgreSQL streaming replication directly

**Promoting a follower** (`flynn pg:unfollow <follower-cluster>`):
- Removes `standby.signal` and restarts as standalone primary
- Detaches from leader's replication
- Follower becomes an independent writable cluster

### 4. Version Upgrades via Replication Changeover

```
flynn pg:upgrade <cluster-name> --version 17
```

Uses follower-based upgrade pattern:
1. Create follower cluster running target PostgreSQL version
2. Wait for follower to sync
3. Put leader in read-only mode
4. Promote follower
5. Update all attachments to point to new cluster
6. Decommission old cluster

**Major version challenge**: Physical streaming replication (what sirenia uses) doesn't work across major versions. Cross-version upgrades require **logical replication** (`CREATE PUBLICATION` / `CREATE SUBSCRIPTION`), supported in PostgreSQL 10+.

## Phased Implementation Plan

### Phase 1: Shared Cluster Improvements ✅ (Done)

- `REVOKE CONNECT` isolation in `flynn-postgres-api/main.go`
- Covers ~80% of use cases with zero additional resource cost

### Phase 2: Standalone Clusters + Attachments (~2-3 weeks)

- New controller DB migrations (`database_clusters`, `database_attachments`)
- New CLI commands: `flynn pg:create`, `flynn pg:attach`, `flynn pg:detach`, `flynn pg:info`, `flynn pg:destroy`
- New controller API endpoints for cluster CRUD and attachment management
- Managed env var enforcement
- Keep `flynn resource add postgres` as backward-compatible shortcut

Key files to modify:
- `controller/data/` — new repos for clusters and attachments
- `controller/` — new API routes
- `cli/` — new pg subcommands
- `controller/client/v1/client.go` — new client methods
- `bootstrap/manifest_template.json` — register shared cluster as default

### Phase 3: Followers and Upgrades (~3-4 weeks)

- Sirenia external standby mode or separate replication manager
- Follower creation, promotion, unfollowing
- Logical replication support for version upgrades
- Upgrade orchestration workflow

Key files to modify:
- `pkg/sirenia/state/state.go` — external standby role
- `appliance/postgresql/process.go` — logical replication config
- `cli/` — `flynn pg:follow`, `flynn pg:unfollow`, `flynn pg:upgrade`

## Reference: Heroku Postgres Concepts

- **Followers**: Read-only replicas streaming changes from leader. Can be promoted (`pg:unfollow`) to independent writable databases.
- **Forks**: Point-in-time snapshots, independent writable copies for testing migrations.
- **Attachments**: A database can be attached to multiple apps. Each attachment creates a config var.
- **Plans**: Different tiers with different resource allocations.
- **Version upgrades**: Follower-based changeover — create follower on new version, sync, promote.

Sources:
- https://devcenter.heroku.com/articles/heroku-postgres-follower-databases
- https://devcenter.heroku.com/articles/heroku-postgres-fork

