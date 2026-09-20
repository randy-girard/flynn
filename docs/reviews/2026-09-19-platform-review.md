# Flynn platform review — security, reliability, stability, performance

**Date:** 2026-09-19
**Reviewed revision:** `flynn` `main` @ `5c6be2a8` ("docs: keep dashboard mock stats aligned with Flynn host types") and each `flynn-plugin-*` repo at its `main` HEAD on the same day.
**Type:** point-in-time, read-only code review. **No code was changed.** The only files produced are this report and one link line in `docs/README.md`, on branch `docs/platform-review-2026-09`.

## 1. Scope and method

### What was read

- `flynn/` core: `controller/` (HTTP + gRPC authz, `authorizer/`, `authz/`, `tokensigner/`, releases, jobs, routes, stats, GitHub integration, scheduler), `host/` (`http.go` auth, `libcontainer_backend.go`, `userns.go`, `netpolicy.go`, `downloader/`, `cli/plugin*.go`), `router/` (`http.go`, `tcp.go`, proxy), `discoverd/server/` (handler, DNS), `blobstore/`, `gitreceive/` (server + `receiver/flynn-receive.go`), `slugbuilder/` and `dockerbuilder/` build scripts, `taffy/`, `bootstrap/` (manifest, secret generation, host-auth action, discovery client), `pkg/netpolicy/`, `pkg/iptables/`, `pkg/plugin/` (install, GitHub download, hooks, `official-plugins.json`), `pkg/cluster/`, `pkg/httphelper/`, `pkg/backup/`, `cli/config/`, `cli/cluster.go`, `script/install-flynn*`, `script/release`, `schema/controller/new_job.json`, `docs/content/security.md`, `docs/content/production.html.md`, `go.mod`.
- Plugins: `flynn-plugin.json` for all eleven plugins; `flynn-plugin-dashboard/internal/{auth,tokens,store,runjobws,webhook,config}` + `cmd/dashboard/main.go` + `web/src` sink grep; `flynn-plugin-discovery/internal/{server,store}` + `script/ready.sh`; `flynn-plugin-scheduler/{cmd/scheduler,internal/{server,runner}}`; `flynn-plugin-otel/{cmd,internal/{server,export,store}}`; `flynn-plugin-redis` (`start.sh`, `process.go`, `handler.go`, `cmd/flynn-redis`); `flynn-plugin-{mariadb,mongodb,kafka,clickhouse}` `handler.go`, `cmd/flynn-*/main.go`, `img/packages.sh`; `flynn-plugin-mongodb/flynn-plugin.json` backup args.
- Second pass (same day) additionally read: `flynn/pkg/sirenia/state/state.go`, `flynn/appliance/postgresql/{process,handler}.go`, `flynn/discoverd/server/store.go`, `flynn/logaggregator/{api,server,aggregator}.go` + `buffer/`, `flynn/flannel/{main.go,discoverd/registry.go,subnet/subnet.go,backend/vxlan}`, `flynn/host/cli/{update.go,github_updater.go}`, `flynn/host/downloader/downloader.go`, `flynn/host/http.go` (`PullBinariesAndConfig`), `flynn/updater/updater.go`, `flynn/status/status.go`, `flynn/host/volume/{api/http.go,zfs/zfs.go,zfs/zpool.go,manager}`, `flynn/router/acme/{acme,account,responder}.go`, `flynn/controller/{acme_config,managed_certificate,grpc}.go`, `flynn/controller/data/managed_certificate.go`, `flynn/controller/authz/grpc.go`, `flynn/controller/scheduler/scheduler.go` (volume adoption, job sync), `flynn/host/resource/resource.go`, `flynn/pkg/iptables/iptables.go` (full), `flynn/builder/img/{ubuntu-noble,go}.sh`, `flynn/dockerbuilder/img/packages.sh`, `flynn/apt_cache/`.

### Tools run

- `go version` → go1.24 (host: macOS). `govulncheck ./...` with `GOFLAGS=-mod=vendor` in `flynn/`: first run failed to type-check Linux-only code on darwin; re-run with `GOOS=linux GOARCH=amd64` succeeded (exit 3 = vulns found). `govulncheck` also run per plugin repo with `GOOS=linux GOARCH=amd64`. Output summarised in Appendix A.
- `git log`, `git diff --stat`, `git blame` against the upstream tip (`c283e9f5`, last flynn/flynn commit present in the fork history) to classify findings as **fork-introduced** vs **inherited**.
- `rg`/`grep` for auth/permission/timeout/TLS keywords.

### Conventions

- Finding IDs `SEC-`, `REL-`, `STAB-`, `PERF-`, `INFO-` are **this report's** numbering. The code already contains comments tagged `SEC-001`…`SEC-017` from an earlier remediation pass (e.g. `host/http.go`, `host/libcontainer_backend.go`); those are unrelated to the IDs below and are referred to as "code tag SEC-0xx" where relevant.
- Paths are relative to the workspace root (`flynn/...`, `flynn-plugin-redis/...`). Line numbers are as of the reviewed revision.
- Anything not verified end-to-end is marked **needs confirmation**.
- Severity reflects exploitability from the weakest credential that reaches the code path (an app-scoped dashboard user, a `git push`, or code running inside a user/build container), not from a cluster admin, who is root-equivalent by design.

## 2. Summary

### By severity

| Severity | Count |
|---|---|
| Critical | 3 |
| High | 11 |
| Medium | 16 |
| Low | 19 |
| Info | 3 |
| **Total** | **52** |

### By area

| Area | Critical | High | Medium | Low | Info | Total |
|---|---|---|---|---|---|---|
| Security | 3 | 10 | 11 | 11 | 1 | 36 |
| Reliability | 0 | 1 | 2 | 7 | 0 | 10 |
| Stability | 0 | 0 | 2 | 1 | 0 | 3 |
| Performance | 0 | 0 | 1 | 0 | 0 | 1 |
| Hygiene (Info) | 0 | 0 | 0 | 0 | 2 | 2 |

Critical: SEC-001, SEC-002, SEC-004. High: SEC-003, SEC-005–SEC-011, SEC-028, SEC-029, REL-007. Medium: SEC-012, SEC-013, SEC-015–SEC-020, SEC-030–SEC-032, REL-001, REL-008, STAB-001, STAB-003, PERF-001. Low: SEC-014, SEC-021–SEC-027, SEC-033–SEC-035, REL-002, REL-003, REL-005, REL-006, REL-009–REL-011, STAB-002. Info: INFO-001–INFO-003.

**Revision note (second pass, same day).** Sections 4.12–4.19 were added to cover the components originally listed as not reviewed. Existing IDs are stable; the following existing findings were revised after reading more code: SEC-003 (Critical → High: user/build containers *cannot* reach discoverd `:1111` — the INPUT/FORWARD rules delete the legacy ACCEPTs — but the key is trivially obtainable from the node network, see SEC-028), SEC-014 (Medium → Low: `GetJob` is app-scoped via `GetInApp`; only `PutJob` is unscoped), SEC-017 (two "needs confirmation" items resolved: gRPC authz bypass not applicable, x/crypto/ssh not production-reachable), STAB-002 (confirmed), SEC-002 (`ALLOW_DEV_TOKEN` confirmed `false` in the plugin manifest).

The overarching theme: the fork added a real privilege boundary (app-scoped tokens, fine-grained grants, user namespaces, netpolicy) on top of an upstream architecture that assumed every authenticated caller and every internal service was fully trusted. Most Critical/High items are places where that old assumption still leaks through the new boundary.

## 3. Top 10 to work on next

1. **SEC-001** — `RunJob` passes caller-supplied `meta`/`partition`/`profiles` to the host; a `jobs:run` grant on any app yields a system-class job (host auth key in env, `zfs`/`kvm` profiles, no userns, system netpolicy) → host root.
2. **SEC-002** — Dashboard mints a token with `nil` scopes + `nil` grants for a non-admin user with zero collaborator rows; the controller treats that as cluster admin.
3. **SEC-004** — Blobstore has no authentication and is reachable from build jobs; the "signed build cache URL" is never verified server-side. Cross-app slug/cache poisoning and cluster-wide image deletion.
4. **SEC-028 / SEC-003** — The controller publishes `CONTROLLER_KEY` as discoverd instance metadata (`AUTH_KEY`), and discoverd `:1111` is unauthenticated (`DISCOVERD_AUTH_KEY` is never set). Anyone on the node network gets the cluster admin key with one `GET`; `POST /shutdown` and raft peer edits are also open.
5. **SEC-029** — Datastore appliance admin HTTP APIs (`postgres :5433`, `mariadb :3307`, `mongodb :27018`, `redis :6380`, Kafka topic API) are unauthenticated and netpolicy allows user containers to every datastore port: `GET /backup` dumps all MariaDB databases, `POST /stop` halts any datastore, Kafka topics can be deleted.
6. **SEC-005** — gitreceive accepts *any* validly-signed token to push to *any* app by name (no grant check).
7. **SEC-007** — App-scoped `routes:write` can point a route at any discoverd service (`controller`, `blobstore`, `postgres-api`, …), exposing internal services publicly.
8. **SEC-008** — Discovery plugin publishes the cluster join token at unauthenticated `GET /.well-known/cluster` on a public route and accepts unauthenticated instance registration; bootstrap then sends the host auth key (HTTP basic) to attacker-registered URLs.
9. **REL-007** — Let's Encrypt certificates are never renewed and failed issuances are never retried (`ListExpiring` has no callers; only `pending` certificates are processed), contrary to the docs. Every ACME-backed route goes dark within 90 days.
10. **SEC-006 / SEC-011 / SEC-009** — Build jobs run root-in-container with `CAP_SYS_ADMIN`, seccomp unconfined, no AppArmor, no userns, with `CONTROLLER_KEY` in the environment, on top of vendored `runc/libcontainer v1.0.0-rc8` (2019). Any `git push` is one known libcontainer weakness away from host root.

Also close behind: **SEC-012** (unsigned plugin hooks run as root with cluster secrets), **SEC-017** (x/net, grpc, yaml.v2 reachable vulns), **SEC-031/SEC-032** (unverified toolchain/tarball downloads and plaintext, unchecksummed binary distribution during rolling updates).

## 4. Findings

### 4.1 Controller — authorization boundary

#### SEC-001 — `RunJob` honours caller-controlled `meta`, `partition` and `profiles`; escalates to host root

- **Severity:** Critical · **Area:** Security · **Component:** `flynn/controller`, `flynn/host` · **Origin:** fork-introduced (upstream had no per-app boundary; the fork added one but did not sanitise this input) · **Effort:** S
- **Refs:** `flynn/controller/jobs.go:137-266` (also the attach path `:362-436`), `flynn/schema/controller/new_job.json`, `flynn/host/libcontainer_backend.go:2183-2197` (`isSystemJob`), `:2204-2214` (`lockUntrustedJob`), `:507-508` (profiles), `:982` (`FLYNN_HOST_AUTH_KEY`), `flynn/host/userns.go:39-44`, `flynn/pkg/netpolicy/policy.go:115-125`, `flynn-plugin-dashboard/internal/runjobws/handler.go:62`.
- **Description.** `RunJob` decodes `ct.NewJob` from the request body and builds the `host.Job` by merging `newJob.Meta` over `app.Meta`, then setting `Partition: string(newJob.Partition)` and `Profiles: newJob.Profiles` verbatim:

  ```go
  // flynn/controller/jobs.go:234-263
  for k, v := range newJob.Meta { metadata[k] = v }
  ...
  job := &host.Job{ ..., Partition: string(newJob.Partition), Profiles: newJob.Profiles }
  ```

  On the host, trust is derived from exactly those fields:

  ```go
  // flynn/host/libcontainer_backend.go:2190-2197
  if job.Partition == "system" { return true }
  ...
  return job.Metadata["flynn-system-app"] == "true" || job.Metadata["flynn-controller.app_name"] == "builder"
  ```

  A system job skips `lockUntrustedJob`, keeps `Profiles` (`jobProfiles` includes device/host-mount profiles such as zfs/kvm), runs **without** a user namespace (`useUserNS` → `ClassifyJob != ClassUser`), receives `FLYNN_HOST_AUTH_KEY` in its environment (`:982`), and is placed in the `system` netpolicy class (no overlay/node/host drops). The JSON schema allows `meta`, `partition` (`enum` includes `system`) and `profiles`. The route is guarded only by `app:jobs:run` for the target app. The dashboard's run-job websocket forwards the raw client JSON to this endpoint with the cluster key (`runjobws/handler.go:62`), so any dashboard user with "run" on one app reaches it.
- **Impact.** A collaborator with `jobs:run` on a single app obtains a container with the host API key (`:1113` full control incl. arbitrary privileged jobs), raw device access via profiles, and no userns → host root on every node.
- **Fix.** In `RunJob` (both variants) for non-admin callers: drop reserved keys from `newJob.Meta` (`flynn-system-app`, `flynn-controller.*`, `flynn-datastore`, `flynn-plugin`), force `Partition = "user"` (or `""`) unless `app.Meta["flynn-system-app"]=="true"`, and reject non-empty `Profiles`. Additionally, make the host derive trust from a signed/attested field the controller sets rather than free-form metadata (defence in depth).

#### SEC-013 — `scale:write`/`env:write` can create and activate arbitrary releases; `SetAppRelease`/`RunJob` accept releases of other apps

- **Severity:** Medium · **Area:** Security · **Component:** `flynn/controller` · **Origin:** fork-introduced · **Effort:** S
- **Refs:** `flynn/controller/authz/grants.go:211-214`, `flynn/controller/release.go:19-29` (`CreateRelease`), `:84-110` (`SetAppRelease`), `flynn/controller/data/app.go:298-309`, `flynn/controller/jobs.go:149-153`.
- **Evidence.**

  ```go
  // grants.go:211
  func CanCreateRelease(perms []string) bool {
      return HasAppPermission(perms, PermAppEnvWrite) || HasAppPermission(perms, PermAppScaleWrite)
  }
  ```

  `SetAppRelease` loads `rid.ID`, validates the schema and calls `appRepo.SetRelease(app, release.ID)`; neither it nor `TxSetRelease` compares `release.AppID` to `app.ID`. `RunJob` likewise loads `newJob.ReleaseID` without checking it belongs to the app in the URL.
- **Impact.** (a) A custom role holding only `app:scale:write` can `POST /releases` with a new artifact + env and then `PUT /apps/:id/release`, i.e. deploy arbitrary code and rewrite config despite lacking `app:deploy`/`app:env:write`. Default roles ("Deploy", "Manage") are not affected. (b) Given another app's release UUID (not enumerable, but leaks via events/logs), an app-scoped token can activate that release on its own app or run a job with `release_env: true` and read the other app's env/secrets.
- **Fix.** Require `PermAppDeploy` (or a dedicated `release:write`) for `CreateRelease`/`SetAppRelease`; in `SetAppRelease`, `RunJob`, `RunJobAttach` reject when `release.AppID != "" && release.AppID != app.ID`.

#### SEC-014 — `PutJob` does not bind the job to the app in the URL

- **Severity:** Low (revised from Medium) · **Area:** Security · **Component:** `flynn/controller` · **Origin:** fork-introduced (boundary) · **Effort:** S
- **Refs:** `flynn/controller/jobs.go:74-91` (`PutJob`), `:49-57` (`GetJob` — correctly uses `jobRepo.GetInApp(appID, jobID)`), `flynn/controller/scheduler/scheduler.go:662-700`.
- **Description.** `PutJob` upserts the decoded `ct.Job` (including its own `AppID`, `HostID`, `State`) via `jobRepo.Add` without checking `job.AppID == app.ID`. `GetJob` and `KillJob` are scoped, so this is write-only. **Resolved:** the scheduler's sync (`JobListActive`) marks unknown UUIDs `down` and re-persists known ones from its in-memory state, so foreign rows cannot start or stop jobs; the impact is corrupted job history/state rows for another app (given its job UUID).
- **Fix.** Reject when `job.AppID != c.getApp(ctx).ID`.

#### SEC-021 — `GET /github/installations[/…/repos]` readable by any authenticated token

- **Severity:** Low · **Area:** Security · **Component:** `flynn/controller` · **Origin:** fork-introduced · **Effort:** S
- **Refs:** `flynn/controller/authz/http.go:199-205`, `flynn/controller/controller.go:331-332`.
- **Description.** The `github` route kind returns `rkAnyAuth` for all GET/HEAD requests, so an app-scoped token can list every GitHub App installation and every repo the installation can see.
- **Fix.** Require cluster admin, or `app:github:write` on at least one app, and filter to installations already linked to the caller's apps.

#### INFO-003 — `HasClusterAdmin()` treats "no scopes and no grants" as admin

- **Severity:** Info (root cause for SEC-002) · **Component:** `flynn/controller/authorizer` · **Origin:** fork-introduced.
- **Refs:** `flynn/controller/authorizer/authorizer.go:48-58`: `return len(t.Scopes) == 0 && len(t.AppGrants) == 0`.
- **Fix.** Make admin explicit: only `ClusterKey`, `cluster:admin` or `*` grant admin; an empty token should be **no** access. This also removes an entire class of minting bugs.

### 4.2 Dashboard plugin — auth and token minting

#### SEC-002 — Non-admin dashboard user with zero collaborator rows receives a cluster-admin token

- **Severity:** Critical · **Area:** Security · **Component:** `flynn-plugin-dashboard`, `flynn/controller/authorizer` · **Origin:** fork-introduced · **Effort:** S
- **Refs:** `flynn-plugin-dashboard/internal/tokens/mint_user.go:14-42`, `flynn-plugin-dashboard/internal/tokens/tokens.go:34-48`, `flynn/controller/authorizer/authorizer.go:48-58`.
- **Evidence.**

  ```go
  // mint_user.go:29-41  (u != nil, !u.EnabledClusterAdmin())
  rows, err := s.ListCollaboratorsForUser(userID)
  ...
  if u == nil && len(rows) == 0 { /* dev fallback */ }
  var grants []AppGrantClaim
  for _, r := range rows { grants = append(grants, ...) }
  return m.MintControllerAccessToken(clientID, userID, email, nil, grants)   // grants == nil when rows is empty
  ```

  With `rows` empty this mints scopes=`nil`, appGrants=`nil`; the controller's `HasClusterAdmin()` returns `true` for that token.
- **Reproduction path.** Admin invites a user (collaborator row created), later removes them from the app (row deleted) or creates a user via the users API with `cluster_admin=false` and no apps yet. The user logs in and calls `/api/token` (or the OAuth code exchange) → token → any controller call succeeds as admin, including `RunJob` with `partition: system` (SEC-001). The `u == nil` dev fallback is gated by `ALLOW_DEV_TOKEN`; **confirmed** `flynn-plugin-dashboard/flynn-plugin.json:46` sets it to `"false"` (the Go default in `internal/config/config.go:93` is `true`, see SEC-035).
- **Fix.** In `MintTokenForUser`, return an error (or a token with an explicit `none` scope) when a real, non-admin user has no grants. In the controller, remove the empty-means-admin rule (INFO-003).

#### SEC-019 — New users are created with `cluster_admin = TRUE` and demoted non-atomically

- **Severity:** Medium · **Area:** Security · **Component:** `flynn-plugin-dashboard` · **Origin:** fork-introduced · **Effort:** S
- **Refs:** `flynn-plugin-dashboard/internal/store/store.go:711-722`.
- **Description.** `CreateUser` hard-codes `cluster_admin` to `TRUE`; callers that want non-admins (invite acceptance, admin user creation with `cluster_admin:false`) issue a second UPDATE. A crash, error or concurrent token mint between the two statements leaves/uses an admin. The users API also defaults an omitted `cluster_admin` field to true.
- **Fix.** Take `clusterAdmin bool` as a parameter to the INSERT; default the API field to false.

#### Positive notes on dashboard auth

Login sets `HttpOnly`, `Secure` (when configured) and `SameSite=Lax` cookies and hashes passwords with bcrypt (`internal/auth/auth.go:132-178`). The controller is called with the user's minted token for normal API traffic; only the run-job websocket uses the cluster key (SEC-001).

### 4.3 gitreceive, build pipeline, blobstore

#### SEC-005 — gitreceive accepts any signed token for any app

- **Severity:** High · **Area:** Security · **Component:** `flynn/gitreceive` · **Origin:** fork-introduced (upstream only had cluster-wide credentials; the fork added scoped tokens without a scope check here) · **Effort:** S
- **Refs:** `flynn/gitreceive/server.go:104-127`.
- **Evidence.**

  ```go
  if _, err := h.auth.AuthorizeRequest(r); err != nil { ...401... }
  ...
  name := strings.TrimSuffix(...r.URL.Path...)
  app, err := h.controller.GetApp(name)     // h.controller is the cluster-key client
  ```

  The token's grants are discarded (`_`), and the app lookup uses the privileged client.
- **Impact.** Any dashboard collaborator (even read-only on one app) or holder of a build token can `git push` to any app by name, deploying arbitrary code.
- **Fix.** Use the token returned by `AuthorizeRequest`: require cluster admin or `app:deploy` (or `app:write`) on `app.ID`; alternatively perform `GetApp` with a client authenticated as the caller.

#### SEC-006 — Build jobs run as root with `CAP_SYS_ADMIN`, seccomp unconfined, no AppArmor, no user namespace; custom buildpacks are cloned as root

- **Severity:** High · **Area:** Security · **Component:** `flynn/host`, `flynn/slugbuilder`, `flynn/dockerbuilder` · **Origin:** inherited design (upstream ran builders privileged), fork-modified (seccomp/AppArmor/userns exemptions are fork code) · **Effort:** L
- **Refs:** `flynn/host/libcontainer_backend.go:723-732` (seccomp skipped for build jobs), `:781` (AppArmor skipped), `:2204-2214` (`lockUntrustedJob` keeps writable cgroups for build), `:2262-2266` (`CAP_MKNOD, CAP_SYS_CHROOT, CAP_SYS_ADMIN, CAP_NET_ADMIN, CAP_NET_RAW`), `flynn/host/userns.go:39-44`, `flynn/slugbuilder/builder/build.sh:215-226`, `flynn/dockerbuilder/builder/build.sh:27-59`.
- **Description.** `isBuildJob` → `ClassBuild` → `useUserNS` false, so container root is host root (uid 0). The container keeps `CAP_SYS_ADMIN`, runs with no seccomp filter and no AppArmor profile, and has writable cgroups. `build.sh` drops buildpack execution to the `flynn` user via `setuidgid`, but explicitly clones the user-supplied `BUILDPACK_URL` **as root** (`build.sh:218-224`); `no_new_privs` does not protect against a hostile git repository exploiting git itself, and an unprivileged user in an unconfined container can `unshare -Ur` (no seccomp). Dockerfile `RUN` steps execute in BuildKit's nested runc inside this outer container.
- **Impact.** Any `git push` places attacker code one container-escape primitive away from host root, with `CAP_SYS_ADMIN` and no syscall filtering available to it. Combined with SEC-009 (2019-era libcontainer with known escapes) this is realistic.
- **Fix.** Run the build container in a user namespace (BuildKit rootless supports this), give it a seccomp allow-list tailored to BuildKit rather than "unconfined", keep AppArmor, clone buildpacks as the unprivileged user (fix the exit-111 issue instead of working around it), and treat the build tier as untrusted at the network layer too (SEC-004).

#### SEC-011 — `CONTROLLER_KEY` is still injected into build job environments

- **Severity:** High · **Area:** Security · **Component:** `flynn/gitreceive/receiver` · **Origin:** inherited (upstream passed the key), partially mitigated by fork (`unset` before buildpack steps, scoped build token) · **Effort:** M
- **Refs:** `flynn/gitreceive/receiver/flynn-receive.go:325-331`, `:136-150` (comment explaining the fallback), `flynn/slugbuilder/builder/build.sh:113`.
- **Evidence.**

  ```go
  jobEnv := map[string]string{
      "BUILD_CACHE_URL": signedBuildCacheURL(app.ID, os.Getenv("CONTROLLER_KEY")),
      "CONTROLLER_KEY":  os.Getenv("CONTROLLER_KEY"),
  ```

  `build.sh` unsets the variable before running buildpacks, but the value remains in the container's init config on disk (`/.containerconfig`, root-only) and in `/proc/1/environ`. Because build containers run as host-root without userns (SEC-006), the `flynn` user cannot read those directly, but any of the root-level footholds in SEC-006 (root git clone, nested runc) can.
- **Fix.** Stop passing `CONTROLLER_KEY` once all hosts mount `ContainerSecrets`; use only the app-scoped build token, and give the blobstore something to verify (SEC-004).

#### SEC-004 — Blobstore is unauthenticated; build jobs can read, overwrite and delete any blob; build-cache token is never checked

- **Severity:** Critical · **Area:** Security · **Component:** `flynn/blobstore`, `flynn/pkg/netpolicy` · **Origin:** inherited (upstream blobstore had no auth) — the unverified "signed" cache URL (code tag SEC-013) is fork-introduced · **Effort:** M
- **Refs:** `flynn/blobstore/blobstore.go:35-110`, `:169`, `flynn/pkg/iptables/iptables.go:225-226` (build→user DROP only), `:150-159` (build→node DROP), `flynn/gitreceive/receiver/flynn-receive.go:329`, `flynn/dockerbuilder/builder/build.sh:68,104`.
- **Description.** `handler()` serves `GET`/`HEAD`/`PUT`/`DELETE` and directory listing (`GET /?dir=`) with no credential check of any kind; `grep -rn token blobstore/` finds nothing, so the `token=` query string appended by `signedBuildCacheURL` is ignored. Netpolicy places builders in `ClassBuild`, whose only overlay restriction is "may not talk to user-class IPs and node IPs" — blobstore, postgres, controller and the datastore APIs on the overlay remain reachable. `docs/content/security.md` claims user jobs cannot reach internal services; that is true for `ClassUser` but not for build jobs.
- **Impact.** From a `git push`: list and download every slug and image layer of every app (secrets baked into slugs), replace another app's build cache tarball (code injection into that app's next build), `DELETE` layers/slugs cluster-wide (jobs fail to start on next placement — DoS). Layer hash verification at mount time (`host/downloader/downloader.go`) turns layer tampering into DoS rather than code execution, but slugs/caches are not hash-pinned.
- **Fix.** Add bearer/HMAC auth to blobstore (verify the existing signed URL for cache paths; require the cluster key or a scoped token for everything else), and extend netpolicy so `ClassBuild` may only reach blobstore/tarreceive endpoints it needs.

#### SEC-017 (part) — `yaml.v2 v2.2.2` parses user-supplied `Procfile`

See §4.9; the reachable trace is `flynn/slugbuilder/artifact/main.go:242` (`loadProcfileTypes` → `yaml.Unmarshal`), i.e. attacker-controlled input.

### 4.4 flynn-host

#### SEC-010 — Host API `:1113` is fully open while `authKey` is empty; first `ConfigureAuthKey` is trust-on-first-use

- **Severity:** High · **Area:** Security · **Component:** `flynn/host` · **Origin:** inherited (upstream had no host auth at all); fork added the key but left a default-open window · **Effort:** S
- **Refs:** `flynn/host/http.go:87-92`, `:811-821`, `flynn/bootstrap/configure_host_auth_action.go:27-30`.
- **Evidence.**

  ```go
  // host/http.go:89
  if h.authKey == "" { next.ServeHTTP(w, r); return }
  // host/http.go:814
  if h.host.authKey != "" && !h.host.authKeyValid(...) { 401 }
  ```

- **Impact.** Any host started without `/etc/flynn/host.json` containing a key (fresh install before bootstrap, a node joined by hand, a host whose config was lost) exposes create/stop-job, volume and update APIs to anyone who can reach `:1113`, and whoever first calls `POST /host/auth-key` owns the node. On a cloud VPC that is often "anything on the subnet". `docs/content/security.md` does say ports must be firewalled.
- **Fix.** Fail closed: refuse non-status requests when no key is configured except from loopback/unix socket; require `flynn-host init` to generate a key locally before listening. Persisting `host.json` at `0600` (`host/config/auth.go`) is already correct.

#### SEC-016 — Seccomp is a deny-list with `DefaultAction: Allow`

- **Severity:** Medium · **Area:** Security · **Component:** `flynn/host` · **Origin:** fork-introduced (code tag SEC-006) · **Effort:** M
- **Refs:** `flynn/host/libcontainer_backend.go:723-775`.
- **Description.** The profile blocks ~35 syscalls and allows everything else. Docker's default profile (which the comment cites) is an allow-list of ~350 syscalls with everything else denied. Not covered here, for example: `open_by_handle_at`/`name_to_handle_at` (classic escape with `CAP_DAC_READ_SEARCH`), `process_vm_readv/writev`, `ptrace` (allowed; relevant with `CAP_SYS_PTRACE`), `mbind`/`set_mempolicy`, `io_uring_*`, `fsopen`/`fsmount`/`fspick` (new mount API; `move_mount`/`open_tree` are blocked but `fsmount` is not), `personality`, `sysfs`, `uselib`, `vm86`. User jobs also have userns + capability drop + AppArmor, so this is defence-in-depth rather than a direct hole.
- **Fix.** Switch to an allow-list (import Docker's/containerd's default profile JSON and translate to `configs.Syscall`), keep the deny-list only as a fallback when libseccomp lacks a name.

#### STAB-001 — Memory swap limit set to 2× memory; scheduler placement is count-based

- **Severity:** Medium · **Area:** Stability · **Component:** `flynn/host`, `flynn/controller/scheduler` · **Origin:** inherited (limit semantics), unknown for the 2× comment · **Effort:** M
- **Refs:** `flynn/host/libcontainer_backend.go:647`, `:1049-1064`, `flynn/controller/scheduler/scheduler.go` (placement loop, host choice by job count).
- **Description.** `MemorySwap` is set equal to the memory limit ("total = 2x limit"), so each job may consume its limit again in swap; the scheduler chooses hosts by number of jobs per type, not by reserved memory. A host can therefore be committed far beyond RAM+swap; OOM then kills arbitrary system jobs (postgres, discoverd) rather than the offender.
- **Fix.** Set `MemorySwap == Memory` (no additional swap) for user jobs, and add memory-aware placement using `host.Resources` and the per-host stats already collected.

#### STAB-002 — No `pids` cgroup limit by default (needs confirmation)

- **Severity:** Low · **Area:** Stability · **Component:** `flynn/host` · **Origin:** inherited · **Effort:** S
- **Refs:** `flynn/host/libcontainer_backend.go:808-812` (RLIMIT_NPROC only when `max_procs` resource is set), `:2447-2448` (pids controller enabled).
- **Description.** The `pids` controller is enabled but no `PidsLimit` is applied unless the app sets `max_procs`; `RLIMIT_NPROC` is per real uid and shared across jobs mapped to the same uid range. A fork bomb in one user job can exhaust the host PID space. **Confirmed:** `flynn/host/resource/resource.go:52-57` defaults only `memory`, `cpu`, `temp_disk`, `max_fd`; `max_procs` has no default, and `libcontainer_backend.go:808` only sets `RLIMIT_NPROC` when the spec is present.
- **Fix.** Apply `Cgroups.Resources.PidsLimit` from a default (e.g. 4096) for every job.

#### REL-005 — AppArmor apply failure is fail-open for system and build jobs

- **Severity:** Low · **Area:** Reliability/Security · **Component:** `flynn/host` · **Origin:** fork-introduced · **Effort:** S
- **Refs:** `flynn/host/libcontainer_backend.go:1147-1166`.
- **Description.** On an "apparmor" error the job is retried without a profile. User jobs correctly fail closed (`:1155-1158`); system/build jobs silently lose the LSM. Build jobs already run without AppArmor (SEC-006), so exposure is limited to system jobs. Log at error level exists; there is no metric/event.
- **Fix.** Emit a host event/metric when falling back; consider failing closed for datastore-class jobs.

### 4.5 discoverd, network policy, router

#### SEC-003 — discoverd HTTP API is unauthenticated on the node network

- **Severity:** High (revised from Critical) · **Area:** Security · **Component:** `flynn/discoverd`, `flynn/pkg/iptables`, `flynn/host/netpolicy.go` · **Origin:** inherited (upstream discoverd never had auth); the dead `DISCOVERD_AUTH_KEY` (code tag SEC-009) is fork-introduced · **Effort:** M
- **Refs:** `flynn/discoverd/server/handler.go:50` (`h.AuthKey = os.Getenv("DISCOVERD_AUTH_KEY")`), `:131-140` (check only if non-empty), `:58-81` (routes incl. `PUT /services/:service/instances/:id`, `PUT /services/:service/leader`, `PUT|DELETE /raft/peers/:peer`, `POST /shutdown`), `:276-301` (`servePutInstance`: no origin/address validation), `flynn/pkg/iptables/iptables.go:271-295` (`EnableHostIsolation` *deletes* the legacy `:1111` ACCEPT rules and installs INPUT DROP for user/build sets), `:150-159` (user/build → node IPs DROP), `flynn/discoverd/server/dns.go:222` (DNS filtered for user class).
- **Description.** `rg DISCOVERD_AUTH_KEY` finds a single reader and no writer anywhere in `flynn/` (bootstrap manifest, plugins, CLI), so the key is always empty and every discoverd endpoint is open to whoever can reach `:1111` on a node. **Resolved (corrected from the first pass):** user- and build-class containers *cannot* reach it — `EnableHostIsolation` removes the old ACCEPT rules and adds `INPUT … DROP` for both ipsets, and `UserToNodeDropArgs`/`BuildToNodeDropArgs` block other nodes' IPs. The exposure is therefore the node/underlay network (same audience as SEC-010) plus any container that becomes system-class (SEC-001) or datastore-class.
- **Impact.** From the node network: `GET /services/controller/instances` returns the cluster admin key directly (SEC-028) — no MITM required; `POST /shutdown` on each host's discoverd → cluster-wide outage; `PUT /services/<svc>/instances/<id>` / `PUT …/leader` → redirect `controller`, `blobstore`, `postgres` leader, `flannel` subnet meta (SEC-033) to attacker IPs; `PUT|DELETE /raft/peers/:peer` → raft membership tampering.
- **Fix.** Generate `DISCOVERD_AUTH_KEY` in bootstrap and pass it to discoverd and every system client (`pkg/discoverd` client reads it from env); bind the HTTP API to the bridge/loopback plus authenticated peers only; validate that a registered instance's address equals the request's source IP for non-system callers.

#### SEC-007 — App-scoped route writes can target any service

- **Severity:** High · **Area:** Security · **Component:** `flynn/controller`, `flynn/router` · **Origin:** fork-introduced (boundary) · **Effort:** S
- **Refs:** `flynn/controller/routes.go:17-23`, `flynn/router/http.go:214-262`, `flynn/router/tcp.go:154`.
- **Description.** `CreateRoute` sets `route.ParentRef` to the caller's app but never checks that `route.Service` is one of that app's services (`<app>-<proctype>` or a service declared in its release). The router binds whatever service name is given. Domain conflicts are checked, service ownership is not.
- **Impact.** A collaborator with `app:routes:write` creates `evil.example.com → controller`, `→ blobstore` (SEC-004: every slug and backup downloadable from the internet), `→ postgres-api`, `→ dashboard-web`, or a TCP route to `postgres` leader. With ACME enabled the router even obtains a valid certificate for it.
- **Fix.** For non-admin callers, require `route.Service` to match `^<appName>-` or a service name present in the app's current release; deny routes to services whose owning app has `flynn-system-app`.

#### SEC-025 — `X-Forwarded-Proto` / `X-Forwarded-For` are appended, not replaced

- **Severity:** Low · **Area:** Security · **Component:** `flynn/router` · **Origin:** inherited · **Effort:** S
- **Refs:** `flynn/router/http.go:345-352` (`fwdProtoHandler`), `flynn/router/proxy/reverseproxy.go` (XFF handling).
- **Description.** A client speaking plain HTTP to the router can pre-set `X-Forwarded-Proto: https` and it is passed through; backends that trust it (e.g. to skip an HTTPS redirect or set `Secure` cookies) are misled. Same for `X-Forwarded-For` spoofing when `proxyProtocol` is off.
- **Fix.** Overwrite `X-Forwarded-Proto` unconditionally; either overwrite `X-Forwarded-For` or document that only the last hop is trustworthy.

#### REL-006 — TLS listener lacks `ReadHeaderTimeout`

- **Severity:** Low · **Area:** Reliability · **Component:** `flynn/router` · **Origin:** fork partially fixed (plain HTTP server got `IdleTimeout`/`ReadHeaderTimeout` at `:352-353`) · **Effort:** S
- **Refs:** `flynn/router/http.go:415-421` vs `:345-353`.
- **Description.** The HTTPS `http.Server` sets only `TLSNextProto`; slow-header (slowloris) connections on 443 hold goroutines/FDs indefinitely (h2 gets `IdleTimeout` via `http2.Server`, h1 does not).
- **Fix.** Add the same `IdleTimeout`/`ReadHeaderTimeout` to the TLS server.

### 4.6 Plugin system (`pkg/plugin`, `flynn-host plugin:*`)

#### SEC-012 — Install/uninstall/ready hooks: unsigned scripts from GitHub releases executed as root with cluster secrets

- **Severity:** Medium · **Area:** Security · **Component:** `flynn/pkg/plugin`, `flynn/host/cli` · **Origin:** fork-introduced · **Effort:** M
- **Refs:** `flynn/pkg/plugin/github.go:236` (`fetchGitHubHooks`), `flynn/pkg/plugin/install.go:520-535` (`exec.Command(script)` with `CONTROLLER_KEY=…`), `flynn/host/cli/plugin.go:21` (`--github-org`), `flynn/pkg/plugin/official-plugins.json`.
- **Description.** `plugin:install <alias>` resolves `github_repo` from `official-plugins.json` (or `--github-org`/`FLYNN_PLUGIN_GITHUB_ORG`/`/etc/flynn/plugins.json`), downloads `image.json`, layers and `script/*.sh` from the release over HTTPS and runs the hooks as the invoking user (root) with `CONTROLLER_KEY`, and for the dashboard also `ACCESS_TOKEN_SIGNING_KEY`, in the environment. Nothing is signed; integrity rests on GitHub TLS + release immutability. Layer squashfs files **are** hash-verified against `image.json` at mount time (`host/downloader/downloader.go`), but `image.json` and the scripts themselves are not.
- **Impact.** Compromise of the GitHub org/repo/release (or a typo'd `--github-org`) is root on every host plus the cluster key and the token-signing private key.
- **Fix.** Publish a detached signature (minisign/cosign) for `image.json` + hooks and verify it against a key baked into `flynn-host`; pin `official-plugins.json` entries to release digests; drop `ACCESS_TOKEN_SIGNING_KEY` from hook env (deliver via the app env only).

#### SEC-018 — GitHub token sent to arbitrary `LayerURL` from `image.json`

- **Severity:** Medium · **Area:** Security · **Component:** `flynn/pkg/plugin` · **Origin:** fork-introduced · **Effort:** S
- **Refs:** `flynn/pkg/plugin/github.go:160-171`, `:430-439` (`Authorization: Bearer <token>` added whenever `token != ""`).
- **Description.** `downloadURL(token, art.LayerURL(layer), …)` uses the URL embedded in the plugin's `image.json`. A malicious or tampered manifest can set it to `https://attacker.example/…` and receive the operator's GitHub token (used for private repos / rate limits).
- **Fix.** Only attach the token when the URL host is `api.github.com`/`github.com`/`objects.githubusercontent.com`; reject other hosts unless `--allow-external-layers`.

### 4.7 Discovery plugin and host join

#### SEC-008 — Discovery plugin publishes the join token publicly and accepts unauthenticated instance registration

- **Severity:** High · **Area:** Security · **Component:** `flynn-plugin-discovery`, `flynn/host`, `flynn/bootstrap` · **Origin:** fork-introduced · **Effort:** M
- **Refs:** `flynn-plugin-discovery/internal/server/server.go:20-27` (routes), `:61-73` (`GET /.well-known/cluster` returns the default cluster id + URL), `:102-122` (`POST /clusters/{id}/instances` no auth), `:124-134` (`GET …/instances` no auth), `flynn-plugin-discovery/flynn-plugin.json` (`routes: discovery.${CLUSTER_DOMAIN}`, `auto_tls`), `flynn/host/host.go:530-549` (join uses instance URLs as `peerIPs`), `flynn/bootstrap/bootstrap.go:272-293` (`cluster.NewHostWithKey("", url, nil, nil, state.HostAuthKey())` → `GetStatus()`), `flynn/pkg/httpclient/json.go:85-86` (key sent as HTTP basic auth).
- **Description.** Upstream's discovery token is an unguessable URL that acts as a bearer capability. This plugin exposes the one-and-only cluster's token at an unauthenticated well-known path on a **public** route, and both registration and listing are unauthenticated and unrate-limited. `sourceIP()` also trusts the last `X-Forwarded-For` element.
- **Impact.** Anyone on the internet can (a) enumerate all host IPs/URLs of the cluster, (b) register arbitrary `url` values. A host joining with `--discovery <token>` adds those IPs as discoverd peers (DoS/raft poisoning); `bootstrap` iterates all registered URLs and performs `GetStatus()` **authenticated with the host auth key** over plain HTTP → the key is delivered to the attacker's server → full host API access (SEC-010).
- **Fix.** Remove or protect `/.well-known/cluster` (require `CONTROLLER_KEY` or restrict to the internal service address; `script/ready.sh` already reads it via the internal `discovery.discoverd` URL). Require a registration secret (e.g. the cluster token itself as a bearer, since it is meant to be secret) and validate that `inst.URL` host equals the request's real source IP. In `bootstrap`, never send the host key to URLs not on the configured peer list.

### 4.8 Other plugins

#### SEC-015 — Otel plugin `/exporters` API unauthenticated; leaks collector credentials; allows TLS-insecure exporters

- **Severity:** Medium · **Area:** Security · **Component:** `flynn-plugin-otel` · **Origin:** fork-introduced · **Effort:** S
- **Refs:** `flynn-plugin-otel/internal/server/server.go:21-32`, `:116-125` (`listExporters` returns full rows), `flynn-plugin-otel/internal/store/store.go:14-20` (`Headers`, `Insecure` serialised), `flynn-plugin-otel/internal/export/export.go:84-88` (`InsecureSkipVerify` when `Insecure`).
- **Description.** No auth on any route (compare the scheduler plugin, which requires `CONTROLLER_KEY`). `GET /exporters` returns the `Authorization` header configured for the external collector; `POST /exporters` lets any caller add a collector. The service has no public route, so exposure is the internal network: system-class jobs, build jobs (SEC-004 shows they reach the overlay), any job that becomes system-class (SEC-001), or SEC-003 service hijack.
- **Impact.** Theft of the Grafana/Datadog/etc. ingestion token; redirecting cluster/job stats to an attacker; SSRF-lite (POSTs of metric payloads to internal `http://` endpoints with a chosen `Authorization` header).
- **Fix.** Reuse the scheduler plugin's bearer check with `CONTROLLER_KEY`; redact `headers` in list responses.

#### SEC-026 — Scheduler plugin API: cluster key as shared secret, non-constant-time compare

- **Severity:** Low · **Area:** Security · **Component:** `flynn-plugin-scheduler` · **Origin:** fork-introduced · **Effort:** S
- **Refs:** `flynn-plugin-scheduler/internal/server/server.go:54-66`, `flynn-plugin-scheduler/cmd/scheduler/main.go:30-38`, `flynn-plugin-scheduler/internal/runner/runner.go:99-113`.
- **Description.** The API is protected by equality with `CONTROLLER_KEY` (`==`, not `subtle.ConstantTimeCompare`), so every scheduler CLI user must hold the cluster admin key; jobs are launched with `ReleaseEnv: true` for the stored app via the admin client. Auth is correctly present (positive), and the plugin has no public route.
- **Fix.** Constant-time compare; accept controller-issued app-scoped tokens and check `app:jobs:run` for the target app so the cluster key does not have to be shared.

#### SEC-023 — MongoDB plugin backup/restore use `--tlsAllowInvalidCertificates`

- **Severity:** Low · **Area:** Security · **Component:** `flynn-plugin-mongodb` · **Origin:** fork-introduced · **Effort:** S
- **Refs:** `flynn-plugin-mongodb/flynn-plugin.json:99`, `:119`, `:138`.
- **Description.** Datastore TLS is enabled by default (positive, see §5) but dump/restore disable verification, so a MITM on the overlay (SEC-003 hijack) can capture full database dumps.
- **Fix.** Pass the cluster CA (`--tlsCAFile`) instead.

#### REL-003 — Redis/Kafka/ClickHouse data and app volumes are not in the cluster backup; Redis is single-instance

- **Severity:** Low · **Area:** Reliability · **Component:** `flynn/pkg/backup`, `flynn-plugin-redis` · **Origin:** fork (plugins) · **Effort:** M
- **Refs:** `flynn/pkg/backup/backup.go:60-130`, `flynn/docs/content/production.html.md:318-350`.
- **Description.** `flynn cluster backup` dumps the controller Postgres and the sirenia Postgres/MariaDB/MongoDB appliances; Redis, Kafka, ClickHouse and job volumes are excluded and Redis runs as one process with no replication. **The docs state this accurately**, so this is a gap to close rather than a false claim.
- **Fix.** Add per-plugin backup hooks (RDB snapshot / `clickhouse-backup`), or at minimum a `flynn-plugin.json` `backup` capability that `pkg/backup` invokes.

### 4.9 Dependencies (govulncheck, go.mod)

#### SEC-009 — Vendored `github.com/opencontainers/runc v1.0.0-rc8` (2019) via `flynn/runc` replace

- **Severity:** High · **Area:** Security · **Component:** `flynn/host` (libcontainer), all plugins that import `flynn/host` types · **Origin:** inherited · **Effort:** L
- **Refs:** `flynn/go.mod:51`, `:132` (`replace … => github.com/flynn/runc v1.0.0-rc1001`), govulncheck: GO-2026-5761, GO-2025-4098, GO-2024-3110, GO-2023-1682/1683, GO-2022-0914/0452/0396 all "Found in runc@v1.0.0-rc8", fixed in ≥1.1.x–1.3.6; call trace via `host/host.go:81` `LinuxFactory.StartInitialization`.
- **Description.** flynn-host embeds libcontainer as a library with its own init, so not every runc CVE maps 1:1, but several are in code paths this fork uses: masked/readonly paths handling (CVE-2025-31133 family, GO-2025-4098 — the `/dev/null` bind-mount procfs redirect trick), `filepath-securejoin v0.2.2` (GO-2023-2048, symlink race in rootfs setup), and the 2022 `CAP_*` inheritable-set bug (GO-2022-0452). Build jobs (SEC-006) are exactly the context where these matter. Each plugin repo also reports the same 8 runc findings because they depend on `flynn/host/types` via the main module.
- **Fix.** Upgrade libcontainer to a maintained 1.2/1.3 line (significant API churn — `configs.Config`, cgroup managers, `nsexec` changes), or vendor only the types the plugins need so they stop inheriting the dependency.

#### SEC-017 — Other reachable vulnerable dependencies

- **Severity:** Medium · **Area:** Security · **Origin:** inherited (module set), fork for not upgrading · **Effort:** M
- **Refs:** `flynn/go.mod:58` `x/crypto v0.46.0`, `:59` `x/net v0.48.0`, `:63` `grpc v1.56.3`, `:67` `yaml.v2 v2.2.2`, `:89` `gorilla/websocket v1.4.1`, `:120` `x/text v0.32.0`, `:51` `selinux v1.2.2`; full list in Appendix A.
- **Notable reachable items.**
  - `gopkg.in/yaml.v2 v2.2.2` (GO-2020-0036, GO-2021-0061, GO-2022-0956 — CPU/memory DoS on crafted input) via `flynn/slugbuilder/artifact/main.go:242` parsing the **user's `Procfile`**.
  - `google.golang.org/grpc v1.56.3`: GO-2026-6443 (server panic on missing `:authority`), GO-2026-6348 (HTTP/2 DATA fragmentation OOM) via `controller/controller.go:134` `grpc.Server.Serve` and `discoverd/server/handler.go:168` (h2 transport) — remote DoS of the controller gRPC listener. GO-2026-4762 (xDS RBAC `:path` bypass) **resolved: not applicable** — `controller/grpc.go:158-186` authorises via `authz.GRPCAllowed(tok, info.FullMethod)` (`controller/authz/grpc.go:12-23`), which is token-based and only string-matches `Controller/Status` for app-scoped tokens; no xDS RBAC is used.
  - `golang.org/x/crypto/ssh v0.46.0`: seven GO-2026-* DoS/deadlock issues. **Resolved: not production-reachable** — every trace goes through `test/cluster/instance.go` (`ssh.Dial` in the integration-test harness); `gitreceive` serves git over HTTP behind the router and has no SSH server (`gitreceive/start.sh` only seeds client keys). Upgrade anyway to keep `govulncheck` clean.
  - `golang.org/x/net v0.48.0`: GO-2026-4918 (HTTP/2 infinite loop on bad `SETTINGS_MAX_FRAME_SIZE`) — router and controller are HTTP/2 servers.
  - Plugins: `jackc/pgx/v5 v5.8.0` (GO-2026-5004) in dashboard/discovery/scheduler/otel; `mongo-driver v1.17.6` (GO-2026-5327) in mongodb.
- **Fix.** `go get -u` the `x/*`, grpc, yaml (v2.4.0 or migrate to v3), websocket, pgx, mongo-driver modules and re-vendor; add `govulncheck` (with `GOOS=linux`) to CI for core and every plugin.

#### SEC-024 — BuildKit tarball fetched at build time without checksum

- **Severity:** Low · **Area:** Security/Reliability · **Component:** `flynn/dockerbuilder` · **Origin:** fork-introduced · **Effort:** S
- **Refs:** `flynn/dockerbuilder/builder/build.sh:42-46`.
- **Description.** `ensure_buildkit` pins `v0.23.2` but `curl … | tar -xzf -` with no digest check, and it runs inside the privileged build container on every build where the image does not already contain BuildKit. TLS to github.com is the only protection; a build also fails when GitHub is unreachable.
- **Fix.** Bake BuildKit into the dockerbuilder image (`img/`) and verify `sha256sum` against a pinned value in the fallback path.

### 4.10 CLI and installer

#### SEC-020 — `flynn cluster … ` TLS pin refresh dials with `InsecureSkipVerify` and overwrites the pin

- **Severity:** Medium · **Area:** Security · **Component:** `flynn/cli` · **Origin:** fork-introduced · **Effort:** S
- **Refs:** `flynn/cli/cluster.go:535-580` (`updateClusterTLSPin`), `:450-457`.
- **Description.** The pin-update path connects with certificate verification disabled, takes whatever leaf is presented and stores its SHA-256 as the new pin. Run at the wrong time (or on a compromised network) this converts a pinning mismatch — the very signal TOFU is meant to surface — into silent re-pinning to the attacker's cert.
- **Fix.** Show the new fingerprint and require explicit confirmation (`--yes` for scripts); when the controller is ACME/publicly signed, verify normally and pin the resulting chain instead.

#### SEC-022 — `~/.flynnrc` written with process umask

- **Severity:** Low · **Area:** Security · **Component:** `flynn/cli/config` · **Origin:** inherited · **Effort:** S
- **Refs:** `flynn/cli/config/config.go:207-209` (`os.Create(path)`).
- **Description.** Cluster keys/tokens are written with default mode (typically `0644`). Other local users can read them.
- **Fix.** `os.OpenFile(path, O_CREATE|O_TRUNC|O_WRONLY, 0600)` and `chmod` existing files on save.

#### SEC-027 — Legacy `script/install-flynn.tmpl` lacks checksum verification

- **Severity:** Low · **Area:** Security · **Component:** `flynn/script` · **Origin:** inherited · **Effort:** S
- **Refs:** `flynn/script/install-flynn.tmpl:185-240` vs `flynn/script/install-flynn-release` (`github_download_checksums`), `flynn/script/release:345-380`.
- **Description.** The **published** installer (`install-flynn-release`, produced by `script/release`) downloads `checksums.sha512` and verifies `flynn-host`/manifest before running — good. The template variant used by older docs does not. Both fetch the checksum file from the same origin as the binaries, so this is integrity, not authenticity (no signature).
- **Fix.** Delete or update `install-flynn.tmpl`; consider signing `checksums.sha512`.

### 4.11 Performance and reliability

#### PERF-001 — `GetAppJobsStats`/`GetAppJobsStatsEnriched` call `ListJobs` once or twice per job per host

- **Severity:** Medium · **Area:** Performance · **Component:** `flynn/controller` · **Origin:** fork-introduced · **Effort:** S
- **Refs:** `flynn/controller/stats.go:14-60`, `:102-125`.
- **Evidence.** For each host, `GetAllJobsStats()` (1 call), then for every job `isJobForApp(h, …)` → `h.ListJobs()` (HTTP) and `jobStatsIsInternal(h, …)` → `h.ListJobs()` again. A 5-host cluster with 200 jobs/host is ≈2,000 host HTTP calls per dashboard poll, executed serially, using a client with no timeout (REL-001).
- **Fix.** Call `ListJobs()` once per host, index by job ID, filter locally; run hosts concurrently with a bounded `errgroup` and per-request context deadline.

#### REL-001 — Cluster/host HTTP clients have no timeouts; stats endpoints hang on a stuck host

- **Severity:** Medium · **Area:** Reliability · **Component:** `flynn/pkg/httphelper`, `flynn/pkg/cluster` · **Origin:** inherited client; fork-introduced callers · **Effort:** S
- **Refs:** `flynn/pkg/httphelper/httphelper.go:27` (`RetryClient = &http.Client{Transport: &http.Transport{Dial: dialer.Retry.Dial}}`), `flynn/pkg/cluster/client.go:30`, `flynn/pkg/cluster/host.go:37-51`.
- **Description.** No `Timeout`, `ResponseHeaderTimeout` or context deadline. `GetClusterStats`, `GetAppJobsStats`, the dashboard metrics poller and the otel exporter loop (`flynn-plugin-otel/internal/export/export.go:57-65`, which *does* set a 45 s client timeout on its side) all fan out through this client; one host with a wedged `flynn-host` blocks the controller handler goroutine indefinitely, and repeated polls accumulate.
- **Fix.** Give `RetryClient` a `Transport.ResponseHeaderTimeout` (e.g. 10 s) and honour request contexts; in stats handlers use `context.WithTimeout` per host.

#### REL-002 — Restart backoff caps at 30 s forever

- **Severity:** Low · **Area:** Reliability · **Component:** `flynn/controller/scheduler` · **Origin:** inherited · **Effort:** S
- **Refs:** `flynn/controller/scheduler/scheduler.go:2892-2900`.
- **Description.** `getBackoffDuration` returns 0 / 10 s / 30 s; a permanently crashing process restarts every 30 s indefinitely, generating events, log volume and image mounts with no alert or circuit breaker.
- **Fix.** Exponential backoff to a few minutes with jitter; emit a `crash_loop` event after N restarts.

### 4.12 discoverd internals and service metadata (second pass)

#### SEC-028 — Controller publishes `CONTROLLER_KEY` as discoverd instance metadata

- **Severity:** High · **Area:** Security · **Component:** `flynn/controller`, `flynn/discoverd` · **Origin:** inherited (upstream design; `router/store.go`, `status`, `updater` all consume it) · **Effort:** M
- **Refs:** `flynn/controller/controller.go:94-100`, consumers: `flynn/router/store.go:42`, `flynn/router/acme/acme.go:183`, `flynn/updater/updater.go:83`, `flynn/status/status.go:156`, `flynn/host/cli/events.go:149`, `flynn/host/cli/github_updater.go:1373`.
- **Evidence.**

  ```go
  // controller/controller.go:94
  httpService, err := discoverd.DefaultClient.AddServiceAndRegisterInstance("controller", &discoverd.Instance{
      Addr: httpAddr, Proto: "http",
      Meta: map[string]string{ "AUTH_KEY": os.Getenv("AUTH_KEY") },
  })
  ```

- **Description.** The cluster admin key is stored in plaintext in discoverd's replicated state and returned by `GET /services/controller/instances` to any caller. Because discoverd has no authentication (SEC-003), the "secret" is readable by anyone who can open a TCP connection to `:1111` on any node: the underlay/VPC network, a host without netpolicy applied, a system- or datastore-class container, or a compromised plugin job. DNS does not expose `Meta` (`discoverd/server/dns.go`), so user containers are excluded only by the iptables rules.
- **Impact.** One unauthenticated HTTP GET from the node network yields full cluster admin, without any of the MITM steps discussed in SEC-003.
- **Fix.** Stop publishing the key: give router/status/updater their own env-injected credential (the plugin `inject_env` mechanism already does this for plugins), or at minimum authenticate discoverd (SEC-003) and mark `AUTH_KEY` as a redacted meta key that the API never returns.

#### REL-009 — discoverd: initial subscription snapshot is sent while holding the store lock; raft library is a 2016 pre-release

- **Severity:** Low · **Area:** Reliability · **Component:** `flynn/discoverd/server` · **Origin:** inherited · **Effort:** S/M
- **Refs:** `flynn/discoverd/server/store.go:1032-1091` (`Subscribe` blocking sends under `s.mu`, with the TODO), `:1093-1120` (`broadcast` non-blocking send, closes slow subscribers — good), `flynn/discoverd/server/handler.go:27` (`StreamBufferSize = 64`), `flynn/go.mod:33` (`hashicorp/raft v0.0.0-20160603…`), `store.go:126-128` (1 s heartbeat/election, 500 ms lease).
- **Description.** Steady-state event fan-out is bounded (positive), but `Subscribe(sendCurrent=true)` writes every current instance into the subscriber's 64-slot channel with blocking sends while holding `s.mu`. For a service with >64 instances and a slow consumer (e.g. a router on a loaded host streaming `flynn-net-user`, which has one entry per user job), every raft `Apply`, heartbeat and DNS lookup on that node stalls until the consumer drains. Leader-change handling is otherwise correct (heartbeat map reset + 2×TTL grace in `EnforceExpiry`). The raft library predates hashicorp/raft 1.0 and its snapshot/restore and membership fixes; with 1 s election timeouts, a 3-node cluster loses quorum on any two-node hiccup and the remaining node cannot serve writes (registrations) until quorum returns — expected raft semantics, but worth stating because sirenia failover depends on it.
- **Fix.** Snapshot instances under the lock, release it, then send with a timeout; upgrade hashicorp/raft (API changes are contained to `store.go`).

### 4.13 Network overlay (flannel)

#### SEC-033 — Flannel subnet leases live in unauthenticated discoverd service metadata; overlay is unencrypted

- **Severity:** Low · **Area:** Security · **Component:** `flynn/flannel` · **Origin:** inherited · **Effort:** M
- **Refs:** `flynn/flannel/discoverd/registry.go:143-160` (`setNetwork` → `service.SetMeta`), `flynn/flannel/main.go:166-168`, `flynn/bootstrap/manifest_template.json:60-80` (`BACKEND` from `FLANNEL_BACKEND`), `flynn/flannel/backend/vxlan/vxlan.go:57` (`PublicIP` in lease attrs), `flynn/script/bootstrap-flynn:135` (`alloc` in dev).
- **Description.** The subnet→host map (including each host's VXLAN VTEP MAC and public IP) is the `flannel` service meta in discoverd. With SEC-003, any node-network attacker can rewrite it and every host will reprogram its FDB/routes to send a victim subnet's traffic to an attacker-chosen IP; the VXLAN/host-gw backends carry no encryption or authentication, so plaintext controller/Postgres traffic on the overlay is exposed to anyone on the underlay regardless. Rated Low because SEC-028 already hands the same attacker the admin key; this becomes relevant once SEC-003/SEC-028 are fixed.
- **Fix.** Authenticate discoverd (SEC-003); consider WireGuard between hosts or at least TLS for controller↔postgres.

#### REL-010 — Subnet leases never expire

- **Severity:** Low · **Area:** Reliability · **Component:** `flynn/flannel` · **Origin:** inherited (fork's discoverd registry) · **Effort:** S
- **Refs:** `flynn/flannel/discoverd/registry.go:162-166` ("set a far future expiry as discoverd meta does not currently support ttl", `AddDate(10,0,0)`), `flynn/flannel/subnet/subnet.go:191-220` (renewal logic that is effectively inert).
- **Description.** Subnets of decommissioned hosts are never reclaimed; with the default `/16` network and `/24` per host, roughly 250 host lifetimes exhaust the space, after which new hosts cannot join. Manual cleanup requires editing discoverd meta.
- **Fix.** Tie leases to the host's discoverd instance (expire when the `flannel` instance for that host goes away) or add a `flynn-host` command to prune leases.

### 4.14 Log aggregation

#### SEC-030 — Logaggregator HTTP API and syslog ingest are unauthenticated on the overlay

- **Severity:** Medium · **Area:** Security · **Component:** `flynn/logaggregator` · **Origin:** inherited · **Effort:** M
- **Refs:** `flynn/logaggregator/api.go:25-32` (`GET /log/:channel_id`, `/cursors`, `/snapshot`, no auth), `flynn/logaggregator/server.go:152-190` (`runSyslog`/`drainSyslogConn`: no auth, no read deadline), `flynn/logaggregator/aggregator.go:33,124` (`Feed` → `getBuffer(msg.AppName)`), `flynn/logaggregator/buffer/buffer.go:33` (`DefaultCapacity = 10000` lines per channel), `flynn/bootstrap/manifest_template.json:521-540` (ports 80 and 514).
- **Description.** The controller front-door (`GET /apps/:id/log`) is authorised, but the aggregator itself trusts everything on the overlay. Build-class containers are not blocked from system-class overlay IPs (SEC-004), so attacker code in a `git push` can `GET http://logaggregator.discoverd/snapshot` (all channels, all apps) or `GET /log/<appID>?lines=10000`, and can open TCP 514 to inject RFC 5424 frames with any `APP-NAME`, forging log lines into another app's stream (e.g. fake deploy/audit messages) or creating unbounded numbers of new channels (each up to 10 000 buffered messages → memory growth on the aggregator, which has the default 1 GiB limit). Application logs routinely contain secrets. Retention is memory-only, 10 000 lines/channel; `/snapshot` is used by the host `logmux` for handover, not for durable storage.
- **Fix.** Require the cluster key (or a scoped token) on the HTTP API; accept syslog only from `flynn-host` (bind 514 to the host bridge and firewall the overlay, or add a shared secret in structured data); cap the number of channels and reject unknown app IDs (the aggregator can check the controller).

### 4.15 sirenia (Postgres / MariaDB / MongoDB HA)

#### REL-008 — Singleton mode auto-promotes a fresh peer without fencing the old primary

- **Severity:** Medium · **Area:** Reliability · **Component:** `flynn/pkg/sirenia/state` · **Origin:** fork-introduced · **Effort:** M
- **Refs:** `flynn/pkg/sirenia/state/state.go:862-874` (`canSingletonTakeover`), `:876-912` (`startSingletonTakeover`, `InitWAL: p.db.XLog().Zero()`), `:590-600` (called from the unassigned path), `flynn/controller/scheduler/scheduler.go:47-51`, `:1310-1314`, `:1810-1812` (`ErrVolumeInUse` defer), `flynn/appliance/postgresql/process.go:490-503` (standby reuses initialized data dir), `:596-611` (`verifyOrReseedStandby`).
- **Description.** Upstream's singleton (one-node-write) mode freezes the cluster; if the primary disappears an operator must intervene. The fork adds an automatic takeover: an *unassigned* peer configured as singleton promotes itself the moment the recorded primary is absent from discoverd. There is no fencing in singleton mode (no synchronous standby to block the old primary's commits), so if the old primary is alive but unregistered — discoverd expiry during host overload, a partitioned host, or a stuck deregistration — two writable primaries coexist until the old one next evaluates state and sees the new generation (`assumeDeposed`). Clients that already hold connections keep writing to the old one. The scheduler mitigates the *empty-dataset* variant well: `shouldDeferVolumeAllocation` makes placement wait while the old volume is held, so a blank database is not created during a normal deploy. **Needs confirmation** for the host-down path: if the controller marks the host down, the volume is unavailable and the replacement is placed elsewhere with a *new* volume; the fork then auto-promotes that empty peer (upstream would have frozen), which is silent data loss until an operator restores from backup.
- **Fix.** Before promoting, require either (a) proof the old primary is stopped (host reports job stopped) or (b) the volume to be the same dataset; when a brand-new volume is used, refuse to promote unless `SIRENIA_ALLOW_EMPTY_PROMOTE=1`, and emit an event.

#### Positive notes on sirenia

Normal (non-singleton) mode keeps the Manatee invariants: the primary uses `synchronous_commit = remote_write` with `synchronous_standby_names` set to the sync (`appliance/postgresql/process.go:1217-1218`), so a primary that loses its sync blocks commits rather than diverging; takeover requires the candidate's WAL ≥ `InitWAL` (`state.go:1127-1131`); deposed peers re-join only after the new primary lists them (`state.go:756-763`) and standbys detect timeline divergence and re-seed via `pg_basebackup` (`process.go:641-676`). The fork's identity-by-appliance-ID reconciliation (`presentPeer`/`peersEqual`) fixes real rolling-deploy hangs. MariaDB "lagging but advancing" replicas are now kept instead of re-seeded (commit `fa5dd952`) — sensible.

### 4.16 Router: TLS/ACME (second pass)

#### REL-007 — Managed (Let's Encrypt) certificates are never renewed and failed issuances are never retried

- **Severity:** High · **Area:** Reliability · **Component:** `flynn/router/acme`, `flynn/controller` · **Origin:** fork-introduced (ACME support is fork code) · **Effort:** M
- **Refs:** `flynn/controller/data/managed_certificate.go:87` (`ListExpiring` — **zero callers** in the tree), `flynn/router/acme/acme.go:331-360` (`Run` streams managed certificates and `:357` skips anything that is not `pending`), `:408-521` (every error path sets `Failed` and returns; nothing re-queues), `flynn/docs/content/apps.md:288` ("Flynn can automatically provision and renew TLS certificates"), `flynn/host/cli/acme.go:402`.
- **Evidence.** `rg -n "ListExpiring" flynn/` → only the definition and the query name. `rg -i renew flynn/router flynn/controller` → no code path.
- **Impact.** Every `auto_tls` route (including the discovery, dashboard and www plugins' routes) serves an expired certificate ~90 days after issuance; a transient ACME failure (rate limit, DNS not yet propagated) leaves the route permanently `failed` with no automatic retry, and an operator must delete/recreate the route. This contradicts the documentation.
- **Fix.** Add a renewal loop in the ACME service: every N hours call `ListExpiring(now+30d)`, set status back to `pending`, and let `handleCertificate` reissue; retry `failed` certificates with exponential backoff; surface `last_error` in `flynn route list`.

#### Router TLS/ACME positives and minor notes

`tlsconfig.SecureCiphers` with `MinVersion = TLS1.2` unless `LegacyTLSVersions` (`router/http.go:378-387`); TCP TLS termination also 1.2 (`router/tcp.go:208`). The ACME account key is P-256, stored in the controller DB, and `GET /acme/config` strips it (`controller/acme_config.go:18-22`); challenge tokens are looked up from a discoverd-backed cache on `/.well-known/acme-challenge/` (`router/http.go:470-500`). Order polling is bounded (5 min, `acme.go:559-575`). No handling of ACME `Retry-After`/rate-limit responses beyond that bound (folds into REL-007). Header handling: see SEC-025; slowloris on 443: REL-006.

### 4.17 flynn-host update, updater, status, volumes

#### SEC-032 — Rolling update distributes binaries over plaintext HTTP without checksum verification on the receiving host

- **Severity:** Medium · **Area:** Security · **Component:** `flynn/host/cli`, `flynn/host/downloader` · **Origin:** fork-introduced (tarball/coordinator update path) · **Effort:** S
- **Refs:** `flynn/host/cli/github_updater.go:1826-1836` (coordinator starts `http.FileServer` on the node IP, `http://…`), `:339-420` (`updateRemoteBinaries` → `h.PullBinariesAndConfig(..., baseURL)`), `flynn/host/http.go:442-484` (`PullBinariesAndConfig` uses `downloader.NewWithBaseURL(query.Get("base-url"))`), `flynn/host/downloader/downloader.go:89-133`, `:200-310` (`DownloadBinaries`/`downloadGzippedBinary`: no checksum; only image layers are verified at `:472-504`).
- **Description.** The coordinator host verifies GitHub downloads against `checksums.sha512` (`github_updater.go:201-222`, positive) but then re-serves the binaries to every other node over unauthenticated plaintext HTTP, and the receiving `flynn-host` installs whatever it gets from the caller-supplied `base-url` without hashing. The request that triggers the pull is host-key-authenticated, but the download itself is not integrity-checked, so an on-path attacker on the node network (ARP/route spoofing, SEC-033) can substitute `flynn-host`/`flynn-init` binaries during an update. The `--tarball` path has a correct path-traversal guard (`:1940-1943`) but also no signature on the tarball.
- **Fix.** Pass the expected SHA-512 map to `PullBinariesAndConfig` and verify in `Downloader.DownloadBinaries`; or serve over the host API (`:1113`) with host-key auth instead of a temporary file server.

#### REL-011 — No automatic rollback when a rolling update fails mid-way

- **Severity:** Low · **Area:** Reliability · **Component:** `flynn/host/cli` · **Origin:** fork-introduced · **Effort:** M
- **Refs:** `flynn/host/cli/github_updater.go:905-1000` (versioned binaries + symlink swap), `:216-227` (install `flynn-host` first, then re-exec into the new updater), `:485` (health-wait replaces blind sleep).
- **Description.** Binaries are installed as `name.<version>` with a symlink, so a manual rollback is one `ln -sf`, and the coordinator re-execs into the new binary before touching other nodes — good. But if node N of M fails health checks the update stops with hosts on mixed versions and nothing reverts the symlinks or the controller's image manifest; there is no `flynn-host update --rollback`. The `updater` job (`flynn/updater/updater.go`) deploys system apps sequentially and skips already-current ones, which is idempotent but not transactional.
- **Fix.** Record the previous version per host and add `flynn-host update --rollback[=version]`; abort-and-revert when a host fails post-update health.

#### SEC-034 — Volume API accepts caller-supplied volume IDs used in dataset and mount paths

- **Severity:** Low · **Area:** Security · **Component:** `flynn/host/volume` · **Origin:** inherited · **Effort:** S
- **Refs:** `flynn/host/volume/api/http.go:86-111` (`Create` decodes `volume.Info` incl. `ID` from the body), `flynn/host/volume/zfs/zfs.go:331-333` (`datasetPath` = `filepath.Join(dataset, type, info.ID)`), `:327-329` (`mountPath` under `WorkingDir/mnt`).
- **Description.** `info.ID` is not validated as a UUID; `filepath.Join` normalises `../` so an ID like `../../x` yields a dataset outside `flynn-default/data` and a mountpoint outside the volumes directory. The API is behind host auth (SEC-010), so today this only matters when `:1113` is open or the key has leaked (SEC-028).
- **Fix.** Reject IDs that do not match `^[0-9a-f-]{36}$` in `NewVolumeFromProvider`.

#### STAB-003 — No ZFS quota on data volumes

- **Severity:** Medium · **Area:** Stability · **Component:** `flynn/host/volume/zfs` · **Origin:** inherited · **Effort:** S
- **Refs:** `flynn/host/volume/zfs/zfs.go:179-217` (`CreateFilesystem` with only `mountpoint`), `rg refquota host/volume` → none; `flynn/host/volume/zfs/zpool.go:79-92` (zpool sized to 70 % of the device or a 100 GB file).
- **Description.** Every app/datastore volume shares one zpool with no `quota`/`refquota`; a single runaway volume (or a build's temp disk if it lands on the same pool) fills the pool and every Postgres/MariaDB/MongoDB/Kafka on that host stops writing simultaneously. `temp_disk` requests are enforced for scratch space but not for persistent volumes.
- **Fix.** Set `refquota` from a per-volume size in `volume.Info` (default e.g. 20 GiB, overridable via the release), and alert at 80 % pool usage.

#### Status service

`flynn/status` answers unauthenticated for RFC 1918/CGNAT source ranges and otherwise requires `AUTH_KEY` (`status/status.go:35-75`); the source IP is taken from the *last* `X-Forwarded-For` element, which the router appends, so it is not spoofable through the router (it would be if `status` were exposed without the router). It reads the controller key from discoverd meta (SEC-028). No new finding.

### 4.18 Datastore plugins and appliance (Go code)

#### SEC-029 — Datastore appliance admin HTTP APIs are unauthenticated and reachable from user containers

- **Severity:** High · **Area:** Security · **Component:** `flynn/appliance/postgresql`, `flynn-plugin-mariadb`, `flynn-plugin-mongodb`, `flynn-plugin-redis`, `flynn-plugin-kafka`, `flynn-plugin-clickhouse`, `flynn/pkg/netpolicy` · **Origin:** inherited pattern (upstream postgres `/stop`), fork-introduced surface (`/backup`, `/restore`, Kafka topic/consumer-group management, netpolicy datastore ACCEPT) · **Effort:** M
- **Refs:** `flynn/pkg/iptables/iptables.go:127-128` (`UserToDatastoreArgs`: user set → datastore set `ACCEPT`, **no `--dport`**), `flynn/pkg/netpolicy/policy.go:126-140` (`isDatastoreProcess`: every non-`web` process of a datastore app), `flynn/appliance/postgresql/handler.go:33` (`POST /stop`; `HTTP_PORT` 5433), `flynn-plugin-mariadb/handler.go:35-38` (`GET /backup`, `GET /tls`, `POST /stop`), `flynn-plugin-mariadb/cmd/flynn-mariadb/main.go:36` (`3307`), `flynn-plugin-mongodb/cmd/flynn-mongodb/main.go:36` (`27018`), `flynn-plugin-redis/handler.go:28-30` (`POST /stop`, `POST /restore`), `flynn-plugin-redis/cmd/flynn-redis/main.go:26` (`:6380`), `flynn-plugin-kafka/handler.go:32-44` (topics and consumer groups: list/create/configure/delete), `flynn-plugin-clickhouse/handler.go`, `flynn/discoverd/server/dns.go:222,458` (user jobs may resolve `leader.<datastore>.discoverd`).
- **Description.** Each datastore container runs, beside the database port, a sirenia-style management HTTP server with no authentication of any kind (`rg -n "Authorization|BasicAuth" flynn-plugin-*/handler.go` → nothing). Netpolicy intentionally lets user containers reach datastore-class IPs so apps can use their databases, but the rule matches **all ports**, and the DNS filter lets a user job resolve `leader.mariadb.discoverd`/`leader.postgres.discoverd` etc. to obtain the IP.
- **Impact (from any user container, no credentials).** `GET http://leader.mariadb.discoverd:3307/backup` streams a `mariabackup` of the whole instance — every app's databases; `POST …/stop` on postgres/mariadb/mongodb/redis stops the process (sirenia fails over; repeated calls keep the cluster in failover churn, and on the controller's own Postgres this is a platform outage); `POST redis:6380/restore` replaces a Redis dataset; Kafka `DELETE /topics/:name` / consumer-group deletion destroys other tenants' data; ClickHouse equivalents. Redis/MariaDB/Mongo/Kafka/ClickHouse TLS-by-default protects the database ports but not these plaintext admin ports.
- **Fix.** Restrict `UserToDatastoreArgs` to the database ports (`--dport 5432,3306,27017,6379,9093,9440`, one rule per protocol, or a port-aware ipset), and additionally require a bearer token (the appliance already has `CONTROLLER_KEY`-holding callers: `pkg/backup`, the provider `web` process) on every non-`/.well-known/status` route.

#### Datastore plugin positives

Provider APIs (`/databases`, `/clusters`) run as `web` processes classified system-class and are therefore not reachable from user containers; credentials are generated with `pkg/random`; TLS is enabled by default for MariaDB/Mongo/Redis/Kafka/ClickHouse (`*_TLS_ENABLED=true`) with certificates from the cluster CA; MongoDB dump/restore is the only place verification is disabled (SEC-023). Kafka runs KRaft (no ZooKeeper) with node IDs derived from the appliance ID. ClickHouse strips file capabilities from the binary at image build (`img/packages.sh:31-40`). The scheduler plugin's job execution requires `CONTROLLER_KEY` and only launches the app's *current* release with `ReleaseEnv: true` (SEC-026 covers the shared-key concern).

### 4.19 Dashboard frontend/backend depth and supply chain

#### SEC-035 — Dashboard: websocket lacks an Origin check, webhook secret accepted in the query string, dev-token default is `true` in code

- **Severity:** Low · **Area:** Security · **Component:** `flynn-plugin-dashboard` · **Origin:** fork-introduced · **Effort:** S
- **Refs:** `flynn-plugin-dashboard/cmd/dashboard/main.go:237-243` (`websocket.New(...)` default config: all origins), `internal/runjobws/handler.go:18-50` (auth = `session` cookie + per-app `jobs:run` check — good), `internal/webhook/ingest.go:23-26` (`?secret=` accepted), `internal/config/config.go:93` (`ALLOW_DEV_TOKEN` default `"true"`), `flynn-plugin.json:46` (`"false"` when installed as a plugin), `internal/auth/auth.go:177-178,304-305` (`HttpOnly`, `Secure` when the public URL is https, `SameSite=Lax`).
- **Description.** CSRF: state-changing API calls are JSON `POST`s authenticated by a `SameSite=Lax` cookie, which modern browsers do not attach to cross-site `fetch`/`XMLHttpRequest`, so classic CSRF is mitigated without a token; there are no state-changing `GET`s. The run-job websocket relies on the same cookie and never checks `Origin`; Lax also withholds cookies from cross-site WebSocket handshakes in current Chromium/Firefox/WebKit, so cross-site WebSocket hijacking is mitigated by browser policy only — a `Strict`/`Lax` regression or an older browser would expose a job-exec channel. XSS: no `dangerouslySetInnerHTML`/`innerHTML`/`eval` sinks in `web/src` (React escaping; external links use `rel="noreferrer"`). Webhook ingest accepts the shared secret as `?secret=`, which ends up in router/access logs. The Go default of `ALLOW_DEV_TOKEN=true` is only safe because the manifest overrides it.
- **Fix.** Set `websocket.Config{Origins: []string{publicURL}}`; drop `?secret=` support; flip the code default to `false`; consider `SameSite=Strict` for the session cookie plus a CSRF header check on `/api/ws`.

#### SEC-031 — Build/base image supply chain: unverified toolchain and vendor tarballs, unpinned apt packages

- **Severity:** Medium · **Area:** Security · **Component:** `flynn/builder/img`, `flynn/dockerbuilder/img`, plugin `img/packages.sh` · **Origin:** fork-introduced (Go 1.24 / Noble / Kafka / BuildKit scripts) · **Effort:** S
- **Refs:** `flynn/builder/img/go.sh:6-16` (`go1.24.12.linux-*.tar.gz` fetched by `curl … | tar`, no `sha256` check — this toolchain compiles **every** Flynn binary in the release), `flynn/builder/img/ubuntu-noble.sh:16-41` (Ubuntu cloud image verified against `SHA256SUMS` from the same origin, but from the rolling `releases/noble/release/` pointer — not date-pinned, so builds are not reproducible; the comment at `:39-41` acknowledges the cache hazard), `flynn/dockerbuilder/img/packages.sh:33` (BuildKit tarball, no checksum; see also SEC-024), `flynn-plugin-kafka/img/packages.sh:19-22` (Kafka distribution tarball, no `.sha512`/`.asc` check), `flynn-plugin-{mariadb,mongodb,clickhouse}/img/packages.sh` (vendor apt repos with GPG keys fetched over HTTPS — signed by apt, acceptable), all `img/packages.sh` (`apt-get install` without version pins), `flynn/apt_cache/` (contains only `lock`/`partial`; no pin or snapshot lists).
- **Description.** Go publishes `.sha256` files for every toolchain download and Apache publishes `.sha512` + PGP for Kafka; neither is checked. Apt packages are unpinned, so two builds of the same tag can differ. All fetches are HTTPS, so the practical exposure is a compromised upstream mirror/CDN or a MITM of the build host.
- **Fix.** Pin and verify: `echo "<sha> go.tar.gz" | sha256sum -c`, Kafka `.sha512` + `gpg --verify` against `KEYS`, BuildKit `.sha256`; use a dated Ubuntu image URL; record `apt-get` versions in a lockfile (`apt-mark`/`dpkg -l` snapshot) and install with `pkg=version`.

### 4.20 Hygiene

#### INFO-001 — `flynn/dashboard/` and `flynn/rails-dashboard/` are untracked leftovers

`git ls-files dashboard rails-dashboard` returns 0 files; upstream commit `f48c672b all: Remove dashboard` deleted the old dashboard. The directories present in the checkout are local, ignored artefacts and are **dead relative to the repository**; the live dashboard is `flynn-plugin-dashboard`. Safe to delete locally.

#### INFO-002 — Cluster backups contain all secrets in plaintext

`pkg/backup` serialises the controller DB (env vars, `CONTROLLER_KEY`, signing keys). Expected for a restore artefact; the docs should say "encrypt at rest" explicitly.

## 5. Positive observations

- **Fine-grained authz model** (`controller/authz`): explicit permission constants, role expansion, `IsPlatformAppName`/`SystemAppAllowed` blocks non-admins from touching system apps, and `stripPrivilegedProcessTypes`/`sanitizeAppMetaUpdate` prevent users from defining `slugbuilder`/`dockerbuilder` process types or setting `flynn-system-app` on existing apps.
- **GitHub webhook** verifies HMAC before processing; GitHub App private keys stay controller-side; taffy clones only from the admin-configured GitHub API host (no SSRF found).
- **User jobs** get a per-job user namespace (`host/userns.go`), capability drop, `no_new_privs`, AppArmor `flynn-default` (fail-closed on error), a seccomp filter, and DNS-level scoping of discoverd names (`UserMayResolveDiscoverd`).
- **Network policy** (`pkg/iptables`) with ipsets mirrored from discoverd is a sound design; user→overlay NEW is dropped, user→node and user→host are dropped except DNS, and `EnableHostIsolation` actively removes the older rules that let containers reach discoverd `:1111`.
- **Sirenia normal mode** keeps synchronous-replication fencing and WAL-position checks on takeover; standbys detect timeline divergence and re-seed; the fork's identity reconciliation fixes real rolling-deploy hangs (§4.15).
- **Update path**: GitHub downloads are checksum-verified, binaries are installed as versioned files behind a symlink (manual rollback is trivial), the coordinator re-execs into the new `flynn-host` before touching other nodes, and tarball extraction has a path-traversal guard.
- **discoverd** event fan-out drops slow subscribers instead of blocking; leader-change handling waits 2×TTL before expiring instances.
- **Router TLS**: TLS 1.2 minimum with a curated cipher list by default; ACME account key never leaves the controller via the API.
- **Dashboard frontend**: no raw-HTML sinks; session cookies are `HttpOnly`/`Secure`/`SameSite=Lax`; the run-job websocket enforces per-app `jobs:run`.
- **Layer integrity**: squashfs layers are verified against `sha512_256` from the artifact manifest before mount (`host/downloader`), so tampering with blobstore layers is DoS not RCE.
- **Secrets**: bootstrap generates 128-bit random keys; `host.json` saved `0600`; datastore plugins enable TLS by default (`*_TLS_ENABLED=true`) and Redis uses `requirepass` with a generated password.
- **Event streaming** uses bounded per-subscriber queues and closes slow consumers (`controller/data/events.go`), so a stalled client cannot exhaust controller memory.
- **Published installer** verifies `checksums.sha512`; plugin hook `script/ready.sh` reads the discovery token via the internal URL.
- **Scheduler plugin** requires authentication on its API; **otel exporter** uses bounded client timeouts and a 1 MiB body limit.
- Docs (`production.html.md`) are honest about what `cluster backup` excludes.

## 6. Appendix

### A. govulncheck summary

Command (core): `cd flynn && GOOS=linux GOARCH=amd64 GOFLAGS=-mod=vendor govulncheck ./...` → exit 3. Native darwin run failed with type-check errors (Linux-only syscalls/`configs.NEW*`), so the Linux run is authoritative.

Core (`flynn/`): **21 vulnerabilities reachable from 9 modules**; 9 more in imported packages and 20 in required modules not reachable.

| ID | Module (found → fixed) | Reachable via |
|---|---|---|
| GO-2026-6443, -6348, -6061, -4762 | grpc v1.56.3 → 1.79.3–1.83.1 | `controller/controller.go:134`, `discoverd/server/handler.go:168` |
| GO-2026-6355, -6354, -5020, -5019, -5018, -5017, -5013 | x/crypto v0.46.0 → 0.52–0.56 | `test/cluster/instance.go:265` (`ssh.Dial`) — test harness only; no production SSH server (resolved) |
| GO-2026-5026, -4918 | x/net v0.48.0 → 0.53/0.55 | `pkg/httprecorder` (http2 transport / idna) |
| GO-2026-6278 | gorilla/websocket v1.4.1 → 1.5.3 | `controller/client/v1/client.go:760` |
| GO-2026-5970 | x/text v0.32.0 → 0.39.0 | http transport → `norm.Form.Bytes` |
| GO-2025-4098 | opencontainers/selinux v1.2.2 → 1.13.0 | `host/host.go:81` `label.FormatMountLabel` |
| GO-2025-3372 | golang/glog v1.1.0 → 1.2.4 | `flannel/subnet/subnet.go:427` |
| GO-2023-2048 | filepath-securejoin v0.2.2 → 0.2.4 | `host/host.go:81` `filepath.SecureJoin` |
| GO-2022-0956, GO-2021-0061, GO-2020-0036 | yaml.v2 v2.2.2 → 2.2.4/2.2.8 | `slugbuilder/artifact/main.go:242` |

Plugins (`GOOS=linux GOARCH=amd64 govulncheck ./...`, filtered):

| Repo | Reachable | Modules |
|---|---|---|
| dashboard, discovery, otel | 10 | runc rc8 (8), x/text, pgx/v5 v5.8.0 (GO-2026-5004) |
| scheduler | 10 | runc rc8 (8), x/text, pgx (legacy 2016 + v5) |
| mongodb | 10 | runc rc8 (8), x/text, mongo-driver v1.17.6 (GO-2026-5327) |
| redis, mariadb, kafka, clickhouse, www | 8 | runc rc8 |

(`template` was not scanned — it has no `go.mod` module of its own beyond the scaffold.)

### B. Files/dirs reviewed

`flynn/`: `controller/{authorizer,authz,tokensigner,data,scheduler,utils}/`, `controller/{controller,jobs,release,routes,stats,events,internal_process,github*}.go`, `host/{http,host,libcontainer_backend,userns,netpolicy,job_services}.go`, `host/config/auth.go`, `host/downloader/`, `host/cli/plugin*.go`, `router/{http,tcp,tls}.go`, `router/proxy/`, `discoverd/server/{handler,dns}.go`, `blobstore/`, `gitreceive/server.go`, `gitreceive/receiver/flynn-receive.go`, `slugbuilder/builder/{build.sh,install-buildpack}`, `slugbuilder/artifact/main.go`, `dockerbuilder/builder/build.sh`, `taffy/`, `tarreceive/` (scope check only), `bootstrap/{manifest_template.json,gen_random_action.go,configure_host_auth_action.go,bootstrap.go}`, `bootstrap/discovery/discovery.go`, `pkg/{netpolicy,iptables,plugin,cluster,httphelper,httpclient,backup}/`, `cli/config/config.go`, `cli/cluster.go`, `script/{install-flynn.tmpl,install-flynn-release,release}`, `schema/controller/new_job.json`, `docs/content/{security.md,production.html.md}`, `go.mod`, `.github/workflows/release.yml`.

Second pass additions (`flynn/`): `pkg/sirenia/state/state.go`, `appliance/postgresql/{process,handler}.go` + `cmd/flynn-postgres/main.go`, `discoverd/server/store.go`, `logaggregator/{api,server,aggregator}.go`, `logaggregator/buffer/buffer.go`, `flannel/{main.go,discoverd/registry.go,subnet/subnet.go,backend/vxlan/vxlan.go}`, `host/cli/{update,github_updater,update_rollout,acme}.go`, `host/downloader/downloader.go` (binary path), `host/http.go` (`PullBinariesAndConfig`, `Update`), `updater/updater.go`, `status/status.go`, `host/volume/{api/http.go,zfs/zfs.go,zfs/zpool.go,manager/manager.go}`, `host/resource/resource.go`, `router/acme/{acme,account,responder}.go`, `router/{store,http,tcp}.go` (TLS config), `controller/{acme_config,managed_certificate,grpc}.go`, `controller/authz/grpc.go`, `controller/data/managed_certificate.go`, `controller/scheduler/scheduler.go` (volume adoption, controller job sync), `pkg/iptables/iptables.go` (complete), `builder/img/{ubuntu-noble,go}.sh`, `builder/manifest.json`, `dockerbuilder/img/packages.sh`, `apt_cache/`, `docs/content/apps.md` (ACME section).

Plugins: all `flynn-plugin.json`; `flynn-plugin-dashboard/{cmd/dashboard/main.go,internal/{auth,tokens,store,runjobws,webhook,config},web/src (sink grep)}`; `flynn-plugin-discovery/{internal/server,internal/store,script/ready.sh,README.md}`; `flynn-plugin-scheduler/{cmd/scheduler,internal/server,internal/runner,internal/cli}`; `flynn-plugin-otel/{cmd/opentelemetry,internal/server,internal/export,internal/store}`; `flynn-plugin-redis/{start.sh,process.go,handler.go,cmd/flynn-redis/main.go}`; `flynn-plugin-mariadb/{handler.go,cmd/flynn-mariadb/main.go,img/packages.sh}`; `flynn-plugin-mongodb/{flynn-plugin.json,cmd/flynn-mongodb/main.go,img/packages.sh}`; `flynn-plugin-kafka/{handler.go,cmd/flynn-kafka-api/main.go,img/packages.sh}`; `flynn-plugin-clickhouse/{handler.go,cmd/flynn-clickhouse-api/main.go,img/packages.sh}`; `img/packages.sh` of dashboard and otel.

### C. Explicitly NOT reviewed

- `flynn/discoverd` raft **library** internals (vendored `hashicorp/raft` 2016 snapshot) — only the store's use of it was read; no simulation of partitions was run.
- `flynn/pkg/sirenia` MariaDB and MongoDB **process** implementations in depth (`flynn-plugin-mariadb/process.go`, `flynn-plugin-mongodb/{process,replset}.go`): only the replication-health change (commit `fa5dd952`) and the admin HTTP surface were read; GTID/oplog-based takeover correctness for those engines is not assessed. The `pkg/sirenia/simulator` was not executed.
- `flynn/logaggregator` secrets-in-logs content audit (what each component actually logs) — only the access model was assessed.
- Host layer-cache eviction and ZFS snapshot GC races (`host/volume/zfs` snapshot/`Pull`/`Send` paths were skimmed, not traced).
- Router route-store persistence internals (`router/store.go` beyond the controller-key bootstrap) and HTTP/2 stream limits.
- The dashboard OAuth authorization-code exchange and the frontend `api/` client beyond a sink search; no browser-based testing.
- Kafka and ClickHouse `process.go`/`admin.go` business logic (topic ACLs, keeper quorum) beyond the HTTP handler surface.
- Any runtime testing — this review is static; no cluster was exercised and no exploit was executed.
- Sibling checkouts and repos listed as out of scope (`flynn-plugin-dashboard-*`, `flynn-datastore-tls`, `flynn-resource-demo`, `flynn-cursor-agent-test`, `flynn-ansible`, `.worktrees/*`, `worktrees/*`, `flynn/.kilo/`).
