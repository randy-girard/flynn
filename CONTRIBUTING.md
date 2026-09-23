# Contribution Guide

We welcome community contributions to this Flynn fork.

Please read this guide and the [development docs](docs/content/development.html.md) before sending a pull request.

There are many ways to help besides code:

- Fix bugs or file issues
- Improve documentation under `docs/` and the root `README.md`

## Contributing code

Unless you are fixing a known bug, discuss the change in a GitHub issue or on [Discord](https://discord.gg/VU2ZqrPUay) first so the work matches the fork's direction.

All contributions are pull requests against **`main`** on [randy-girard/flynn](https://github.com/randy-girard/flynn). Every patch is reviewed, including patches from maintainers. At least one review is required. If tests fail, update the pull request until they pass.

## Code style

- Go code should match the output of `gofmt -s`
- Shell scripts should follow the [Google Shell Style Guide](https://google.github.io/styleguide/shell.xml)
- Commit subjects **always** use [Conventional Commits](https://www.conventionalcommits.org/): `<type>(<scope>): <summary>` (for example `feat(cli): …`, `fix(host): …`, `test(router): …`, `docs: …`)

## Commit sign-off

This fork does **not** use the Developer Certificate of Origin. Use plain `git commit`; do not add `Signed-off-by` trailers (`git commit -s`). A Conventional Commit subject is the only commit-message requirement (see [AGENTS.md](AGENTS.md) and `.cursor/rules/git-commits.mdc`).

## Pull request procedure

You need a GitHub account. See GitHub's docs on [forking](https://docs.github.com/en/get-started/quickstart/fork-a-repo) and [pull requests](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/proposing-changes-to-your-work-with-pull-requests/about-pull-requests). Target **`main`**. Before opening a PR:

1. Create a feature branch off `main`.
1. [Rebase](https://git-scm.com/book/en/v2/Git-Branching-Rebasing) onto current `main`.
1. Run `make test-unit` (and `script/vagrant-smoke.sh --item quick` when the change needs a cluster; `singleton`/`ha` before a release).
1. Run `gofmt -s` (or `make install-git-hooks` so pre-commit / pre-push run `validate-gofmt`).
1. Use a Conventional Commit subject on every commit (`<type>(<scope>): <summary>`); no DCO sign-off.

Pull requests are review requests. Maintainers will comment on style and substance.

Include tests unless the change is genuinely hard to test; if so, explain why in the commit message.

## Communication

Use [Discord](https://discord.gg/VU2ZqrPUay) or GitHub issues. This fork does not use Freenode IRC or `contact@flynn.io`.

## Conduct

Whether you are a regular contributor or a newcomer, we care about making this community a safe place.

- We are committed to a friendly, safe, and welcoming environment for all, regardless of gender, sexual orientation, disability, ethnicity, religion, or similar personal characteristic.
- Please avoid nicknames that detract from that environment.
- Be kind and courteous. There is no need to be mean or rude.
- We will exclude you from interaction if you insult, demean, or harass anyone. In particular, we do not tolerate behavior that excludes people in socially marginalized groups.
- Private harassment is also unacceptable. If you feel you have been harassed or made uncomfortable, contact a maintainer on Discord or GitHub.
- Spamming, trolling, flaming, baiting, or other attention-stealing behaviour is not welcome.
