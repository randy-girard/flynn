---
title: Compare platforms
layout: docs
toc_min_level: 2
---

# Compare platforms

Flynn next to Heroku, Vercel, Fly.io, Render, Canine, and Railway. Railway is included because it is the other git-push platform teams put in this list.

**Yes** means the platform ships it. **Partial** means a narrower version, a plugin, a paid tier, or something you operate yourself. **No** means it is not a standard feature in that product's public docs. Facts are from those docs as of October 2026. Flynn's column is this project today, including optional plugins.

Sources: [Heroku dynos](https://www.heroku.com/dynos/), [pipelines](https://devcenter.heroku.com/articles/pipelines), [Review Apps](https://devcenter.heroku.com/articles/github-integration-review-apps), [Vercel projects](https://vercel.com/docs/projects), [Fly.io storage](https://fly.io/docs/database-storage-guides), [Render service types](https://render.com/docs/service-types), [Canine](https://canine.sh/), [Railway features](https://railway.com/features.md).

## Who runs it

| Feature | Flynn | Heroku | Vercel | Fly.io | Render | Canine | Railway |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Where apps run | **Yes.** Ubuntu hosts you administer | **No.** Heroku's cloud | **No.** Vercel's cloud | **No.** Fly.io's cloud | **No.** Render's cloud | **Yes.** Your Kubernetes, or Canine Cloud | **No.** Railway's cloud |
| Who operates it | **Yes.** You | **No.** Heroku | **No.** Vercel | **No.** Fly.io | **No.** Render | **Partial.** You, when self-hosted | **No.** Railway |
| Open source | **Yes.** BSD-3-Clause | **No.** Proprietary | **No.** Proprietary | **No.** Proprietary | **No.** Proprietary | **Yes.** MIT | **No.** Proprietary |
| More than one region | **No.** One low-latency network | **Yes.** Regions, plus Private Spaces | **Yes.** CDN and many function regions | **Yes.** Machines in many regions | **Yes.** Services placed per region | **Yes.** Every cluster you connect | **Yes.** Replicas across regions |

## From a repository to a release

| Feature | Flynn | Heroku | Vercel | Fly.io | Render | Canine | Railway |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Git deploy | **Yes.** git push, or the GitHub plugin | **Yes.** git push and GitHub | **Yes.** Git is the main path | **Yes.** Deploy from the repo | **Yes.** Git or a container registry | **Yes.** GitHub and GitLab | **Yes.** GitHub, CLI, or an image |
| Dockerfile on the platform | **Yes.** Same git push, BuildKit | **Partial.** heroku.yml, Cedar only | **Partial.** Not a general image host | **Yes.** Default builder | **Yes.** | **Yes.** | **Yes.** Or Nixpacks |
| Buildpacks | **Partial.** Heroku-24 packs. No .NET pack | **Yes.** Official packs, including .NET | **No.** Framework builders | **Yes.** Cloud Native Buildpacks | **Partial.** Native runtimes, or Docker | **Yes.** Buildpacks or a Dockerfile | **Yes.** Nixpacks |
| Image you already built | **Yes.** | **Partial.** Container Registry, Cedar, amd64 | **No.** | **Yes.** | **Yes.** | **Partial.** Builds from the repo | **Yes.** Public or private registries |
| Pull-request app | **Partial.** Branch or preview remote. Not every webhook yet | **Yes.** Review Apps | **Yes.** Every non-production push | **No.** Not a built-in PR app | **Partial.** Pro plan. Copies have no production data | **Yes.** Feature-branch environments | **Yes.** PR environments |
| Built-in test runner | **No.** A deploy can wait for GitHub Checks | **Partial.** Heroku CI on Cedar, not Fir | **No.** | **No.** | **No.** | **No.** | **No.** |

## Processes, scale, and disk

| Feature | Flynn | Heroku | Vercel | Fly.io | Render | Canine | Railway |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Web and workers | **Yes.** Any process type | **Yes.** Web, worker, one-off | **Partial.** Functions, cron, queues, daemons | **Yes.** Process groups from one image | **Yes.** Web, private, worker, cron | **Yes.** Web, worker, cron | **Yes.** Services, cron, one-off |
| Scale out | **Yes.** Per process type | **Partial.** Standard dynos and above | **Yes.** Automatic | **Yes.** Count and regions | **Yes.** | **Yes.** Kubernetes replicas | **Yes.** Replicas |
| Scale to zero | **No.** | **Partial.** Eco dynos sleep | **Yes.** | **Yes.** Optional autostop | **Partial.** Free instances sleep | **No.** | **Yes.** Optional |
| Rollback | **Yes.** Automatic and manual | **Yes.** | **Yes.** Promote an earlier deployment | **Yes.** | **Yes.** | **Yes.** | **Yes.** |
| One-off command | **Yes.** | **Yes.** | **No.** | **Yes.** Shell on a Machine | **Yes.** Shell | **Yes.** One-off pod | **Yes.** |
| Scheduled jobs | **Partial.** Scheduler plugin | **Partial.** Scheduler add-on | **Yes.** Cron Jobs | **Yes.** | **Yes.** Cron jobs | **Yes.** | **Yes.** |
| Autoscaling | **Partial.** Web only, response time, plugin | **Partial.** Performance dynos | **Yes.** | **Yes.** Autostart and manual count | **Yes.** CPU and memory | **Partial.** Scale the cluster | **Yes.** CPU and memory |
| Disk that survives restart | **Partial.** ZFS volume. Not in the cluster backup | **No.** Wiped when the dyno restarts | **No.** | **Partial.** Local volume, one Machine, not replicated | **Partial.** Persistent disk on eligible services | **Yes.** Volumes | **Yes.** Volumes |

## How services reach each other

| Feature | Flynn | Heroku | Vercel | Fly.io | Render | Canine | Railway |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Custom domain and HTTPS | **Yes.** Your certificate, or the Let's Encrypt plugin | **Yes.** Automated Certificate Management | **Yes.** Automatic. Your own certificate is Enterprise | **Yes.** Let's Encrypt or yours | **Yes.** | **Yes.** Let's Encrypt | **Yes.** |
| Public TCP | **Yes.** Including a database | **No.** HTTP and HTTPS | **No.** | **Yes.** | **No.** | **Partial.** Whatever the cluster ingress allows | **Yes.** TCP proxy |
| Private network between services | **No.** Call another app through a route | **Partial.** Private Spaces | **No.** | **Yes.** Private IPv6 mesh | **Yes.** Same workspace and region | **Yes.** The cluster network | **Yes.** Within a project |
| Apps blocked from each other | **Yes.** Except routes and injected database URLs | **Partial.** Private Spaces and Shield | **No.** Shared platform | **No.** Org-wide private network | **Partial.** Private to the workspace and region | **Partial.** Kubernetes tenancy | **Partial.** Private to the project |

## Databases and catalogs

Flynn's engines suit development, staging, and small production. They are not tuned for very large datasets or high write volume. See [Databases](databases.html.md) and [Backups](production.html.md#backups).

| Feature | Flynn | Heroku | Vercel | Fly.io | Render | Canine | Railway |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Postgres | **Partial.** Plugin. One node. Optional follower, not automatic failover unless you add one. Not in the cluster backup | **Yes.** HA, forks, and point-in-time rollback on higher plans | **Partial.** Marketplace | **Yes.** Managed, HA, backups | **Partial.** HA and recovery on larger paid plans. Free expires | **Partial.** Add-on or Helm chart. You operate it | **Yes.** Managed |
| Redis | **Partial.** No automatic failover. Not in the cluster backup | **Yes.** HA on higher plans | **Partial.** Marketplace | **Partial.** Upstash | **Yes.** Key Value | **Yes.** Add-on | **Yes.** |
| Kafka | **Partial.** Three brokers. Topic data is not in the cluster backup | **Yes.** Managed | **No.** | **No.** | **No.** | **Partial.** Helm chart, not a managed service | **No.** |
| MySQL-compatible | **Yes.** MariaDB 10.11, automatic failover | **Partial.** Marketplace | **Partial.** Marketplace | **No.** | **No.** | **Partial.** Helm or add-on | **No.** |
| MongoDB | **Yes.** Replica set, automatic failover | **Partial.** Marketplace | **Partial.** Marketplace | **No.** | **No.** | **Partial.** Helm or add-on | **Yes.** Template |
| ClickHouse | **Partial.** Three replicas. Tables are not in the cluster backup | **No.** | **No.** | **No.** | **Partial.** Run it as a private service | **Partial.** Helm chart | **Yes.** Template |
| Third-party catalog | **No.** First-party plugins only | **Yes.** Elements | **Yes.** Marketplace | **Partial.** Partners such as Upstash and Tigris | **No.** | **Yes.** Helm charts | **Partial.** Templates, not a partner store |
| Salesforce sync | **No.** | **Yes.** Heroku Connect. Not on review or CI apps | **No.** | **No.** | **No.** | **No.** | **No.** |

## People, logs, and metrics

| Feature | Flynn | Heroku | Vercel | Fly.io | Render | Canine | Railway |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Team roles | **Yes.** View, Deploy, Manage, Admin. Custom roles need the enterprise plugin | **Yes.** Teams and Enterprise | **Yes.** | **Yes.** Organizations | **Yes.** | **Yes.** Teams and RBAC | **Yes.** RBAC |
| Single sign-on | **Partial.** Enterprise plugin: OIDC or SAML | **Partial.** Enterprise | **Partial.** Enterprise | **Partial.** Organization plans | **Partial.** Higher plans | **Yes.** SAML, OIDC, or LDAP | **Partial.** Enterprise |
| Logs and metrics | **Yes.** Tail, syslog, alerts. Host metrics via the OpenTelemetry plugin | **Partial.** Retention and alerts depend on the dyno | **Yes.** Function logs and observability | **Yes.** | **Yes.** | **Partial.** Kubernetes logs and metrics | **Yes.** Logs, metrics, and an OpenTelemetry collector |

## What the matrix leaves out

The Postgres your apps use is a plugin: one node and one volume. A follower is optional, and automatic failover stays off until you add one. That database is not the highly available Postgres Flynn uses for itself, and it is not in the cluster backup. MariaDB and MongoDB do fail over on their own.

Review Apps can be created for every pull request. Heroku CI and the Container Registry are Cedar-generation features and are not on Fir. Heroku Connect syncs Postgres with Salesforce and opts out of review and CI apps.

Vercel is a framework and functions platform on a global CDN. Databases are marketplace products. It is not the place you run an ordinary long-lived Docker formation.

Machines can stop while idle and start on the next request. A volume is local disk on one Machine, and Fly.io does not replicate it for you. Managed Postgres is a separate highly available service. Redis is Upstash.

Preview environments need a Pro workspace and do not copy production data. Postgres point-in-time recovery and high availability are on larger paid instances. A free Postgres instance expires.

Canine sits on Kubernetes you already have, or on Canine Cloud. Add-ons and Helm charts supply databases. Canine does not operate those databases the way a hosted Postgres service does.

Railway builds with Nixpacks or a Dockerfile, opens a PR environment per pull request, and offers templates for Postgres, Redis, MongoDB, and ClickHouse. It is a hosted service, not something you install on your own machines.
