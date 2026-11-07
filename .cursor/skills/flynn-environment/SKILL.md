---
name: flynn-environment
description: >-
  Operate the FlynnWorkspace checkouts: dev cluster versus smoke, macOS unit
  tests, plugin e2e, the flynn CLI, and GitHub Actions. Use when working in
  flynn or any flynn-plugin-* repo, booting Vagrant, running make test-unit,
  vagrant-smoke, chaos, cluster/run.sh, building the CLI, pushing main, or
  watching Actions.
---

# Flynn environment

The workspace is a folder of separate git repos, not one repo. Read the current repo's `AGENTS.md` for product rules. Use this skill for how to run things.

Command details and the project-board ids are in [reference.md](reference.md).

## Pick the right check

| Question | Command | Where |
| --- | --- | --- |
| Go unit tests on a Mac | `script -q /tmp/flynn-unit-script.log script/run-unit-tests` | `flynn/` |
| Plugin unit tests | `./script/run-unit-tests` | that plugin |
| Vagrant script contracts, no VMs | `make test-vagrant` | `flynn/` |
| Boot or update the laptop cluster | `script/vagrant.sh` | `flynn/` |
| Acceptance smoke | `script/vagrant-smoke.sh --item quick` | `flynn/` |
| Fault injection on the laptop cluster | `script/vagrant.sh --yes chaos` | `flynn/` |
| Plugin CLI against the live cluster | `FLYNN=/tmp/flynn-cli cluster/run.sh --cli-only` | that plugin |
| GitHub Actions | `gh run list` then `gh run watch <id> --exit-status` | that repo |

`make test-integration` boots a nested cluster and needs Linux, ZFS, and KVM. On macOS, smoke is the stand-in. Do not start it.

## Two Vagrant worlds

Dev and smoke are separate on purpose. A bare `vagrant up` in `flynn/` boots smoke.

| | Dev (laptop loop) | Smoke |
| --- | --- | --- |
| Entry | `script/vagrant.sh` | `script/vagrant-smoke.sh` |
| State | `.vagrant-dev` | `.vagrant` |
| Subnet | `192.168.57.0/24` | `192.168.56.0/24` |
| Build dir | `./build-dev` | `./build` |
| Domain | `1.localflynn.com` | `upgrade-smoke.localflynn.com` |
| Live node | `dev-node1` at `192.168.57.20` | smoke `node1` |

Default builder RAM is 30000 MB and each node is 6144 MB. The subnets can both be up. Do not start the smoke builder while the dev builder and the dev nodes are running.

Dev update rebuilds on `dev-builder`, halts it, then `flynn-host update` on the running nodes. It does not redeploy plugins. Use `script/vagrant.sh plugin <name>` for a sibling `../flynn-plugin-<name>` checkout.

If a dev build sits on `Fetching latest tags from remote` with no further output, `build.sh` is blocked on the GitHub host key inside the guest (`git fetch --tags`, stderr discarded). On the builder, as root, append `ssh-keyscan -t ed25519 github.com` to `/root/.ssh/known_hosts`. Do not try to answer that prompt from the laptop tty.

## macOS unit tests

`script/run-unit-tests` always passes `docker -t`. From an agent shell with no tty, wrap it:

```bash
script -q /tmp/flynn-unit-script.log script/run-unit-tests
```

Docker Desktop skips ZFS volume tests. GitHub Actions on Ubuntu runs them. Plugin `./script/run-unit-tests` uses that plugin's own dev image and the same Linux-versus-Docker split.

## CLI and plugin e2e

Build a CLI without sudo:

```bash
cd flynn && go build -mod=vendor -o /tmp/flynn-cli ./cli
```

`script/vagrant.sh cli` installs to `/usr/local/bin/flynn` and needs a password. Leave the installed binary in place when sudo is unavailable. Git still calls `/usr/local/bin/flynn git-credentials`.

Point plugin e2e at the binary you built: `FLYNN=/tmp/flynn-cli cluster/run.sh --cli-only`. Web Playwright needs the live dashboard password. `flynn-dev` is not that password, and the dashboard app env does not store it. A 401 on `POST /api/session` is a missing password, not a failed CLI test.

Plugin API tasks (`/bin/flynn-*-api task ...`) run on the user app. Copy `CONTROLLER_KEY` from the plugin app's release into that task env only. Do not put the cluster controller key on an isolated database release. `flynn run` on a datastore app (`postgres-plugin`, `mysql-plugin`, and the other `*-plugin` datastore apps) stays refused.

## Git and Actions

Each checkout has its own `origin`. Push with `git push`. Never force-push.

A rejected push is usually `ci: update coverage badge [skip ci]` landing on `main` after the previous green run. `git pull --rebase origin main`, then push again. After a green run, `git pull --ff-only` so the new badge commit is local.

Wait for the whole workflow, including the coverage-badge job. `gh run watch <id> --exit-status` from that repo.

These checkouts are not GitHub remotes. Do not push them to `github.com`:

- `flynn-resource-demo` — remote `flynn` is the cluster git. Branch `test` has no upstream.
- `flynn-cursor-agent-test` — remote `flynn` is `git.flynn.linode.randygirard.com`.

Commit shape is in that repo's `AGENTS.md`. One concern per commit. Stage whole files. If a user rule names a different subject style, follow the user rule for the subject and still split concerns.

Plans and reviews go in `FlynnWorkspace/.plans/`, which is not a git repo. Worktrees go in `FlynnWorkspace/.worktrees/`.
