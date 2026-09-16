#!/bin/bash
# Regression: flynn-host update re-execs the new binary after installing
# flynn-host. That binary's compiled version matches the target tag, so the
# updater must continue (init, CLI, daemon restart, images) without --force.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
updater="${ROOT}/host/cli/github_updater.go"
updater_test="${ROOT}/host/cli/github_updater_test.go"
usage="${ROOT}/host/cli/update.go"

need_file() {
  local path=$1 msg=$2
  if [[ ! -f "${path}" ]]; then
    echo "${msg}" >&2
    echo "  missing ${path}" >&2
    exit 1
  fi
}

need_in() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE -- "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need_file "${updater}" "GitHub updater must exist"
need_file "${updater_test}" "GitHub updater tests must exist"
need_file "${usage}" "flynn-host update usage must exist"

need_in "${updater}" 'continueAfterHostReexec' \
  "update must detect FLYNN_UPDATE_REEXEC after installing flynn-host"
need_in "${updater}" 'shouldAbortGitHubUpdate' \
  "already-on-latest abort must be a named gate so re-exec can continue"
need_in "${updater}" 'continuing update after flynn-host re-exec' \
  "re-exec continuation must be logged"
need_in "${updater_test}" 'TestShouldAbortGitHubUpdate' \
  "unit tests must cover the already-on-latest gate"
need_in "${updater_test}" 'TestContinueAfterHostReexec' \
  "unit tests must cover FLYNN_UPDATE_REEXEC"
need_in "${updater_test}" 're-exec mid-update must continue without --force' \
  "a version bump must finish without --force after re-exec"
need_in "${usage}" 'does not need --force' \
  "flynn-host update usage must say a version bump does not need --force"

echo "ok flynn-host update continues after re-exec without --force"
