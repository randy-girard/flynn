# Flynn documentation

These guides are meant to be read on GitHub. Links between pages are **relative
file paths** so they resolve in the repository. This fork does not host the
historical `https://flynn.io/docs/...` site, so `/docs/databases/clickhouse`
style URLs 404.

Start at the [root README](../README.md) or pick a page. Agent notes (keep docs and tests in sync with code) are in [AGENTS.md](../AGENTS.md).

## Install

- [Installation](content/installation.html.md)
- [Manual installation](content/installation/manual.md)
- [Vagrant](content/installation/vagrant.md)
- [CLI](content/cli.md)

## Using

- [Basics](content/basics.md)
- [Apps](content/apps.md)
- [Docker](content/docker.md)
- [Databases](content/databases.html.md)
  - [PostgreSQL](content/databases/postgres.md)
  - [MariaDB](content/databases/mysql.md)
  - [MongoDB](content/databases/mongodb.md)
  - [Redis](content/databases/redis.md)
  - [Kafka](content/databases/kafka.md)
  - [ClickHouse](content/databases/clickhouse.md)
- [Languages](content/languages): [Go](content/languages/go.md), [Java](content/languages/java.md), [Node.js](content/languages/nodejs.md), [PHP](content/languages/php.md), [Python](content/languages/python.md), [Ruby](content/languages/ruby.md)

## Operate

- [Production](content/production.html.md)
- [Security](content/security.md)
- [Stability](content/stability.md)
- [HTTPS / Let's Encrypt](content/apps.md#https)
- [Monitoring / OpenTelemetry](content/production.html.md#monitoring)
- [Plugins](content/plugins.md)
- [Platform security & reliability review (2026-09)](reviews/2026-09-19-platform-review.md)

## Reference

- [Architecture](content/architecture.html.md)
- [Controller API](api-examples/controller.md)
- [Roadmap](content/roadmap.md)
- [Contributing](content/contributing.md)
- [Development](content/development.html.md)
- [Trademark guidelines](content/trademark-guidelines.html.md)
