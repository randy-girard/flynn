---
title: Stability
layout: docs
toc_min_level: 2
---

# Stability

Every user has different requirements when it comes to stability.

The term "production ready" means very different things to different people.
We'd like to share what it means to us.

We believe software should only be described as "production ready" when it's
actually ready. Here's what we mean:

_The software is observed not to have a negative impact on SLAs in production.
Production-grade software also has stable lifecycle tooling including the
ability to update, monitor, debug, backup, and restore without having a negative
impact on uptime. We promise this is the only way we'll ever use the term._

This repository is a community fork of Flynn. Treat it as a work in progress:
try it, read [Security](security.md) and [Production](production.html.md), and
decide what is acceptable for your workload.

## Releases

Binaries and cluster images are published as [GitHub
Releases](https://github.com/randy-girard/flynn/releases) for this fork. There
is no longer a Flynn-operated `releases.flynn.io` channel for these builds.

The original project distinguished nightly and monthly stable channels. This
fork tags releases as they are cut; read the release notes for what changed.

Flynn currently has [security considerations](security.md) you should take
into account.

The built-in database appliances are intended for staging, development, and
small-scale production. They are not yet optimized for high write volume or
very large datasets.

## Announcements

Watch the GitHub repository and join [Discord](https://discord.gg/VU2ZqrPUay)
for discussion. The historical Mailchimp release list was for the original
project and is not used by this fork.
