#!/bin/bash
# Regression: smoke must seed every datastore, deploy test/apps/upgrade-smoke,
# run two --force upgrades, and print a per-engine persistence report.
# Sirenia/redis volume bugs have shown up on the second pass after the first
# succeeded (seen 2026-09-09 mariadb hang / redis empty GET).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
app="${ROOT}/test/apps/upgrade-smoke"

need() {
  local needle=$1 msg=$2
  if ! grep -qE "${needle}" "${smoke}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${smoke}" >&2
    exit 1
  fi
}

need_file() {
  local path=$1 msg=$2
  if [[ ! -f "${path}" ]]; then
    echo "${msg}" >&2
    echo "  missing ${path}" >&2
    exit 1
  fi
}

need_file "${app}/main.go" "smoke app must live in test/apps/upgrade-smoke"
need_file "${app}/go.mod" "upgrade-smoke must be a Go module (git-push buildpack)"
need_file "${app}/Procfile" "upgrade-smoke must have a Procfile"
need_file "${app}/data/seed.txt" "upgrade-smoke must embed at least one data file"
grep -q 'go:embed data' "${app}/main.go" || { echo "upgrade-smoke must embed data/ blobs" >&2; exit 1; }
grep -q '/status' "${app}/main.go" || { echo "upgrade-smoke must serve GET /status" >&2; exit 1; }
need_file "${ROOT}/cli/clickhouse_test.go" "clickhouse CLI stdin hang must have unit tests"
grep -q 'TestApplyClickhouseStdinPolicy' "${ROOT}/cli/clickhouse_test.go" \
  || { echo "cli/clickhouse_test.go must test applyClickhouseStdinPolicy (INSERT VALUES TTY hang)" >&2; exit 1; }
grep -q 'tty INSERT VALUES' "${ROOT}/cli/clickhouse_test.go" \
  || { echo "cli/clickhouse_test.go must cover TTY INSERT VALUES stdin close" >&2; exit 1; }
grep -q 'pipe INSERT VALUES' "${ROOT}/cli/clickhouse_test.go" \
  || { echo "cli/clickhouse_test.go must keep piped stdin for INSERT FORMAT CSV" >&2; exit 1; }

need 'test/apps/upgrade-smoke' \
  "smoke must deploy test/apps/upgrade-smoke (not mutate test/apps/http)"
need 'postgres mysql mongodb redis kafka clickhouse' \
  "smoke must provision every datastore provider"
need 'DATASTORE_PROVIDERS' \
  "smoke must iterate a shared provider list"
need 'SMOKE_SEED_ROWS' \
  "smoke must seed a configurable number of dummy rows/keys"
need 'SMOKE_BLOB_COUNT' \
  "smoke must embed a configurable number of slug blobs"
need 'generate_series' \
  "smoke must bulk-insert postgres dummy rows"
need 'pg_available_extensions' \
  "smoke must verify postgis/pgrouting/timescaledb survived image slimming"
need 'smoke_payload' \
  "smoke must seed a 1KB payload table (postgres/mysql) so restarts copy real data"
need 'payload TEXT' \
  "mysql payload column must not be named blob (BLOB is reserved in MariaDB)"
need 'dummy:' \
  "smoke must SET redis dummy keys (not only smoke_probe)"
need 'kafka topics create smoke_probe' \
  "smoke must create a kafka topic whose metadata lives on /data"
need 'CREATE DATABASE IF NOT EXISTS smoke_db ENGINE = Atomic' \
  "clickhouse seed must create the DB without ON CLUSTER (DistributedDDL may be absent)"
need 'CHECK_FILE' \
  "per-engine checks must be written to a file (run_step is a subshell)"
need 'smoke_db.rows' \
  "smoke must insert clickhouse dummy rows"
need 'UPGRADE_PASSES' \
  "smoke must run more than one --force tarball update"
need 'post-upgrade-2' \
  "smoke must assert pass-1 markers still exist after the second upgrade"
need 'sirenia_primary_read_write' \
  "smoke must wait for postgres/mariadb/mongodb after each upgrade pass"
need 'wait_datastores_ready "after bootstrap" postgres' \
  "bootstrap must only wait for postgres (mariadb/mongodb stay scaled to 0 until resource add)"
need 'wait_datastores_ready "after resource add" postgres mariadb mongodb redis' \
  "after provisioning, smoke must wait for every scaled sirenia appliance plus redis"
need 'wait_datastores_ready "after upgrade' \
  "after each --force update, smoke must wait for postgres/mariadb/mongodb/redis"
need 'record_check' \
  "smoke must record per-engine results for the final report"
need 'print_datastore_report' \
  "smoke must print an App & datastore persistence table"
need 'assert_app_status' \
  "smoke must verify /status resource env after each upgrade"
need 'probe_app_http' \
  "HTTP wait retries must not record FAIL/PASS on every attempt"
need '</dev/null' \
  "flynn1 must close stdin so clickhouse-client INSERT cannot hang on a TTY"
need 'INSERT INTO smoke_db.rows SELECT' \
  "clickhouse marker rows must use INSERT SELECT (INSERT VALUES waits on stdin)"
need 'RESUME_AT=upgrade' \
  "smoke must be able to resume at the --force update after a hung pre-upgrade verify"
need 'db-check' \
  "assert_databases must log per-engine progress so a hang is obvious"
need 'step_host_unit_tests' \
  "smoke must run host unit tests before Vagrant up"
need 'SKIP_UNIT_TESTS' \
  "smoke must allow skipping the pre-cluster unit-test gate"
need 'print_unit_report' \
  "final smoke report must include host unit-test results"
need 'UNIT_CHECK_FILE' \
  "per-package unit results must be written to a file (run_step is a subshell)"
need 'not starting Vagrant cluster' \
  "host unit-test failures must abort before booting VMs"
need 'CLUSTER_STARTED' \
  "pre-cluster unit-test failures must not destroy existing cluster nodes"
need 'step_builder_unit_tests' \
  "smoke must run Linux unit tests on the builder VM before booting cluster nodes"
need 'step_vagrant_up_builder' \
  "builder must come up before cluster nodes so unit tests can gate the 3-node boot"
need 'step_vagrant_up_nodes' \
  "cluster nodes must boot only after builder unit tests pass"
need 'SKIP_BUILDER_UNIT_TESTS' \
  "smoke must allow skipping only the builder Linux suite"
need 'FLYNN_TEST_DOCKER=0' \
  "builder unit tests must run natively (ZFS is unavailable in Docker Desktop)"
need 'not starting cluster nodes' \
  "builder unit-test failures must abort before booting node1/2/3"
need 'flynn_git_safe_directory' \
  "builder must mark the synced repo safe.directory so Go VCS stamping does not fail as root"
if ! grep -Fq -- '-buildvcs=false' "${smoke}"; then
  echo "builder unit tests must disable Go VCS stamping (git status exit 128 on vboxsf)" >&2
  exit 1
fi
need_file "${ROOT}/script/lib/git-safe-dir.sh" \
  "git safe.directory helper must exist for Vagrant/Docker root builds"
if ! grep -Fq -- '-buildvcs=false' "${ROOT}/script/go-build-version"; then
  echo "go-build-version must pass -buildvcs=false (Makefile build → flynn-host)" >&2
  exit 1
fi
if ! grep -Fq -- '-buildvcs=false' "${ROOT}/build.sh"; then
  echo "build.sh flannel-wrapper rebuild must pass -buildvcs=false (vboxsf git status 128)" >&2
  exit 1
fi
if ! grep -Fq -- '-buildvcs=false' "${ROOT}/script/flynn-builder"; then
  echo "script/flynn-builder bootstrap go build must pass -buildvcs=false" >&2
  exit 1
fi
need_file "${ROOT}/script/run-unit-tests" \
  "script/run-unit-tests must exist for the builder Linux gate"
need_file "${ROOT}/script/lib/ui.sh" \
  "smoke uses script/lib/ui.sh for STEP banners"
if grep -v '^#' "${ROOT}/script/lib/ui.sh" | grep -q 'echo -e'; then
  echo "ui.sh must not use echo -e (not portable; macOS bash 3.2 prints \\\\e literally)" >&2
  exit 1
fi
if grep -v '^#' "${ROOT}/script/lib/ui.sh" | grep -qE '\\e\['; then
  echo "ui.sh must not use \\\\e ANSI (use tput or printf \\\\033 on all platforms)" >&2
  exit 1
fi
grep -qF "printf '\\033" "${ROOT}/script/lib/ui.sh" \
  || { echo "ui.sh must keep a printf \\\\033 ANSI fallback for systems without tput setaf" >&2; exit 1; }
grep -q 'tput setaf' "${ROOT}/script/lib/ui.sh" \
  || { echo "ui.sh must try terminfo (tput) before hard-coded ANSI" >&2; exit 1; }
grep -q 'ui_session_begin' "${ROOT}/script/lib/ui.sh" \
  || { echo "ui.sh must support a scoped session theme (not green-on-green)" >&2; exit 1; }
grep -q 'ui_session_begin' "${smoke}" \
  || { echo "smoke must start a UI session so STEP banners are cyan on white body text" >&2; exit 1; }
grep -q '^ok()' "${ROOT}/script/lib/ui.sh" \
  || { echo "ui.sh must have ok() for green STEP OK / PASS" >&2; exit 1; }
grep -q 'ui_status_text' "${smoke}" \
  || { echo "smoke report tables must color PASS green and FAIL red" >&2; exit 1; }
grep -q '22;97' "${ROOT}/script/lib/ui.sh" \
  || { echo "smoke session body text must be normal-weight bright white" >&2; exit 1; }
grep -q '_UI_COLLAPSE_BODY' "${ROOT}/script/lib/ui.sh" \
  || { echo "ui.sh must reprint STEP banners to /dev/tty when command output is collapsed" >&2; exit 1; }
grep -q 'SMOKE_DETAIL' "${smoke}" \
  || { echo "smoke must support SMOKE_DETAIL=1 to stream command output live" >&2; exit 1; }
grep -q 'rprnt' "${smoke}" \
  || { echo "smoke must disable tty rprnt so Ctrl+R is not echoed as ^R" >&2; exit 1; }
grep -q "x12" "${smoke}" \
  || { echo "smoke must read Ctrl+R (ASCII 0x12) from /dev/tty to expand command output" >&2; exit 1; }
if grep -q 'stty status' "${smoke}"; then
  echo "smoke must not use stty status ^R (does not work in Cursor; prints ^R)" >&2
  exit 1
fi
grep -q 'smoke_poll_detail_key' "${smoke}" \
  || { echo "smoke must poll /dev/tty for Ctrl+R while a step is running" >&2; exit 1; }
grep -q 'smoke_toggle_detail' "${smoke}" \
  || { echo "smoke must toggle live command output without hiding the final report" >&2; exit 1; }
grep -q 'tee -a "${LAST_STEP_LOG}" "${SMOKE_RUN_LOG}" >/dev/null' "${smoke}" \
  || { echo "collapsed run_step must log command output without streaming it to the terminal" >&2; exit 1; }
grep -q 'print_results_table' "${smoke}" \
  || { echo "smoke must still print the final results tables" >&2; exit 1; }
grep -q '_UI_COLLAPSE_BODY=0' "${smoke}" \
  || { echo "final report must run with body collapse off so tables are visible" >&2; exit 1; }
if awk '/^say\(\)/,/^}/ { print }' "${ROOT}/script/lib/ui.sh" | grep -v '^[[:space:]]*#' | grep -q '$(ui_wrap'; then
  echo "say() must not capture wrap in command substitution: that makes stdout a pipe so all session colors vanish" >&2
  exit 1
fi
# Session color must survive command substitution (report tables) and pipes (tee).
# Agent/CI shells often set NO_COLOR=1 TERM=dumb FORCE_COLOR=0; unset those so
# this probe matches an interactive smoke run.
ui_probe="$(env -u NO_COLOR -u FORCE_COLOR -u CLICOLOR_FORCE TERM=xterm-256color bash -c '
  # shellcheck source=/dev/null
  source "$1"
  _UI_SESSION=1
  _UI_SESSION_COLOR=1
  ui_wrap cyan "STEP"
' _ "${ROOT}/script/lib/ui.sh")"
case "${ui_probe}" in
  *$'\033'*STEP*) ;;
  *)
    echo "ui_wrap must still emit color inside \$(...) once a session has enabled color" >&2
    exit 1
    ;;
esac
ui_probe_status="$(env -u NO_COLOR -u FORCE_COLOR -u CLICOLOR_FORCE TERM=xterm-256color bash -c '
  source "$1"
  _UI_SESSION=1
  _UI_SESSION_COLOR=1
  ui_status_text PASS
' _ "${ROOT}/script/lib/ui.sh")"
case "${ui_probe_status}" in
  *$'\033'*PASS*) ;;
  *)
    echo "ui_status_text PASS must stay green inside \$(...) during a UI session" >&2
    exit 1
    ;;
esac
if grep -q 'date +%H:%M:%S.%' "${ROOT}/script/lib/ui.sh"; then
  echo "ui.sh must not use GNU date subsecond formats (macOS prints them literally)" >&2
  exit 1
fi
need 'NODE_SSH_FORCE_TTY' \
  "builder unit tests must allocate a remote PTY so pkg/term can open /dev/tty"
need_file "${ROOT}/pkg/term/term_linux_test.go" \
  "pkg/term Linux tests must exist"
grep -q 'controlling TTY required' "${ROOT}/pkg/term/term_linux_test.go" \
  || { echo "pkg/term tests must skip when /dev/tty is missing (headless ssh)" >&2; exit 1; }

if ! grep -F 'seq 1 "${UPGRADE_PASSES}"' "${smoke}" >/dev/null; then
  echo "smoke must loop upgrade passes with seq 1 UPGRADE_PASSES" >&2
  exit 1
fi

echo "ok smoke seeds all datastores, deploys test/apps/upgrade-smoke, and reports per engine"
