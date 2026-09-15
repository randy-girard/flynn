---
title: Contributing
layout: docs
toc_min_level: 2
---

# Contributing

Contributions to this Flynn fork are welcome. Please read this page and the [development guide](development.html.md) before opening a pull request.

There are many ways to help besides code: file issues, reproduce bugs, and improve documentation.

## Talk first

Unless you are fixing a known bug, open a GitHub issue (or ask on [Discord](https://discord.gg/VU2ZqrPUay)) before a large change so the work fits the fork's direction.

All patches are reviewed, including patches from maintainers. At least one review is required. Authors are expected to keep the pull request green until it merges.

## Code style

* Go must match `gofmt -s`
* Shell scripts should follow the [Google Shell Style Guide](https://google.github.io/styleguide/shell.xml)
* Commit subjects **always** use [Conventional Commits](https://www.conventionalcommits.org/): `<type>(<scope>): <summary>` (for example `feat(cli): …`, `fix(host): …`, `test(router): …`, `docs: …`)

## Developer's Certificate of Origin

Every commit must include a DCO sign-off (`git commit -s`):

```text
Signed-off-by: Jane Example <jane@example.com>
```

Anonymous or pseudonymous contributions are not accepted.

## Pull request procedure

1. Branch from `develop` (not a long-lived personal copy of `master`).
1. Rebase onto current `develop`.
1. Run the tests you can (`make test-unit`; integration or `script/vagrant-upgrade-smoke.sh` if the change needs a cluster).
1. Run `gofmt -s` (or `make install-git-hooks` so pre-commit / pre-push run `validate-gofmt`).
1. Sign off every commit.
1. Use a Conventional Commit subject on every commit (`<type>(<scope>): <summary>`).
1. Include tests, or explain in the commit message why the change is hard to test.

Target the `develop` branch of [randy-girard/flynn](https://github.com/randy-girard/flynn).

## Communication

Use [Discord](https://discord.gg/VU2ZqrPUay) or GitHub issues. The original Freenode `#flynn` channel and `contact@flynn.io` are not used by this fork.

## Conduct

Be kind. Harassment, insults, and exclusionary behavior are not tolerated. If you have a conduct concern, contact a maintainer privately on Discord or GitHub.

The full DCO text and conduct notes live in [`CONTRIBUTING.md`](../../CONTRIBUTING.md) at the repository root.
