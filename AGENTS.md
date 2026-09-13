# Agent notes

This is the **randy-girard/flynn** community fork of Flynn (PaaS). Default branch is `develop`. Host OS is Ubuntu 24.04; Go is 1.24 with `-mod=vendor`.

## Keep docs and tests in sync with code

When you change behavior, **do not ship code alone**. In the same change (or an immediate follow-up in the same PR):

1. **Docs** — Update the user-facing pages that describe the behavior. At minimum check:
   - Root [README.md](README.md) if install, features, or datastore versions changed
   - [docs/content/](docs/content/) for the matching topic (apps, docker, databases, CLI, install, development, production, security)
   - Component READMEs (`cli/`, `controller/`, `test/`, …) if that component’s interface changed
   - [docs/README.md](docs/README.md) if you add or rename a guide
2. **Links** — Use **relative file paths** that work on GitHub (`databases/clickhouse.md`, not `/docs/databases/clickhouse`). This fork does not host the old flynn.io docs site.
3. **Tests** — Add or update coverage at the right layer:
   - Unit: `make test-unit` / `go test` next to the code
   - Shell helpers: `bats script/test` or `script/test-*.sh`
   - Smoke driver contracts: `script/test-vagrant-smoke-*.sh` if you change `script/vagrant-upgrade-smoke.sh`
   - Cluster / overlay / datastores / dockerbuilder / upgrades / membership / backup-restore: `script/vagrant-upgrade-smoke.sh` (narrow with `SMOKE_TOPOLOGIES` if needed)
   - Full-stack Go: `script/run-integration-tests` for `test/` suites
4. **Do not leave docs describing removed or replaced behavior** (old Ubuntu, old DB versions, `dl.flynn.io`, tup, upstart, HHVM, Python 2, godep as the default, website `/docs/...` URLs).

If a change is internal-only and has no user-visible effect, say so in the PR and skip docs — still add tests when the logic can regress.

See [Development](docs/content/development.html.md) and [Contributing](CONTRIBUTING.md).
