# Flynn platform review — security, reliability, stability, performance

**Date:** 2026-09-19
**Reviewed revision:** `flynn` `main` @ `5c6be2a8` ("docs: keep dashboard mock stats aligned with Flynn host types") and each `flynn-plugin-*` repo at its `main` HEAD on the same day.
**Type:** point-in-time, read-only code review. **No code was changed.** The only files produced are this report and one link line in `docs/README.md`, on branch `docs/platform-review-2026-09`.

## 1. Scope and method

### What was read

- `flynn/` core: `controller/` (HTTP + gRPC authz, `authorizer/`, `authz/`, `tokensigner/`, releases, jobs, routes, stats, GitHub integration, scheduler), `host/` (`http.go` auth, `libcontainer_backend.go`, `userns.go`, `netpolicy.go`, `downloader/`, `cli/plugin*.go`), `router/` (`http.go`, `tcp.go`, proxy), `discoverd/server/` (handler, DNS), `blobstore/`, `gitreceive/` (server + `receiver/flynn-receive.go`), `slugbuilder/` and `dockerbuilder/` build scripts, `taffy/`, `bootstrap/` (manifest, secret generation, host-auth action, discovery client), `pkg/netpolicy/`, `pkg/iptables/`, `pkg/plugin/` (install, GitHub download, hooks, `official-plugins.json`), `pkg/cluster/`, `pkg/httphelper/`, `pkg/backup/`, `cli/config/`, `cli/cluster.go`, `script/install-flynn*`, `script/release`, `schema/controller/new_job.json`, `docs/content/security.md`, `docs/content/production.html.md`, `go.mod`.
- Plugins: `flynn-plugin.json` for all eleven plugins; `flynn-plugin-dashboard/internal/{auth,tokens,store,runjobws}`; `flynn-plugin-discovery/internal/{server,store}` + `script/ready.sh`; `flynn-plugin-scheduler/{cmd/scheduler,internal/{server,runner}}`; `flynn-plugin-otel/{cmd,internal/{server,export,store}}`; `flynn-plugin-redis` (`start.sh`, `process.go`, `handler.go`); `flynn-plugin-mongodb/flynn-plugin.json` backup args; manifests of mariadb/kafka/clickhouse/www/template.

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
| Critical | 4 |
| High | 7 |
| Medium | 12 |
| Low | 12 |
| Info | 3 |
| **Total** | **38** |

### By area

| Area | Critical | High | Medium | Low | Info | Total |
|---|---|---|---|---|---|---|
| Security | 4 | 7 | 9 | 7 | 1 | 28 |
| Reliability | 0 | 0 | 1 | 4 | 0 | 5 |
| Stability | 0 | 0 | 1 | 1 | 0 | 2 |
| Performance | 0 | 0 | 1 | 0 | 0 | 1 |
| Hygiene (Info) | 0 | 0 | 0 | 0 | 2 | 2 |

Critical: SEC-001, SEC-002, SEC-003, SEC-004. High: SEC-005, SEC-006, SEC-007, SEC-008, SEC-009, SEC-010, SEC-011. Medium: SEC-012–SEC-020, REL-001, STAB-001, PERF-001. Low: SEC-021–SEC-027, REL-002, REL-003, REL-005, REL-006, STAB-002. Info: INFO-001–INFO-003.

The overarching theme: the fork added a real privilege boundary (app-scoped tokens, fine-grained grants, user namespaces, netpolicy) on top of an upstream architecture that assumed every authenticated caller and every internal service was fully trusted. Most Critical/High items are places where that old assumption still leaks through the new boundary.

## 3. Top 10 to work on next

1. **SEC-001** — `RunJob` passes caller-supplied `meta`/`partition`/`profiles` to the host; a `jobs:run` grant on any app yields a system-class job (host auth key in env, `zfs`/`kvm` profiles, no userns, system netpolicy) → host root.
2. **SEC-002** — Dashboard mints a token with `nil` scopes + `nil` grants for a non-admin user with zero collaborator rows; the controller treats that as cluster admin.
3. **SEC-003** — discoverd HTTP API (`:1111`) is unauthenticated (`DISCOVERD_AUTH_KEY` never set) and netpolicy explicitly allows user and build containers to reach it: service hijack, `POST /shutdown`, raft peer edits.
4. **SEC-004** — Blobstore has no authentication and is reachable from build jobs; the "signed build cache URL" is never verified server-side. Cross-app slug/cache poisoning and cluster-wide image deletion.
5. **SEC-005** — gitreceive accepts *any* validly-signed token to push to *any* app by name (no grant check).
6. **SEC-007** — App-scoped `routes:write` can point a route at any discoverd service (`controller`, `blobstore`, `postgres-api`, …), exposing internal services publicly.
7. **SEC-008** — Discovery plugin publishes the cluster join token at unauthenticated `GET /.well-known/cluster` on a public route and accepts unauthenticated instance registration; bootstrap then sends the host auth key (HTTP basic) to attacker-registered URLs.
8. **SEC-006 / SEC-011** — Build jobs run root-in-container with `CAP_SYS_ADMIN`, seccomp unconfined, no AppArmor, no userns; `CONTROLLER_KEY` still enters the build job environment. Any `git push` is one libcontainer/runc weakness away from host root.
9. **SEC-009 / SEC-017** — Vendored `runc/libcontainer v1.0.0-rc8` (2019) plus `x/crypto v0.46`, `x/net v0.48`, `grpc v1.56.3`, `yaml.v2 v2.2.2` with reachable known vulnerabilities (govulncheck: 21 reachable in core).
10. **SEC-012** — `flynn-host plugin:install` downloads unsigned hook scripts from a GitHub release and runs them as root with `CONTROLLER_KEY` and `ACCESS_TOKEN_SIGNING_KEY` in env; `--github-org` lets any org be the source.

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

#### SEC-014 — `GetJob`/`PutJob` do not bind the job to the app in the URL

- **Severity:** Medium · **Area:** Security · **Component:** `flynn/controller` · **Origin:** fork-introduced (boundary) · **Effort:** S
- **Refs:** `flynn/controller/jobs.go:49-95`.
- **Description.** `GetJob` looks the job up by ID only; `PutJob` upserts the decoded `ct.Job` (including `AppID`) via `jobRepo.Add` without checking `job.AppID == app.ID`. An app-scoped token with `jobs:read`/`jobs:run` can read another app's job record (by UUID) or write job state rows for another app, which the controller scheduler consumes for sync. **Needs confirmation** that the scheduler acts on foreign-written rows rather than reconciling from hosts.
- **Fix.** After lookup/decode, return 404 unless `job.AppID == c.getApp(ctx).ID`.

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
- **Reproduction path.** Admin invites a user (collaborator row created), later removes them from the app (row deleted) or creates a user via the users API with `cluster_admin=false` and no apps yet. The user logs in and calls `/api/token` (or the OAuth code exchange) → token → any controller call succeeds as admin, including `RunJob` with `partition: system` (SEC-001).
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
- **Description.** The `pids` controller is enabled but no `PidsLimit` is applied unless the app sets `max_procs`; `RLIMIT_NPROC` is per real uid and shared across jobs mapped to the same uid range. A fork bomb in one user job can exhaust the host PID space. **Needs confirmation** whether `resource.SetDefaults` fills `max_procs`.
- **Fix.** Apply `Cgroups.Resources.PidsLimit` from a default (e.g. 4096) for every job.

#### REL-005 — AppArmor apply failure is fail-open for system and build jobs

- **Severity:** Low · **Area:** Reliability/Security · **Component:** `flynn/host` · **Origin:** fork-introduced · **Effort:** S
- **Refs:** `flynn/host/libcontainer_backend.go:1147-1166`.
- **Description.** On an "apparmor" error the job is retried without a profile. User jobs correctly fail closed (`:1155-1158`); system/build jobs silently lose the LSM. Build jobs already run without AppArmor (SEC-006), so exposure is limited to system jobs. Log at error level exists; there is no metric/event.
- **Fix.** Emit a host event/metric when falling back; consider failing closed for datastore-class jobs.

### 4.5 discoverd, network policy, router

#### SEC-003 — discoverd HTTP API is unauthenticated and explicitly reachable from user and build containers

- **Severity:** Critical · **Area:** Security · **Component:** `flynn/discoverd`, `flynn/pkg/iptables`, `flynn/host/netpolicy.go` · **Origin:** inherited (upstream discoverd never had auth); the dead `DISCOVERD_AUTH_KEY` (code tag SEC-009) and the netpolicy ACCEPT rule are fork-introduced · **Effort:** M
- **Refs:** `flynn/discoverd/server/handler.go:50` (`h.AuthKey = os.Getenv("DISCOVERD_AUTH_KEY")`), `:131-140` (check only if non-empty), `:58-81` (routes incl. `PUT /services/:service/instances/:id`, `PUT /services/:service/leader`, `PUT|DELETE /raft/peers/:peer`, `POST /shutdown`), `:276-301` (`servePutInstance`: no origin/address validation), `flynn/pkg/iptables/iptables.go:179-188` (`UserToHostDiscoverdArgs`/`BuildToHostDiscoverdArgs` ACCEPT to bridge `:1111`), `flynn/discoverd/server/dns.go:222` (DNS *is* filtered for user class).
- **Description.** `rg DISCOVERD_AUTH_KEY` finds a single reader and no writer anywhere in `flynn/` (bootstrap manifest, plugins, CLI), so the key is always empty and every discoverd endpoint is open. The fork's DNS layer filters what a user job may *resolve*, but the HTTP API on the bridge address is whitelisted for both user and build classes and has no equivalent filter.
- **Impact (from any user container).** `POST /shutdown` on each host's discoverd → cluster-wide outage. `PUT /services/controller/instances/<id>` with the attacker's overlay IP → system components resolving `controller.discoverd` (gitreceive, router, dashboard, scheduler/otel plugins) may connect to the rogue instance and present `CONTROLLER_KEY` as HTTP basic auth over plaintext → cluster admin. Same for `blobstore` (serve malicious slugs to slugrunner) and `postgres` leader. **Needs confirmation** on the exact client selection behaviour (round-robin vs leader-only) for the MITM step; the DoS and metadata/leader tampering are certain.
- **Fix.** Generate `DISCOVERD_AUTH_KEY` in bootstrap and pass it to discoverd and every system client (`pkg/discoverd` client reads it from env); drop the user/build `:1111` ACCEPT rules or replace them with a read-only proxy exposing just what apps legitimately need (register self, get instances of allowed services); validate that a registered instance's address equals the request's source IP for non-system callers.

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
  - `google.golang.org/grpc v1.56.3`: GO-2026-6443 (server panic on missing `:authority`), GO-2026-6348 (HTTP/2 DATA fragmentation OOM), GO-2026-4762 (authz bypass on `:path` without leading slash — the controller has its own interceptors, **needs confirmation** that they do not key on path), via `controller/controller.go:134` `grpc.Server.Serve` and `discoverd/server/handler.go:168` (h2 transport).
  - `golang.org/x/crypto/ssh v0.46.0`: seven GO-2026-* DoS/deadlock issues. govulncheck's example traces go through `test/cluster/instance.go` (test tooling); `gitreceive` also runs an SSH server on x/crypto — **needs confirmation** whether the affected functions are on its path.
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

### 4.12 Hygiene

#### INFO-001 — `flynn/dashboard/` and `flynn/rails-dashboard/` are untracked leftovers

`git ls-files dashboard rails-dashboard` returns 0 files; upstream commit `f48c672b all: Remove dashboard` deleted the old dashboard. The directories present in the checkout are local, ignored artefacts and are **dead relative to the repository**; the live dashboard is `flynn-plugin-dashboard`. Safe to delete locally.

#### INFO-002 — Cluster backups contain all secrets in plaintext

`pkg/backup` serialises the controller DB (env vars, `CONTROLLER_KEY`, signing keys). Expected for a restore artefact; the docs should say "encrypt at rest" explicitly.

## 5. Positive observations

- **Fine-grained authz model** (`controller/authz`): explicit permission constants, role expansion, `IsPlatformAppName`/`SystemAppAllowed` blocks non-admins from touching system apps, and `stripPrivilegedProcessTypes`/`sanitizeAppMetaUpdate` prevent users from defining `slugbuilder`/`dockerbuilder` process types or setting `flynn-system-app` on existing apps.
- **GitHub webhook** verifies HMAC before processing; GitHub App private keys stay controller-side; taffy clones only from the admin-configured GitHub API host (no SSRF found).
- **User jobs** get a per-job user namespace (`host/userns.go`), capability drop, `no_new_privs`, AppArmor `flynn-default` (fail-closed on error), a seccomp filter, and DNS-level scoping of discoverd names (`UserMayResolveDiscoverd`).
- **Network policy** (`pkg/iptables`) with ipsets mirrored from discoverd is a sound design; user→overlay NEW is dropped, user→node and user→host are dropped except DNS.
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
| GO-2026-6355, -6354, -5020, -5019, -5018, -5017, -5013 | x/crypto v0.46.0 → 0.52–0.56 | `test/cluster/instance.go:265` (`ssh.Dial`); gitreceive ssh server needs confirmation |
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

Plugins: all `flynn-plugin.json`; `flynn-plugin-dashboard/internal/{auth,tokens,store,runjobws}`; `flynn-plugin-discovery/{internal/server,internal/store,script/ready.sh,README.md}`; `flynn-plugin-scheduler/{cmd/scheduler,internal/server,internal/runner,internal/cli}`; `flynn-plugin-otel/{cmd/opentelemetry,internal/server,internal/export,internal/store}`; `flynn-plugin-redis/{start.sh,process.go,handler.go}`; `flynn-plugin-mongodb/flynn-plugin.json`.

### C. Explicitly NOT reviewed

- `flynn/pkg/sirenia` state machines and the MariaDB/MongoDB/Postgres appliance failover logic (only manifests and TLS flags were checked).
- `flynn/discoverd` raft implementation internals (leader election correctness, snapshotting).
- `flynn/logaggregator` internals beyond confirming it is cluster-key protected; secrets-in-logs was not audited.
- `flynn/flannel` beyond the glog trace; overlay encryption/authentication was not assessed.
- `flynn/updater`, `flynn/status`, `flynn/appliance/*` Go code, `flynn/host/volume` (ZFS) and layer-cache eviction.
- `flynn/router` route store persistence, ACME rate-limit handling and certificate storage.
- Dashboard **frontend** (`web/`), CSRF on state-changing dashboard endpoints (cookies are `SameSite=Lax`, which mitigates but does not eliminate), and the dashboard OAuth code exchange in depth.
- Kafka, ClickHouse, MariaDB plugin Go code (`cmd/`, `internal/`) and `img/packages.sh` package pinning for every plugin.
- `apt_cache/` base-image pinning.
- Any runtime testing — this review is static; no cluster was exercised and no exploit was executed.
- Sibling checkouts and repos listed as out of scope (`flynn-plugin-dashboard-*`, `flynn-datastore-tls`, `flynn-resource-demo`, `flynn-cursor-agent-test`, `flynn-ansible`, `.worktrees/*`, `worktrees/*`, `flynn/.kilo/`).
