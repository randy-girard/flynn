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
