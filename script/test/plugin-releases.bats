#!/usr/bin/env bats

load "helper"
load_lib "plugin-releases.sh"

setup() {
  TMP="$(mktemp -d "${BATS_TMPDIR}/plugin-releases.XXXXXX")"
  STUB="${TMP}/gh"
  LOG="${TMP}/gh.log"
  cat >"${STUB}" <<'EOF'
#!/bin/bash
echo "$*" >> "${GH_LOG}"
if [[ "$1" == "release" && "$2" == "view" ]]; then
  version=$3
  repo=""
  while [[ $# -gt 0 ]]; do
    if [[ "$1" == "--repo" ]]; then
      repo=$2
      break
    fi
    shift
  done
  if [[ -n "${GH_EXISTING:-}" && -f "${GH_EXISTING}/${repo}/${version}" ]]; then
    exit 0
  fi
  exit 1
fi
exit 0
EOF
  chmod +x "${STUB}"
  export GH="${STUB}"
  export GH_LOG="${LOG}"
  : >"${LOG}"
}

teardown() {
  rm -rf "${TMP}"
}

@test "plugin_release_repos_from_text accepts URLs commas and comments" {
  run plugin_release_repos_from_text "$(cat <<'EOF'
# first-party plugins for this fork
https://github.com/acme/flynn-plugin-cache.git
acme/flynn-plugin-queue, acme/flynn-plugin-cache
EOF
)"
  assert_success
  assert_output $'acme/flynn-plugin-cache\nacme/flynn-plugin-queue'
}

@test "plugin_release_repos_from_text rejects path-like names" {
  run plugin_release_repos_from_text "acme/../etc"
  assert_failure
}

@test "plugin_dispatch_releases dry-run does not invoke gh" {
  run plugin_dispatch_releases "v20260915.0" "acme/flynn-plugin-cache" "false" "true"
  assert_success
  [[ ! -s "${LOG}" ]]
}

@test "plugin_dispatch_releases queues release.yml with Flynn version pin" {
  run plugin_dispatch_releases "v20260915.0" "acme/flynn-plugin-cache" "true" "false"
  assert_success
  grep -q 'workflow run release.yml --repo acme/flynn-plugin-cache' "${LOG}"
  grep -q 'version=v20260915.0' "${LOG}"
  grep -q 'flynn_version=v20260915.0' "${LOG}"
  grep -q 'prerelease=true' "${LOG}"
}

@test "plugin_dispatch_releases skips tags that already exist" {
  mkdir -p "${TMP}/existing/acme/flynn-plugin-cache"
  touch "${TMP}/existing/acme/flynn-plugin-cache/v20260915.0"
  export GH_EXISTING="${TMP}/existing"
  run plugin_dispatch_releases "v20260915.0" "acme/flynn-plugin-cache" "false" "false"
  assert_success
  if grep -q 'workflow run' "${LOG}"; then
    echo "existing release must not be dispatched again" >&2
    cat "${LOG}" >&2
    return 1
  fi
}

@test "dispatch-plugin-releases skips when PLUGIN_RELEASE_REPOS is empty" {
  run env PLUGIN_RELEASE_REPOS= PLUGIN_RELEASE_TOKEN= GH_TOKEN= \
    "${ROOT}/script/dispatch-plugin-releases" --version v20260915.0 --dry-run
  assert_success
}

@test "dispatch-plugin-releases requires a token when repos are set" {
  run env PLUGIN_RELEASE_REPOS="acme/flynn-plugin-cache" PLUGIN_RELEASE_TOKEN= GH_TOKEN= \
    "${ROOT}/script/dispatch-plugin-releases" --version v20260915.0
  assert_failure
}

@test "plugin release workflow is published-only and has no hardcoded plugins" {
  wf="${ROOT}/.github/workflows/plugin-releases.yml"
  grep -q 'types: \[published\]' "${wf}"
  grep -q 'vars.PLUGIN_RELEASE_REPOS' "${wf}"
  grep -q 'secrets.PLUGIN_RELEASE_TOKEN' "${wf}"
  grep -q 'dispatch-plugin-releases' "${wf}"
  if grep -E 'flynn-plugin-(redis|mariadb|mongodb|kafka|clickhouse|template)' "${wf}" \
    "${ROOT}/script/dispatch-plugin-releases" \
    "${ROOT}/script/lib/plugin-releases.sh"; then
    echo "Flynn must not hardcode plugin appliance repos" >&2
    return 1
  fi
}

@test "Build and Release can dispatch plugin-releases.yml" {
  wf="${ROOT}/.github/workflows/release.yml"
  grep -q 'dispatch_plugins:' "${wf}"
  grep -q 'inputs.dispatch_plugins' "${wf}"
  grep -q 'workflow run plugin-releases.yml' "${wf}"
  grep -q 'actions: write' "${wf}"
  grep -q 'version=${VERSION}' "${wf}"
  grep -q 'prerelease=${PRERELEASE}' "${wf}"
  if grep -E 'flynn-plugin-(redis|mariadb|mongodb|kafka|clickhouse|dashboard|template)' "${wf}"; then
    echo "Build and Release must not hardcode plugin appliance repos" >&2
    return 1
  fi
}
