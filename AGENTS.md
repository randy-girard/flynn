# Agent notes

This is the **randy-girard/flynn** community fork of Flynn (PaaS). Default branch is `main` (pull requests target `main`). Host OS is Ubuntu 24.04; Go is 1.24 with `-mod=vendor`.

## Colon commands

Nested `flynn` and `flynn-host` verbs are `noun:verb` (`env:get`, `plugin:install`, `volume:gc`). Extra colons only for a nested noun (`plugin:credentials:set`, `kafka:topics:create`), not a hyphenated verb (`acme:disable-system-routes`). Standalone verbs stay verbs (`backup`, `daemon`, `update`). Space form is an alias only. See `.cursor/rules/colon-commands.mdc`.

## Keep docs and tests in sync with code

When you change behavior, **do not ship code alone**. In the same change (or an immediate follow-up in the same PR):

1. **Docs** — Update the user-facing pages that describe the behavior. At minimum check:
   - Root [README.md](README.md) if install, features, or datastore versions changed
   - [docs/content/](docs/content/) for the matching topic (apps, docker, databases, CLI, plugins, install, development, production, security)
   - [docs/content/cli.md](docs/content/cli.md) when you add, rename, or remove a `flynn` / `flynn-host` command — every canonical command is listed there in `noun:verb` form (compatibility alias registrations such as space forms or `plugin:credentials-set` are exempt), and other pages must not keep the old spelling
   - Component READMEs (`cli/`, `controller/`, `host/`, `router/`, `test/`, `script/`, …) if that component’s interface changed
   - [docs/README.md](docs/README.md) **and** [docs/docs-nav.json](docs/docs-nav.json) if you add or rename a guide
   - Plugin contract changes (manifest fields in `pkg/plugin`, install/update/uninstall flow, `official-plugins.json`, `flynn resource:expose` providers): [docs/content/plugins.md](docs/content/plugins.md), [docs/content/databases/](docs/content/databases/), and the sibling `../flynn-plugin-*/README.md` + `flynn-plugin.json` when that checkout exists
2. **Links** — Use **relative file paths** that work on GitHub (`databases/clickhouse.md`, not `/docs/databases/clickhouse`). This fork does not host the old flynn.io docs site.
3. **Tests** — Add or update coverage at the right layer:
   - Unit: `make test-unit` / `go test` next to the code
   - Shell helpers: `bats script/test` or `script/test-*.sh`
   - Smoke driver contracts: `script/test-vagrant-smoke-*.sh` if you change `script/vagrant-upgrade-smoke.sh`
   - Cluster / overlay / datastores / dockerbuilder / upgrades / membership / backup-restore: `script/vagrant-smoke.sh` (entrypoint over `script/vagrant-upgrade-smoke.sh`; narrow with `SMOKE_TOPOLOGIES` if needed)
   - Full-stack Go: `script/run-integration-tests` for `test/` suites
4. **Do not leave docs describing removed or replaced behavior** (old Ubuntu, old DB versions, `dl.flynn.io`, tup, upstart, HHVM, Python 2, godep as the default, website `/docs/...` URLs, the `develop` branch, DCO sign-off, space-form command aliases as the documented spelling).

If a change is internal-only and has no user-visible effect, say so in the PR and skip docs — still add tests when the logic can regress.

## Dashboard mock (sibling checkout only)

**If `../flynn-plugin-dashboard` exists**, that plugin’s local Compose stack uses `mock/` instead of this cluster. When you change `GET /cluster/stats`, `GET /cluster/jobs-stats`, `GET /apps/:id/jobs-stats`, or the JSON on `host.HostResourceStats` / `host.ContainerStats`, update `../flynn-plugin-dashboard/mock` in the same work so the local dashboard still reflects the APIs. If that sibling folder is missing, ignore this.

## Isolated worktrees

Put extra checkouts in **one** place, the workspace-level `.worktrees/` directory (sibling of this `flynn/` clone), never inside the repo and never as a second top-level clone:

```
../.worktrees/<short-name>
```

Example: `git worktree add ../.worktrees/flynn-sirenia-sync -b fix/sirenia-sync-start-first`. Plugin repos use `../../.worktrees/<short-name>` from their checkout. Do not create `worktrees/`, `flynn/.kilo/worktrees/`, or sibling folders named `flynn-<topic>` / `flynn-plugin-dashboard-*`. Remove the worktree (and its local branch) after the work is merged.

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
