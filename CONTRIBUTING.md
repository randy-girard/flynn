# Contribution Guide

We welcome community contributions to this Flynn fork.

Please read this guide and the [development docs](docs/content/development.html.md) before sending a pull request.

There are many ways to help besides code:

- Fix bugs or file issues
- Improve documentation under `docs/` and the root `README.md`

## Contributing code

Unless you are fixing a known bug, discuss the change in a GitHub issue or on [Discord](https://discord.gg/VU2ZqrPUay) first so the work matches the fork's direction.

All contributions are pull requests against **`develop`** on [randy-girard/flynn](https://github.com/randy-girard/flynn). Every patch is reviewed, including patches from maintainers. At least one review is required. If tests fail, update the pull request until they pass.

## Code style

- Go code should match the output of `gofmt -s`
- Shell scripts should follow the [Google Shell Style Guide](https://google.github.io/styleguide/shell.xml)
- Commit subjects use a subsystem prefix (for example `controller:`, `fix(host):`, `docs:`)

## Developer's Certificate of Origin

All contributions must include acceptance of the DCO:

```text
Developer Certificate of Origin
Version 1.1

Copyright (C) 2004, 2006 The Linux Foundation and its contributors.
660 York Street, Suite 102,
San Francisco, CA 94110 USA

Everyone is permitted to copy and distribute verbatim copies of this
license document, but changing it is not allowed.


Developer's Certificate of Origin 1.1

By making a contribution to this project, I certify that:

(a) The contribution was created in whole or in part by me and I
    have the right to submit it under the open source license
    indicated in the file; or

(b) The contribution is based upon previous work that, to the best
    of my knowledge, is covered under an appropriate open source
    license and I have the right under that license to submit that
    work with modifications, whether created in whole or in part
    by me, under the same open source license (unless I am
    permitted to submit under a different license), as indicated
    in the file; or

(c) The contribution was provided directly to me by some other
    person who certified (a), (b) or (c) and I have not modified
    it.

(d) I understand and agree that this project and the contribution
    are public and that a record of the contribution (including all
    personal information I submit with it, including my sign-off) is
    maintained indefinitely and may be redistributed consistent with
    this project or the open source license(s) involved.
```

To accept the DCO, add this line to each commit message (`git commit -s` does this for you):

```text
Signed-off-by: Jane Example <jane@example.com>
```

Anonymous or pseudonymous contributions are not accepted.

## Pull request procedure

You need a GitHub account. See GitHub's docs on [forking](https://docs.github.com/en/get-started/quickstart/fork-a-repo) and [pull requests](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/proposing-changes-to-your-work-with-pull-requests/about-pull-requests). Target **`develop`**. Before opening a PR:

1. Create a feature branch off `develop`.
1. [Rebase](https://git-scm.com/book/en/v2/Git-Branching-Rebasing) onto current `develop`.
1. Run `make test-unit` (and integration or `script/vagrant-upgrade-smoke.sh` when the change needs a cluster).
1. Run `gofmt -s`.
1. Sign off every commit (see above).
1. Use a subsystem prefix in each commit subject.

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
