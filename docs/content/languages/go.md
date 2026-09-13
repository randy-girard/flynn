---
title: Go
layout: docs
---

# Go

Go apps are supported on Flynn by the [Go
buildpack](https://github.com/heroku/heroku-buildpack-go) on the default
`heroku-24` stack.

## Detection

The Go buildpack is used if the repository contains any filenames ending with
`.go`.

## Dependencies

Apps should use **Go modules**. Commit `go.mod` and `go.sum` (and `vendor/` if
you vendor). The buildpack runs `go install` against the module.

Specify the toolchain with the `go` directive in `go.mod` (for example
`go 1.24.0`). If you do not pin a version, the buildpack uses a recent Go it
knows about.

The older `godep` / `Godeps.json` workflow is no longer recommended.

## Binaries

Main packages in the repo are compiled and binaries placed in `/app/bin`, which
is on `PATH`. Binaries are named after the directory that contains them. A main
package at the module root takes its name from the module path.

## Process Types

Declare process types in a `Procfile` in the repository root (`TYPE: COMMAND`).

If you have a main package at the module root and the module path ends in
`myserver`:

```text
web: myserver
```

The `web` process type has an HTTP route by default and a corresponding `PORT`
environment variable that the server should listen on.

## Building a frontend before `go install`

The Go buildpack only compiles Go; it does not run webpack, Vite, and similar
tools. If you serve a generated `dist/` directory from the same repository (for
example `//go:embed`), add an executable `bin/go-pre-compile` script in the app
root. It runs **before** `go install`. Install or invoke Node in that script,
run `npm ci` / `npm run build` in your UI directory, then let the buildpack
compile Go. See the [Heroku Go buildpack README —
hooks](https://github.com/heroku/heroku-buildpack-go/blob/main/README.md#prepost-compile-hooks).
