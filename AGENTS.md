# Agent notes

This is the **randy-girard/flynn** community fork of Flynn (PaaS). Default branch is `develop`. Host OS is Ubuntu 24.04; Go is 1.24 with `-mod=vendor`.

## Colon commands

Nested `flynn` and `flynn-host` verbs are `noun:verb` (`env:get`, `plugin:install`, `volume:gc`). Extra colons only for a nested noun (`plugin:credentials:set`, `kafka:topics:create`), not a hyphenated verb (`acme:disable-system-routes`). Standalone verbs stay verbs (`backup`, `daemon`, `update`). Space form is an alias only. See `.cursor/rules/colon-commands.mdc`.

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

## Dashboard mock (sibling checkout only)

**If `../flynn-plugin-dashboard` exists**, that plugin’s local Compose stack uses `mock/` instead of this cluster. When you change `GET /cluster/stats`, `GET /cluster/jobs-stats`, `GET /apps/:id/jobs-stats`, or the JSON on `host.HostResourceStats` / `host.ContainerStats`, update `../flynn-plugin-dashboard/mock` in the same work so the local dashboard still reflects the APIs. If that sibling folder is missing, ignore this.

## Git commits

**Always** use [Conventional Commits](https://www.conventionalcommits.org/) for every commit. Do not use unstructured subjects.

Format: `<type>(<scope>): <summary>`

- **type** is one of `feat`, `fix`, `test`, `docs`, `refactor`, `perf`, `chore`, `ci`, `build`, `style`
- **scope** is the subsystem (`cli`, `controller`, `host`, `router`, `discoverd`, `plugin`, …). Omit scope only for repo-wide docs or chore
- **summary** is imperative, lowercase, and says why the change matters
- Keep subjects ≤ 72 characters; add a body when the why is not obvious
- Use regular `git commit` only. Do **not** use DCO sign-off (`git commit -s`, `Signed-off-by`)
- **Always commit logically**: one concern per commit. Do not dump an entire session or mixed features into one catch-all commit
- Keep tests and docs for that concern in the same commit. Do not mix a feature with an unrelated cleanup
- Stage whole files by path. Do not use `git add -p` or `git add -i`. If one file mixes concerns, still commit the other files separately

Examples: `feat(cli): load plugin commands from cluster catalog`, `fix(host): ignore missing volumes on destroy`, `test(router): cover ACME HTTP-01 challenge serving`

See [Development](docs/content/development.html.md) and [Contributing](CONTRIBUTING.md).
