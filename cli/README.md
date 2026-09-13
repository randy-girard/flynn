# Flynn Command-Line Interface

`flynn` is the command-line client for the [controller](/controller). It deploys and manages applications, routes, and datastores.

## Installation

Pre-built binaries for Linux, macOS (Intel and Apple Silicon), and Windows are published on [GitHub Releases](https://github.com/randy-girard/flynn/releases).

```text
curl -fsSL https://github.com/randy-girard/flynn/releases/latest/download/install-flynn-cli | sudo bash
```

A specific version:

```text
curl -fsSL https://github.com/randy-girard/flynn/releases/latest/download/install-flynn-cli | sudo bash -s -- --version v2024.01.27.0
```

See [CLI documentation](/docs/content/cli.md) for cluster add, `flynn login`, and the command list.

## Usage

```text
flynn [-a app] [-c cluster] <command> [options] [arguments]
```

Run `flynn help` for commands. Host-level operations (`bootstrap`, ACME, updates) use `flynn-host` on cluster nodes.

## Credits

flynn-cli is a fork of Heroku's [hk](https://github.com/heroku/hk).
