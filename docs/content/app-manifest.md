---
title: App manifest
layout: docs
toc_min_level: 2
---

# App manifest

`flynn-app.json` at the repository root provisions an app the way a Heroku
`app.json` does. The filename is `flynn-app.json`. Container image builds use
a separate `flynn.yml` (ignored on the buildpack stack). A repository may
contain both. Ongoing migrations stay in the Procfile `release` process.

The file is applied in two places:

1. `flynn apps:create <name> --manifest flynn-app.json` (a path, or `-` for
   stdin). v1 apply ignores `catalog`. A deploy button can come later.
2. Automatically on the first code deploy of an app that is still untouched,
   when the revision has `flynn-app.json` at the root.

A later `git push`, `flynn docker:push`, and `pipeline:promote` do not read
the file. Promote copies the artifact and process commands and keeps the
target's env, runtime, and formation.

`apps:create` leaves an untouched app: no release, no resources, strategy
`all-at-once`, deploy timeout 120, protected false, meta limited to
controller-managed keys, and the included `{app}.{cluster}` HTTP route.
Pipeline membership does not change that. The first `git push` (and any other
receive, including a preview app) applies the file before the build, so the
Procfile `release` command sees add-on URLs and manifest env. Formation
quantities replace the implicit `web=1`.

The app is already configured, and the file is skipped, when any of these is
true: a release exists (other than the pipeline preview seed release), a
resource is attached, a route exists besides the included HTTP route and the
preview route, strategy is not `all-at-once`, deploy timeout is not 120,
`protected` is true, meta contains anything other than controller-managed
keys, a GitHub repo is connected (except on a preview app, which the pipeline
connects before the first receive), or `meta["flynn-app.json"]` is `applied`.
The skip line is:

```
-----> flynn-app.json present, app already configured; skipping
```

A first deploy with no file deploys as today. A later commit that adds the
file does not apply it, because the app now has a release.

An apply sets `meta["flynn-app.json"]` to `pending` before any add-on is
created, then to `applied` when it finishes. A push that fails after `pending`
resumes on the next push: it does not create a second add-on for the same
`type` and `as`, and it does not add a route whose domain is already present.
`scripts.postdeploy` runs at most once. A non-zero exit fails that deploy and
is recorded; the next push does not run it again and does not provision the
add-ons again. `apps:create --manifest` that fails before postdeploy deletes
the new app. A first-push apply does not delete the app.

Invalid `strategy`, an unknown `stack`, a path route, `image` on `heroku-24`,
and a `github` block without the GitHub plugin fail before any add-on is
provisioned.

## Fields

| Field | Apply |
| --- | --- |
| `name` | Template title, 30 characters max, display only. The cluster app name is chosen at create time. |
| `description` | Stored as app metadata. |
| `catalog` | `keywords`, `website`, `logo`, `success_url`. Deploy-button metadata. v1 apply ignores it. |
| `repository` | Git URL. With the GitHub plugin, the default for `github.repository` when the URL is on github.com. |
| `stack` | `heroku-24` (default) or `container`. Sets `FLYNN_STACK`. |
| `buildpacks` | `[{url}]`. Used when the stack is `heroku-24` and the repo has no `.buildpacks`. One URL becomes `BUILDPACK_URL`. Several are written as `.buildpacks` (slugbuilder uses the first URL). A `.buildpacks` file in the repo wins. Ignored on `container`. |
| `image` | Prebuilt image for `stack: container` on `apps:create --manifest` when there is no git build. Invalid on `heroku-24`. A git deploy ignores it. |
| `env` | String, or `{description, value, required, generator}`. `generator` is `secret` (64 hex characters). `required` defaults to true. Required with no value and no generator fails unless the operator already set it. |
| `formation.<type>.quantity` | `flynn scale`. Required when the entry is present. Ignored on `release`. |
| `formation.<type>.runtime` | Named runtime: `small` (512MB / 500 cpu), `medium` (1GB / 1000), `large` (2GB / 2000), or a custom runtime. |
| `formation.<type>.tags` | Host placement tags. A non-admin apply needs `runtime:allow-user-scale-tags`. The cluster key bypasses that. |
| `formation.<type>.limits` | Raw `limit:set` (`cpu`, `memory`, `max_fd`, `temp_disk`). Applied only when the cluster allows custom limits. |
| `formation.<type>.volumes` | `{path, delete_on_stop}` merged onto that process type. `path` is required. |
| `addons` | String, or `{type, as, options}`. `flynn addons:create`. No plan tiers. `options` match `addons:create` (`runtime` default `small` for databases, `follow`, `replication`, `auto_failover`, and `cpu` / `memory` / `disk` only when that type allows custom sizes and a cluster admin enabled them). `as` matches `--as` (`ANALYTICS` becomes `ANALYTICS_URL`). The provider still injects `DATABASE_URL`, `REDIS_URL`, and the other default URL when `as` is omitted. Do not also set those under `env`. Order is array order. Apply returns once the instance is scheduled. |
| `scripts.postdeploy` | One-shot after the first create. String or `{command, runtime}`. |
| `scripts.preview_destroy` | One-shot when a pipeline preview app is destroyed, before the app is removed. No-op when the app is not a preview. `flynn pipeline:preview-destroy <pipeline> <app>` runs it. |
| `environments` | `development`, `staging`, `production`, `preview` only. Heroku `review` maps to `preview`. Each value replaces whole root keys that are present (`env`, `formation`, `addons`, `routes`, `scripts`, `stack`, `buildpacks`, `image`, `github`, `strategy`, `deploy_timeout`, `protected`). Keys that are absent stay at the root. This is not a deep merge: a `preview` block that sets `env` and omits `WEB_CONCURRENCY` does not keep the root `WEB_CONCURRENCY`. Preview env values and script commands may use `{{pr}}`, `{{branch}}`, `{{sha}}`, `{{sha_short}}`, `{{app}}`, `{{pipeline}}`, `{{owner}}`, `{{repo}}`, `{{base}}`, `{{cluster}}`. |
| `routes` | Extra HTTP or TCP routes. The included app route is not listed. `type` is `http` or `tcp`. `domain` is required for HTTP. `service` defaults to the app web service. `port`, `sticky` (HTTP), `leader`, `https` (Let's Encrypt when that is enabled), `tls_mode` (`passthrough` or `terminate` for TCP), `drain_backends` (default true; false matches `--no-drain-backends`), `disable_keep_alives` (HTTP). Path-based HTTP routes are rejected. |
| `github` | Applied only when `flynn-plugin-github` is installed. Otherwise apply fails. `repository` is `owner/repo`. `branch` defaults to the repository default, then `main`. `auto_deploy`, `wait_checks`. |
| `strategy` | `all-at-once`, `one-by-one`, `one-down-one-up`, or `in-batches`. `sirenia` and `discoverd-meta` are rejected. Omitted leaves the strategy already on the app. `apps:create` sets `all-at-once`. |
| `deploy_timeout` | Seconds. The controller default is 120. |
| `protected` | When true, `flynn apps:destroy` and `flynn scale` to zero are refused. Stored as app meta `protected=true`. |
| `schema_version` | Optional integer. `1` when omitted. Unknown versions fail apply. |

## Buildpack example

```json
{
  "schema_version": 1,
  "name": "Example",
  "description": "Example app",
  "catalog": {
    "keywords": ["example"],
    "website": "https://example.com",
    "logo": "https://example.com/logo.png",
    "success_url": "/welcome"
  },
  "repository": "https://github.com/acme/example",
  "stack": "heroku-24",
  "buildpacks": [
    {"url": "https://github.com/heroku/heroku-buildpack-nodejs"}
  ],
  "env": {
    "SECRET_TOKEN": {"description": "session secret", "generator": "secret"},
    "WEB_CONCURRENCY": {"description": "web processes", "value": "1", "required": false}
  },
  "formation": {
    "web": {
      "quantity": 1,
      "runtime": "small",
      "tags": {"disk": "ssd"}
    },
    "release": {"quantity": 1}
  },
  "addons": [
    "redis",
    {"type": "mongodb", "as": "MONGO"},
    {"type": "postgres", "options": {"runtime": "small"}}
  ],
  "routes": [
    {"type": "http", "domain": "example.com", "https": true}
  ],
  "github": {
    "repository": "acme/example",
    "branch": "main",
    "auto_deploy": true,
    "wait_checks": true
  },
  "strategy": "all-at-once",
  "deploy_timeout": 120,
  "protected": false,
  "scripts": {
    "postdeploy": "bundle exec rake bootstrap",
    "preview_destroy": "bundle exec rake preview:destroy"
  },
  "environments": {
    "preview": {
      "env": {
        "SECRET_TOKEN": {"generator": "secret"},
        "APP_ENV": "preview-{{branch}}"
      },
      "formation": {"web": {"quantity": 1, "runtime": "small"}}
    },
    "production": {
      "formation": {"web": {"quantity": 2, "runtime": "medium"}}
    }
  }
}
```

`flynn apps:create example` followed by `git push` of a repo with that file
provisions Redis, MongoDB, and Postgres and sets `SECRET_TOKEN` before the
build. The release command sees `REDIS_URL`, `MONGO_URL`, `DATABASE_URL`, and
`SECRET_TOKEN`. The web formation replaces the implicit `web=1`. The same
push after `flynn env:set`, `flynn addons:create`, `flynn route:add`, or
`flynn scale` prints the skip line and deploys the code without applying the
file.

A preview app's first receive applies `environments.preview`. The preview
`env` replaces the root `env`, so `WEB_CONCURRENCY` is not kept.
`pipeline:preview-destroy` runs `scripts.preview_destroy` and then deletes
the app. The pipeline plugin does not apply the file a second time.

## Container example

```json
{
  "name": "Container",
  "stack": "container",
  "image": "registry.example.com/example:latest",
  "formation": {
    "web": {"quantity": 1, "runtime": "small"}
  }
}
```

`flynn apps:create example --manifest flynn-app.json` with that file deploys
the image when there is no git revision. A later `git push` builds the
Dockerfile in the repo and ignores `image`.
