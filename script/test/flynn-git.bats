#!/usr/bin/env bats

load "helper"

SHIM="${BATS_TEST_DIRNAME}/../../builder/img/flynn-git.sh"

# Stand up a real local bare repo to exercise the mirror cache. Using real git
# keeps the test honest about the shim's argv handling and clone replay.
setup() {
  TMP="$(mktemp --directory)"
  export FLYNN_GIT_CACHE_ROOT="${TMP}/cache"
  mkdir -p "${FLYNN_GIT_CACHE_ROOT}"

  # Source repo with one commit on main. Use init.defaultBranch=main on both ends so
  # clone --mirror picks up HEAD -> refs/heads/main and downstream clones can check it out.
  SRC="${TMP}/src"
  git -c init.defaultBranch=main init -q --bare "${SRC}.git"
  WORK="${TMP}/work"
  git -c init.defaultBranch=main init -q "${WORK}"
  ( cd "${WORK}" \
    && git config user.email "t@example.com" \
    && git config user.name  "Test" \
    && echo v1 > file.txt \
    && git add file.txt \
    && git commit -q -m "v1" \
    && git remote add origin "${SRC}.git" \
    && git push -q -u origin main )
  # Ensure HEAD on the bare repo points at main (older git defaults to master).
  git -C "${SRC}.git" symbolic-ref HEAD refs/heads/main
  URL="file://${SRC}.git"

  # Wrap real git so we can count network-touching invocations
  # (clone --mirror and remote update). Local clones from the mirror still
  # invoke real git but are not what we are counting here.
  REAL_GIT_BIN="$(command -v git)"
  export LOG="${TMP}/log"
  : >"${LOG}"
  cat >"${TMP}/git" <<EOF
#!/bin/bash
case " \$* " in
  *" clone --mirror "*|*" remote update "*) echo "net" >> "${LOG}" ;;
esac
exec "${REAL_GIT_BIN}" "\$@"
EOF
  chmod +x "${TMP}/git"
  export REAL_GIT="${TMP}/git"
}

teardown() {
  rm -rf "${TMP}"
  unset FLYNN_GIT_CACHE_ROOT REAL_GIT LOG FLYNN_NO_GIT_CACHE FLYNN_GIT_CACHE_TTL
}

net_count() {
  wc -l <"${LOG}" | tr -d ' '
}

@test "clone populates mirror and reuses it on second call" {
  bash "${SHIM}" clone "${URL}" "${TMP}/c1"
  bash "${SHIM}" clone "${URL}" "${TMP}/c2"
  # First call creates the mirror (1 net hit). Second call is within TTL
  # and is served from the local mirror without a network update.
  [[ "$(net_count)" == "1" ]]
  [[ -f "${TMP}/c1/file.txt" ]]
  [[ -f "${TMP}/c2/file.txt" ]]
}

@test "TTL=0 forces a mirror refresh on every cached clone" {
  export FLYNN_GIT_CACHE_TTL=0
  bash "${SHIM}" clone "${URL}" "${TMP}/d1"
  bash "${SHIM}" clone "${URL}" "${TMP}/d2"
  # mirror create (1) + one remote update on the second call.
  [[ "$(net_count)" == "2" ]]
}

@test "FLYNN_NO_GIT_CACHE=1 bypasses cache entirely" {
  export FLYNN_NO_GIT_CACHE=1
  bash "${SHIM}" clone "${URL}" "${TMP}/e1"
  bash "${SHIM}" clone "${URL}" "${TMP}/e2"
  # Neither call goes through the mirror path, so no "clone --mirror" or
  # "remote update" hits the log.
  [[ "$(net_count)" == "0" ]]
}

@test "without FLYNN_GIT_CACHE_ROOT the shim is a passthrough" {
  unset FLYNN_GIT_CACHE_ROOT
  bash "${SHIM}" clone "${URL}" "${TMP}/f1"
  bash "${SHIM}" clone "${URL}" "${TMP}/f2"
  [[ "$(net_count)" == "0" ]]
  [[ -f "${TMP}/f1/file.txt" ]]
}

@test "non-clone subcommands pass through" {
  bash "${SHIM}" clone "${URL}" "${TMP}/g1"
  ( cd "${TMP}/g1" && bash "${SHIM}" log --oneline ) | grep -q v1
  ( cd "${TMP}/g1" && bash "${SHIM}" status ) >/dev/null
}

@test "unsupported clone flag forces passthrough" {
  # --separate-git-dir is not in the safe list; should bail to real git
  # without populating the mirror.
  bash "${SHIM}" clone --separate-git-dir "${TMP}/h-gitdir" "${URL}" "${TMP}/h1"
  [[ "$(net_count)" == "0" ]]
  [[ -f "${TMP}/h1/file.txt" ]]
  [[ -d "${TMP}/h-gitdir" ]]
}

@test "safe clone flags (--depth, --branch) are replayed against the mirror" {
  bash "${SHIM}" clone --depth 1 --branch main "${URL}" "${TMP}/i1"
  bash "${SHIM}" clone --depth 1 --branch main "${URL}" "${TMP}/i2"
  [[ "$(net_count)" == "1" ]]
  [[ -f "${TMP}/i1/file.txt" ]]
  [[ -f "${TMP}/i2/file.txt" ]]
}

@test "local-path URLs (no scheme) pass through" {
  # Bare filesystem paths are not in the cacheable scheme list; the shim should
  # delegate to real git without populating the mirror.
  bash "${SHIM}" clone "${SRC}.git" "${TMP}/j1"
  [[ "$(net_count)" == "0" ]]
  [[ -f "${TMP}/j1/file.txt" ]]
}

@test "TTL=very-large keeps subsequent clones cached" {
  export FLYNN_GIT_CACHE_TTL=86400
  bash "${SHIM}" clone "${URL}" "${TMP}/k1"
  bash "${SHIM}" clone "${URL}" "${TMP}/k2"
  bash "${SHIM}" clone "${URL}" "${TMP}/k3"
  [[ "$(net_count)" == "1" ]]
}
