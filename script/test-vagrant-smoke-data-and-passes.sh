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
clickhouse_cli_test="${ROOT}/../flynn-plugin-clickhouse/cmd/flynn-clickhouse-cli/main_test.go"
need_file "${clickhouse_cli_test}" "clickhouse plugin CLI stdin hang must have unit tests"
grep -q 'TestApplyClickhouseStdinPolicy' "${clickhouse_cli_test}" \
  || { echo "clickhouse plugin CLI must test applyClickhouseStdinPolicy (INSERT VALUES TTY hang)" >&2; exit 1; }
grep -q 'tty INSERT VALUES' "${clickhouse_cli_test}" \
  || { echo "clickhouse plugin CLI must cover TTY INSERT VALUES stdin close" >&2; exit 1; }
grep -q 'pipe INSERT VALUES' "${clickhouse_cli_test}" \
  || { echo "clickhouse plugin CLI must keep piped stdin for INSERT FORMAT CSV" >&2; exit 1; }

need 'test/apps/upgrade-smoke-docker' \
  "smoke must git-push a Dockerfile app (dockerbuilder-24), not only the slug app"
need 'docker-cli-run' \
  "smoke must flynn run against the Dockerfile app after upgrade, not only HTTP"
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
  "clickhouse seed must create the DB (local MergeTree; Keeper ON CLUSTER is often down)"
need 'clickhouse replica' \
  "clickhouse seed must fan-out schema/rows to every replica so a node drain keeps smoke_db"
need 'clickhouse_row_count' \
  "clickhouse counts must tolerate transient unknown_error after membership changes (set -e)"
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
need 'step_install_plugins' \
  "after bootstrap, smoke must flynn-host plugin install from sibling repos before resource add"
if grep -q 'Reinstall plugins after restore' "${smoke}"; then
  echo "restore must not reinstall plugins; they come back with the postgres backup" >&2
  exit 1
fi
need 'PLUGIN_SMOKE_APPS:-redis mysql mongodb kafka clickhouse dashboard' \
  "default plugin install list must include redis, mysql, mongodb, kafka, clickhouse, and dashboard"
need 'plugin_manifest_matches' \
  "plugin_checkout must resolve mysql from sibling flynn-plugin.json, not a hardcoded mariadb path"
need 'ensure_plugin_vm_mounts' \
  "plugin install must reload VMs when sibling plugin folders are not synced"
need 'already in CLI catalog; skipping hidden-CLI probe' \
  "plugin install must skip the hidden-CLI probe when resuming with plugins already installed"
need 'flynn-plugin-layers-' \
  "plugin-build must overlay ubuntu-noble from this smoke tarball, not a KEEP_BUILDER layer cache"
if grep -qE 'mysql\) echo .*flynn-plugin-mariadb' "${smoke}"; then
  echo "plugin_checkout must not hardcode mysql→mariadb" >&2
  exit 1
fi
need 'flynn-host plugin install' \
  "plugins must be installed with flynn-host, not the user flynn CLI"
need 'FLYNN_PLUGIN_NONINTERACTIVE=1' \
  "plugin install in smoke must not block on TTY setup prompts"
need 'probe_delegated_plugin_cli_hidden' \
  "before plugin install, flynn help must hide redis and flynn redis must fail"
need 'probe_delegated_plugin_cli_visible' \
  "after plugin install, flynn help must list redis from the cluster catalog"
need 'cli-redis-dump' \
  "live CLI must dump redis through the plugin job, not a compiled handler"
need 'plugin_dist_ready' \
  "smoke must rebuild plugin dist when image.json is overlay-only (no ubuntu-noble)"
need 'flynn.plugin.files' \
  "smoke must rebuild plugin dist when binaries were not installed into the overlay (ENOENT /bin/start-*)"
need 'flynn.plugin.arch' \
  "smoke must rebuild plugin dist when Go binaries do not match the Flynn host architecture (exit 126)"
need 'FLYNN_LAYERS_DIR' \
  "plugin-build must overlay the local Flynn ubuntu-noble layer, not GitHub's same-ID other-arch squashfs"
need 'dump_plugin_install_diagnostics' \
  "plugin install failure must dump flynn-host job/squashfs logs, not only the scale timeout"
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
need 'RESUME_AT=backup' \
  "smoke must be able to resume at cluster backup after a failed dump/restore"
need 'RESUME_AT=restore' \
  "smoke must be able to resume at bootstrap --from-backup after a failed restore"
need 'db-check' \
  "assert_databases must log per-engine progress so a hang is obvious"
need 'step_host_unit_tests' \
  "smoke must run host unit tests before Vagrant up"
need 'validate-gofmt' \
  "smoke host unit-test gate must run gofmt (same check as GitHub Actions)"
need_file "${ROOT}/util/commit-validator/validate-gofmt" \
  "gofmt check used by CI, smoke, and unit tests must exist"
need_file "${ROOT}/script/githooks/gofmt-check" \
  "pre-commit/pre-push gofmt hook must exist"
need_file "${ROOT}/script/install-git-hooks" \
  "clones must be able to install gofmt git hooks without git config"
if ! grep -q 'validate-gofmt' "${ROOT}/script/githooks/gofmt-check"; then
  echo "gofmt git hook must run util/commit-validator/validate-gofmt" >&2
  exit 1
fi
if ! grep -q 'validate-gofmt' "${ROOT}/script/run-unit-tests"; then
  echo "script/run-unit-tests must run validate-gofmt" >&2
  exit 1
fi
if grep -q 'gofmt issues found (continuing' "${ROOT}/script/docker/unit-tests/entrypoint.sh"; then
  echo "Docker unit tests must fail on gofmt, not continue" >&2
  exit 1
fi
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
grep -q 'ui_table_cell' "${smoke}" \
  || { echo "smoke report tables must pad/truncate cells so columns stay aligned" >&2; exit 1; }
grep -q 'ui_trunc' "${ROOT}/script/lib/ui.sh" \
  || { echo "ui.sh must truncate over-wide table cells with ellipsis" >&2; exit 1; }
grep -q 'ui_strip_ansi' "${ROOT}/script/lib/ui.sh" \
  || { echo "ui.sh must strip ANSI before measuring table cell width" >&2; exit 1; }
if grep -E 'printf "\| %-16s' "${smoke}"; then
  echo "Check column must not be hard-coded to 16 chars (docker-cli-run-image is 20)" >&2
  exit 1
fi
if grep -E 'printf "\| %-24s' "${smoke}"; then
  echo "Phase column must not be hard-coded to 24 chars (3-node-remove/post-upgrade-2 is 28)" >&2
  exit 1
fi
# Truncate/pad must keep every row the same width even when names overflow.
ui_align="$(env -u NO_COLOR -u FORCE_COLOR -u CLICOLOR_FORCE TERM=dumb bash -c '
  source "$1"
  NO_COLOR=1
  w=16
  a=$(printf "| %s | %s |\n" "$(ui_table_cell "docker-cli-run-image" "$w")" "$(ui_table_cell "ID                                          TYPE" 20)")
  b=$(printf "| %s | %s |\n" "$(ui_table_cell "postgres" "$w")" "$(ui_table_cell "rows=200" 20)")
  echo "$a"
  echo "$b"
' _ "${ROOT}/script/lib/ui.sh")"
cell="$(env -u NO_COLOR TERM=dumb bash -c 'source "$1"; ui_table_cell "docker-cli-run-image" 16' _ "${ROOT}/script/lib/ui.sh")"
if [[ ${#cell} -ne 16 ]]; then
  echo "ui_table_cell must print exactly the requested width (got ${#cell} for 16)" >&2
  exit 1
fi
case "${cell}" in
  *...*) ;;
  *)
    echo "ui_table_cell must ellipsize docker-cli-run-image in a 16-col cell (got '${cell}')" >&2
    exit 1
    ;;
esac
align_a="$(printf '%s\n' "${ui_align}" | sed -n '1p')"
align_b="$(printf '%s\n' "${ui_align}" | sed -n '2p')"
if [[ ${#align_a} -ne ${#align_b} ]]; then
  echo "aligned table rows must be the same length (${#align_a} vs ${#align_b})" >&2
  echo "${ui_align}" >&2
  exit 1
fi
plain="$(printf '\033[1;36mBackup complete.\033[0m' | env TERM=dumb bash -c 'source "$1"; ui_strip_ansi' _ "${ROOT}/script/lib/ui.sh")"
if [[ "${plain}" != "Backup complete." ]]; then
  echo "ui_strip_ansi must drop CSI sequences (got '${plain}')" >&2
  exit 1
fi
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
grep -q 'smoke_tty_usable' "${smoke}" \
  || { echo "smoke must skip /dev/tty when there is no controlling terminal" >&2; exit 1; }
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
