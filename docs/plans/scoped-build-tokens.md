# Option A — Scoped build credentials (design doc, for review)

Status: **DRAFT / awaiting review — no code written yet.**

## Goal

Stop handing build jobs the cluster-wide `CONTROLLER_KEY`. Instead the trusted
gitreceive **receiver** mints a short-lived, narrowly-scoped `AccessToken` and
passes *that* to the slug/dockerbuilder job. If a buildpack / Dockerfile step
exfiltrates the token, the blast radius is one app for a few minutes, not the
whole cluster.

## What already exists (verified in tree)

- `AccessToken` proto with `issue_time`, `expire_time`, `scopes[]`,
  `app_grants[]{app_id, permissions[]}` and a `SignedData{data,signature}`
  envelope. `controller/api/controller.proto:557-585`.
- Verification only: `controller/authorizer/authorizer.go` verifies an ECDSA
  (P-256, ASN.1 sig, SHA-256) signature using the **public** key from
  `ACCESS_TOKEN_KEY`, enforces `issue/expire` and `ACCESS_TOKEN_MAX_VALIDITY`
  (default 1h). Accepts the token via `Authorization: Bearer <t>` or Basic-auth
  user `"Bearer"`.
- Policy: `controller/authz/http.go HTTPAllowed()` — `HasClusterAdmin()` short
  circuits; only paths under `/apps/:id` are app-scoped (`app:read/write/deploy`).
- tarreceive uses the same authorizer; **every** route requires auth.
- blobstore has **no** auth (network isolation only) — slug layers PUT here.

## Blockers found (must be decided)

1. **No private signing key exists anywhere.** Only the public verify key ships.
   Minting requires generating a P-256 key at bootstrap, publishing the public
   half as `ACCESS_TOKEN_KEY` (already consumed), and delivering the **private**
   half only to the receiver.
2. **`POST /artifacts` (controller) and `POST /artifact` + `POST /layer/:id`
   (tarreceive) require cluster-admin today** — they are NOT app-scoped, and
   artifacts are **global** (no `app_id` column; `ArtifactRepo.Add` takes no
   app). So a purely app-scoped token *cannot* create an artifact, which is the
   builder's whole job. This is the crux design decision (see Options 2a/2b).
3. **Clients only send a key via Basic-auth** (`pkg/httpclient`). To send a
   bearer token we either (a) add minimal bearer support to the client, or
   (b) reuse the existing `user="Bearer"` basic-auth path by putting the token
   in the password field.

## Proposed design

### Token shape (minted per build)
- `issue_time = now`, `expire_time = now + 15m` (well under the 1h max).
- `user_email = "build:<app-name>"` (audit trail; not a real user).
- One `app_grant{app_id: <this app>, permissions: ["app:write"]}`.
- Plus whatever is needed to create the artifact — see Option 2.

### Minting location
- New package `controller/tokensigner` (private-key counterpart of authorizer):
  `Sign(token *api.AccessToken) (string, error)` — marshals AccessToken,
  ECDSA-signs SHA-256(data), wraps in `SignedData`, base64url-encodes. Mirrors
  `protoVerifyUnmarshal`/`verifyASN1` exactly so round-trip is guaranteed.
- Receiver loads the private key from `ACCESS_TOKEN_SIGNING_KEY` (base64url
  PKCS8) and mints the token right before building `jobEnv`, replacing
  `CONTROLLER_KEY` with the minted token.

### Key distribution
- Bootstrap generates a P-256 keypair once (new bootstrap step or extend the
  controller-key step). Public → `ACCESS_TOKEN_KEY` on controller/tarreceive
  (as today). Private → `ACCESS_TOKEN_SIGNING_KEY` on the **gitreceive app
  only** (`bootstrap/manifest_template.json`).
- Receiver keeps its own `CONTROLLER_KEY` (it is trusted); only the *build job*
  loses it.

### The artifact-create problem (pick one)
- **Option 2a — scope-based build permission (recommended).** Add a
  `build:artifacts` scope. `HTTPAllowed` allows `POST /artifacts` (and
  tarreceive `POST /artifact`, `POST /layer/:id`) when the token carries both
  `build:artifacts` AND an `app_grant` for the target app. Narrow, explicit,
  no schema change. Token = `scopes:[build:artifacts]` +
  `app_grants:[{app,[app:write]}]`.
- **Option 2b — keep artifacts cluster-admin, mint a cluster-admin token.**
  Simpler policy (no authz change) but the token is still cluster-admin — only
  the *time limit* (15m) reduces risk, not the scope. Weaker; not recommended.

### Client threading
- Minimal-change path: builders keep reading a single secret and pass it as the
  password with user `"Bearer"`. Concretely, add a tiny helper so
  `controller.NewClient`/`tarclient.NewClient` send the value as a bearer when
  it looks like a JWT (contains a `.`), else as a key. Alternatively pass the
  token via `Authorization: Bearer` using an explicit client option. Decide
  during impl; both are small.
- The receiver writes the token to the job the same way it does the key today
  (env var + the SEC-003 `/run/secrets/controller_key` file pattern), so
  slug/dockerbuilder need ~no change beyond reading the new value.

## Files to change (Option 2a)
- `controller/tokensigner/tokensigner.go` (new) + test.
- `controller/authz/http.go` — allow `build:artifacts`+grant for the 3 routes;
  extend `httpRequirement`/`grantCovers`. Update `controller/authz/http_test.go`.
- `gitreceive/receiver/flynn-receive.go` — mint token, replace CONTROLLER_KEY in
  slug + docker jobEnv. `gitreceive/server.go` — load signing key.
- `slugbuilder/artifact/main.go`, `dockerbuilder/artifact/main.go` +
  `dockerbuilder` tar client — accept bearer token (small helper).
- `bootstrap/manifest_template.json` (+ bootstrap keygen step) — generate keypair,
  set `ACCESS_TOKEN_SIGNING_KEY` on gitreceive.
- `pkg/httpclient` (only if we choose explicit bearer option over the
  `user="Bearer"` trick).

## Tests
- `tokensigner` round-trips with `authorizer` (sign → verify → correct grants).
- authz: `build:artifacts`+grant allows the 3 build routes for the target app
  only; rejected without the scope, for other apps, or when expired.
- receiver unit: minted token has 15m expiry, one app grant, build scope; the
  build job env no longer contains `CONTROLLER_KEY`.

## Risk / rollout
- Controller **auth-model** change → its own commit(s), independent of the
  cluster rebuild. `git push`/builds already work today; this is pure hardening.
- Back-compat: if `ACCESS_TOKEN_SIGNING_KEY` is unset, receiver falls back to
  the current `CONTROLLER_KEY` behavior so existing clusters keep working until
  re-bootstrapped.

## Decisions (locked in)
1. Artifact-create: **Option 2a** — new `build:artifacts` scope; the 3 build
   routes are allowed only with that scope **plus** an app-grant for the target
   app.
2. Client threading: **add explicit bearer-token support to `pkg/httpclient`**
   (cleaner than the `user="Bearer"` trick).
3. Key generation: **new dedicated bootstrap step** (`gen-access-token-key`)
   that emits the P-256 keypair; public → `ACCESS_TOKEN_KEY`
   (controller/tarreceive), private → `ACCESS_TOKEN_SIGNING_KEY` (gitreceive).
4. Build timeout + token TTL: a single operator knob, the **app release env var
   `FLYNN_BUILD_TIMEOUT`** (a Go duration string) set with `flynn env set` (on
   the app being built, or on the gitreceive app for a cluster-wide default),
   default **15m**. It drives both (a) the receiver killing the build job if it
   runs longer than the timeout, and (b) the minted build-token TTL (token TTL =
   build timeout, so the credential only lives as long as the build that uses
   it). Builds are triggered by native `git push`, which cannot carry a per-push
   CLI flag to the server-side receiver, so the override lives in the
   app/gitreceive release env rather than a `flynn push` flag. The receiver
   clamps the value to a **cluster max of 30m** (`maxBuildTimeout`), and the
   authorizer independently enforces `ACCESS_TOKEN_MAX_VALIDITY` (default 1h) on
   top, so a client can never obtain a longer-lived token than policy allows.
5. Credential delivery: **secret-file mount** — add a secrets mechanism to
   `host.ContainerConfig` so the token is written to a root-only mounted file
   and NEVER enters the job env. Replaces the SEC-003 env+unset dance for this
   credential.

## Reframing: environment exposure is ALREADY mitigated (SEC-003)

The original worry — "user build code can read the credential from the env" —
is already handled today for `CONTROLLER_KEY`:
- `build.sh` (slug + docker) copies the key to a **root-only** file
  `/run/secrets/controller_key` (`chmod 600`) and **`unset`s it from the env**
  *before* any user code runs. slug buildpacks run as uid 5000 (can't read the
  root file); docker `RUN` steps run in BuildKit which doesn't inherit host env.
  Only `create-artifact` (root) reads it back from the file.
- The host also writes job env to `/.containerconfig` at `0600` (SEC-014).

So the remaining risk is NOT env-exposure; it is that the credential is a
**cluster-wide god-key**. A *root-level* compromise inside the build container
could still read the file. Two complementary defenses:
- **Scope reduction (the real fix):** minted token can only touch this one app
  + create this build's artifact — even a root compromise yields a one-app,
  one-build credential instead of cluster root.
- **Short TTL (defense-in-depth):** a leaked token is useless after the build.

The minted token reuses the exact same `/run/secrets` + `unset` file mechanism,
so it introduces **no new env exposure** while adding scope + TTL.

### Optional stronger variant (secret-file mount)
Today there is NO way to pass a host-job secret except via `Config.Env`
(then env-unset in build.sh). We could add a `Secrets map[string]string` (or a
content-bearing mount) to `host.ContainerConfig` so the receiver writes the
token straight to a root-only mounted file and it NEVER enters the job env,
removing the env-unset dance entirely. Larger change to the host job model;
listed as an option, not required for the core fix.

## Implementation plan (commit grouping)

All decisions are locked; no open questions remain. Build in this order, each a
self-contained commit that builds + vets for `GOOS=linux`:

1. **`controller/tokensigner`** (new pkg) + test — `Sign(*api.AccessToken)`
   mirroring `authorizer.protoVerifyUnmarshal`/`verifyASN1`; round-trip test
   signs then verifies via `authorizer`. Add `ParseSigningKey` (base64url PKCS8).
2. **authz `build:artifacts` scope** — extend `controller/authz/http.go`
   (`httpRequirement`/`grantCovers`/`HTTPAllowed`) so `POST /artifacts` is
   allowed with `build:artifacts` scope + an app-grant for the target app;
   apply the same rule in `tarreceive` for `POST /artifact` and
   `POST /layer/:id`. Update `controller/authz/http_test.go`.
3. **`pkg/httpclient` bearer support** — add a bearer/token field + option so
   `controller.NewClient`/`tarclient.NewClient` can authenticate with a JWT via
   `Authorization: Bearer`. Thread through controller/client and tarreceive/client.
4. **host secret-file mount** — add secrets to `host.ContainerConfig` +
   `host/libcontainer_backend.go` (write root-only file, bind read-only, NOT in
   env) + `pkg/exec` passthrough. Tests in `host/` (Linux).
5. **receiver minting + builder consumption** — `gitreceive/server.go` loads
   `ACCESS_TOKEN_SIGNING_KEY`; `flynn-receive.go` mints the scoped token
   (`build:artifacts` + app grant, TTL clamped) and delivers it via the secret
   mount instead of `CONTROLLER_KEY`; slug/dockerbuilder `create-artifact` read
   the token from the mounted file and use bearer auth. Back-compat: fall back to
   `CONTROLLER_KEY` when no signing key is configured.
6. **bootstrap `gen-access-token-key` step + manifest wiring** — generate the
   P-256 keypair once; publish public → `ACCESS_TOKEN_KEY`, private →
   `ACCESS_TOKEN_SIGNING_KEY` on gitreceive. Wire `ACCESS_TOKEN_MAX_VALIDITY`.
7. **operator-overridable build timeout (`FLYNN_BUILD_TIMEOUT`)** — receiver
   reads `FLYNN_BUILD_TIMEOUT` from the app release env (via `flynn env set`),
   falling back to its own process env (gitreceive app-level default) then the
   built-in 15m default, and clamps to a 30m cluster max. This single value
   both bounds build execution (the receiver kills the build job if it exceeds
   the timeout, failing the push with a clear message) and sets the minted
   token TTL (token TTL = build timeout). Set via the flynn CLI `env set` (not a
   `git push` flag, which git cannot plumb to the receiver).
