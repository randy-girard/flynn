#!/bin/bash
# Matrix runs leave SMOKE_TOPOLOGIES unset until the first item is applied.
# A failure before that (builder unit tests) prints the summary from
# print_results_table. bash 3.2 set -u aborts inside `{ ...; } >file || true`,
# so fail_shutdown never reaches halt/exit and the builder stays up.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
smoke="${ROOT}/script/vagrant/suite.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
: > "${tmp}/unit"
: > "${tmp}/checks"

{
  echo 'set -euo pipefail'
  sed -n '/^print_results_table()/,/^}/p' "${smoke}"
  cat <<'EOF'
ui_banner() { :; }
smoke_col_cap() { echo "$1"; }
smoke_detail_width() { echo 40; }
ui_table_cell() { printf '%s' "$1"; }
smoke_table_rule() { :; }
ui_status_text() { printf '%s' "$1"; }
print_unit_report() { :; }
print_datastore_report() { :; }
RESULT_NAMES=("Builder unit tests (pre-cluster)")
RESULT_STATUS=(FAIL)
RESULT_SECONDS=(1)
RESULT_DETAIL=(failed)
STARTED_AT=$(date +%s)
BUILD_VERSION=test
APP_NAME=upgrade-smoke
BUILDPACK_APP_NAME=upgrade-smoke-buildpack
DOCKER_APP_NAME=upgrade-smoke-docker
DOCKER_PUSH_APP_NAME=upgrade-smoke-docker-push
CLUSTER_DOMAIN=upgrade-smoke.localflynn.com
BUILT_TARBALL=
OVERALL_FAILED=1
UNIT_CHECK_FILE=${PROBE_UNIT}
CHECK_FILE=${PROBE_CHECKS}
unset SMOKE_TOPOLOGIES SMOKE_SEED_ROWS SMOKE_BLOB_COUNT UPGRADE_PASSES
print_results_table
exit 1
EOF
} > "${tmp}/repro.sh"

set +e
ROOT="${tmp}" PROBE_UNIT="${tmp}/unit" PROBE_CHECKS="${tmp}/checks" \
  /bin/bash "${tmp}/repro.sh" >"${tmp}/out" 2>"${tmp}/err"
rc=$?
set -e
if [[ "${rc}" -ne 1 ]]; then
  echo "print_results_table aborted fail_shutdown (exit ${rc})" >&2
  cat "${tmp}/err" >&2
  exit 1
fi
if ! grep -q '^Wrote ' "${tmp}/out"; then
  echo "print_results_table stopped before the failure path could finish" >&2
  cat "${tmp}/out" >&2
  exit 1
fi
echo "ok smoke failure summary survives unset matrix variables"
