#!/usr/bin/env bats

load "helper"
load_lib "release.sh"

# override date so that it's predictable in tests.
DATE="20150301"
date() {
  echo "${DATE}"
}

@test "next_release_version with previous date in tag" {
  run next_release_version "v20150101.0"

  assert_success
  assert_output "v${DATE}.0"
}

@test "next_release_version with today's date in tag" {
  run next_release_version "v${DATE}.0"

  assert_success
  assert_output "v${DATE}.1"
}

@test "next_release_version can handle 2 digit iterations" {
  run next_release_version "v${DATE}.9"

  assert_success
  assert_output "v${DATE}.10"
}

@test "next_release_version_from_tags with no tags is .0" {
  git() {
    if [[ "$1" == "tag" ]]; then
      return 0
    fi
    command git "$@"
  }
  run next_release_version_from_tags
  assert_success
  assert_output "v${DATE}.0"
}

@test "next_release_version_from_tags increments today's highest N" {
  git() {
    if [[ "$1" == "tag" ]]; then
      printf '%s\n' "v${DATE}.0" "v${DATE}.2" "v${DATE}.0-smoke" "v${DATE}.1"
      return 0
    fi
    command git "$@"
  }
  run next_release_version_from_tags
  assert_success
  assert_output "v${DATE}.3"
}

@test "release workflow version is optional and auto-picks the next calver tag" {
  wf="${ROOT}/.github/workflows/release.yml"
  if grep -A8 '^[[:space:]]*version:' "${wf}" | grep -q 'required: true'; then
    echo "Build and Release version must be optional so empty means next vYYYYMMDD.N" >&2
    return 1
  fi
  grep -q 'next_release_version_from_tags' "${wf}"
  grep -q 'needs.build-base.outputs.VERSION' "${wf}"
}

teardown() {
  unset -f git ssh-keygen ssh-keyscan || true
  if [[ -n "${FLYNN_SAVED_HOME+x}" ]]; then
    HOME="${FLYNN_SAVED_HOME}"
    unset FLYNN_SAVED_HOME
  fi
}

begin_tag_fetch_test() {
  if [[ -z "${FLYNN_SAVED_HOME+x}" ]]; then
    FLYNN_SAVED_HOME="${HOME}"
  fi
  FLYNN_FETCH_LOG="${BATS_TEST_TMPDIR}/calls.log"
  : >"${FLYNN_FETCH_LOG}"
  HOME="${BATS_TEST_TMPDIR}/home"
  mkdir -p "${HOME}"
  FLYNN_ORIGIN_URL="git@github.com:randy-girard/flynn.git"
  FLYNN_ORIGIN_FETCH_OK="0"
  FLYNN_ORIGIN_FETCH_ERR="fatal: simulated origin failure"
  FLYNN_HTTPS_FETCH_OK="0"
  FLYNN_GITHUB_HOST_KNOWN="0"
  # shellcheck disable=SC2329
  git() {
    printf 'git %s\n' "$*" >>"${FLYNN_FETCH_LOG}"
    if [[ "$1" == "remote" && "$2" == "get-url" ]]; then
      printf '%s\n' "${FLYNN_ORIGIN_URL}"
      return 0
    fi
    if [[ "$1" == "fetch" && "$2" == "--tags" && "$3" == "--force" && "$4" == "origin" ]]; then
      if [[ "${FLYNN_ORIGIN_FETCH_OK}" == "1" ]]; then
        return 0
      fi
      echo "${FLYNN_ORIGIN_FETCH_ERR}" >&2
      return 1
    fi
    if [[ "$1" == "fetch" ]]; then
      if [[ "${FLYNN_HTTPS_FETCH_OK}" == "1" ]]; then
        return 0
      fi
      echo "fatal: simulated https failure" >&2
      return 1
    fi
    echo "unexpected git $*" >&2
    return 1
  }
  # shellcheck disable=SC2329
  ssh-keygen() {
    printf 'ssh-keygen %s\n' "$*" >>"${FLYNN_FETCH_LOG}"
    if [[ "${FLYNN_GITHUB_HOST_KNOWN}" == "1" ]]; then
      return 0
    fi
    return 1
  }
  # shellcheck disable=SC2329
  ssh-keyscan() {
    printf 'ssh-keyscan %s\n' "$*" >>"${FLYNN_FETCH_LOG}"
    echo "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey"
    return 0
  }
}

assert_log_order() {
  local file="$1"
  local earlier="$2"
  local later="$3"
  local earlier_line later_line
  earlier_line="$(grep -n -F "${earlier}" "${file}" | head -n1 | cut -d: -f1)"
  later_line="$(grep -n -F "${later}" "${file}" | head -n1 | cut -d: -f1)"
  if [[ -z "${earlier_line}" || -z "${later_line}" ]]; then
    echo "missing '${earlier}' or '${later}'" >&2
    cat "${file}" >&2
    return 1
  fi
  if [[ "${earlier_line}" -ge "${later_line}" ]]; then
    echo "expected '${earlier}' before '${later}'" >&2
    cat "${file}" >&2
    return 1
  fi
}

@test "flynn_fetch_release_tags keyscans and falls back to https when github ssh fetch fails" {
  begin_tag_fetch_test
  FLYNN_HTTPS_FETCH_OK="1"
  run flynn_fetch_release_tags
  assert_success
  if [[ "${output}" == *"Warning: Could not fetch tags from remote"* ]]; then
    echo "https fallback succeeded but the warning was printed" >&2
    printf '%s\n' "${output}" >&2
    return 1
  fi
  if ! grep -F -q "ssh-keygen -F github.com" "${FLYNN_FETCH_LOG}"; then
    echo "expected ssh-keygen -F github.com" >&2
    cat "${FLYNN_FETCH_LOG}" >&2
    return 1
  fi
  assert_log_order "${FLYNN_FETCH_LOG}" \
    "ssh-keyscan -t ed25519 github.com" \
    "git fetch --tags --force origin"
  assert_log_order "${FLYNN_FETCH_LOG}" \
    "git fetch --tags --force origin" \
    "git fetch --tags --force https://github.com/randy-girard/flynn.git +refs/tags/*:refs/tags/*"
}

@test "flynn_fetch_release_tags does not fetch https when origin fetch succeeds" {
  begin_tag_fetch_test
  FLYNN_ORIGIN_FETCH_OK="1"
  FLYNN_GITHUB_HOST_KNOWN="1"
  run flynn_fetch_release_tags
  assert_success
  if ! grep -F -q "ssh-keygen -F github.com" "${FLYNN_FETCH_LOG}"; then
    echo "expected ssh-keygen -F github.com before deciding not to keyscan" >&2
    cat "${FLYNN_FETCH_LOG}" >&2
    return 1
  fi
  if ! grep -F -q "git fetch --tags --force origin" "${FLYNN_FETCH_LOG}"; then
    echo "origin was not fetched" >&2
    cat "${FLYNN_FETCH_LOG}" >&2
    return 1
  fi
  if grep -F "https://" "${FLYNN_FETCH_LOG}"; then
    echo "https was fetched after a successful origin fetch" >&2
    return 1
  fi
  if grep -F "ssh-keyscan" "${FLYNN_FETCH_LOG}"; then
    echo "ssh-keyscan ran even though github.com was already known" >&2
    return 1
  fi
}

@test "flynn_fetch_release_tags does not invent an https fallback for non-github remotes" {
  begin_tag_fetch_test
  FLYNN_ORIGIN_URL="git@gitlab.com:acme/widgets.git"
  run flynn_fetch_release_tags
  assert_failure
  [[ "${output}" == *"Warning: Could not fetch tags from remote"* ]]
  [[ "${output}" == *"fatal: simulated origin failure"* ]]
  if grep -F "https://" "${FLYNN_FETCH_LOG}"; then
    echo "non-github origin invented an https fallback" >&2
    return 1
  fi
  if grep -F "ssh-keyscan" "${FLYNN_FETCH_LOG}"; then
    echo "non-github origin keyscanned github.com" >&2
    return 1
  fi
}

@test "flynn_fetch_release_tags maps ssh:// github URLs to https" {
  begin_tag_fetch_test
  FLYNN_ORIGIN_URL="ssh://git@github.com/randy-girard/flynn.git"
  FLYNN_HTTPS_FETCH_OK="1"
  run flynn_fetch_release_tags
  assert_success
  assert_log_order "${FLYNN_FETCH_LOG}" \
    "ssh-keyscan -t ed25519 github.com" \
    "git fetch --tags --force https://github.com/randy-girard/flynn.git +refs/tags/*:refs/tags/*"
}

@test "build.sh fetches tags with flynn_fetch_release_tags" {
  local build="${ROOT}/build.sh"
  if ! grep -q 'flynn_fetch_release_tags' "${build}"; then
    echo "build.sh does not call flynn_fetch_release_tags" >&2
    return 1
  fi
  if grep -q 'git fetch --tags --force 2>/dev/null' "${build}"; then
    echo "build.sh still discards git fetch stderr" >&2
    return 1
  fi
}
