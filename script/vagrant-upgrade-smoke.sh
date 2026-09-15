#!/bin/bash
#
# Vagrant Flynn upgrade smoke test (local builder only).
#
# Flow:
#   1. Run host unit tests (CLI clickhouse stdin policy, datastore/overlay
#      regressions, Darwin-safe Go packages). Fail before Vagrant if any fail.
#   2. Boot the builder VM only, then run the full Linux unit suite there
#      (redis, postgres, mariadb, mongodb, zfs/netlink). Fail before booting
#      cluster nodes if that suite fails. Docker Desktop cannot load ZFS, so
#      the builder is the Linux gate — not script/run-unit-tests in Docker.
#   3. Build Flynn on the builder and package a release tarball into the
#      repo's build/release/ (shared with nodes via Vagrant synced folder)
#   4. For each topology in SMOKE_TOPOLOGIES (default: 1-node singleton, then
#      3-node HA): boot those VMs, install the tarball, bootstrap
#      (--min-hosts N --peer-ips …), deploy test/apps/upgrade-smoke with every
#      datastore provider, git-push test/apps/upgrade-smoke-docker on the
#      container stack (dockerbuilder-24), verify HTTP/status/rows, exercise flynn /
#      flynn-host, run flynn-host update --all-nodes --tarball --force twice,
#      re-verify, then flynn cluster backup, wipe Flynn (--clean), bootstrap
#      --from-backup, and re-verify apps plus postgres/mysql/mongodb. Plugin
#      apps restore with postgres (no second flynn-host plugin install). Redis,
#      Kafka, and ClickHouse volumes are not in the cluster backup; those
#      engines must come back empty. Then destroy the cluster nodes (builder
#      is kept) before the next topology. Sizes are 1 (singleton) or >=3 (HA);
#      2 is invalid.
#      Named topologies: add (stable 3-node, then join node4 and upgrade)
#      and remove (stable 3-node, then drain node3; HTTP/DBs/deploys must
#      keep working). Vagrant nodes are generated as node1..max(N) —
#      e.g. 1,3,5 or 1,3,7; add reserves node4.
#   5. Print a step table, unit-test results, and a per-engine persistence
#      report (phases are prefixed N-node/).
#
# Local-only: layer-0 uses --peer-ips (no discovery service). Init waits for
# flynn-host HTTP (:1113); discoverd (:1111) starts during bootstrap. Layer-1
# uses CLUSTER_DOMAIN entries in /etc/hosts on each VM — no real DNS records.
# Shared VM logs live in ./flynn-logs/{builder,node*} and are cleared at start
# unless KEEP_LOGS=1.
#
# Usage (from repo root):
#   script/vagrant-upgrade-smoke.sh
#
# Environment:
#   BUILD_VERSION        Version string for the local build/tarball
#                        [default: vYYYYMMDD.N-smoke]
#   BUILD_PHASE          build.sh phase: cluster|all|auto [default: auto]
#   CLUSTER_DOMAIN       Bootstrap domain [default: upgrade-smoke.localflynn.com]
#   APP_NAME             Slug/buildpack test app [default: upgrade-smoke]
#   DOCKER_APP_NAME      Dockerfile/container-stack app
#                        [default: upgrade-smoke-docker]
#   VAGRANT_MEMORY       Cluster node RAM MB [default: 6144]
#   VAGRANT_CPUS         Cluster node CPUs [default: 2]
#   BUILDER_MEMORY       Builder RAM MB [default: 30000]
#   BUILDER_CPUS         Builder CPUs [default: 8]
#   KEEP_VMS=1           Do not destroy any VMs at the end (success or failure)
#   KEEP_BUILDER=1       On success, keep builder when tearing down nodes [default: 1]
#   KEEP_VMS_ON_FAIL=1   On failure, keep VMs for debugging (default: destroy all)
#   KEEP_LOGS=1          Do not clear ./flynn-logs/{builder,node*}
#                        at start (default: clear so each run has fresh logs)
#   SKIP_UNIT_TESTS=1            Skip host + builder Linux pre-cluster unit gates
#                                (host gate includes gofmt -s / validate-gofmt)
#   SKIP_BUILDER_UNIT_TESTS=1    Skip only the builder Linux suite (host tests
#                                still run). SKIP_DOCKER_UNIT_TESTS=1 is an alias.
#   SKIP_VAGRANT_UP=1    Assume VMs are already running
#   SKIP_BUILD=1         Skip builder build; use existing
#                        build/release/flynn-${BUILD_VERSION}.tar.gz
#   FLYNN_BUILD_ATTEMPTS How many times to retry a transient builder apt/network
#                        failure [default: 3]
#   SKIP_INSTALL=1       Assume Flynn is already installed/bootstrapped
#                        (skips install+init+bootstrap)
#   RESUME_AT=bootstrap  Skip vagrant/build/install/init; run bootstrap onward
#                        (nodes must already have flynn-host inited and :1113 up)
#   RESUME_AT=upgrade    Skip through pre-upgrade verify; run --force tarball
#                        updates (app + datastores must already be deployed)
#   RESUME_AT=backup     Skip through upgrades; run cluster backup, wipe,
#                        bootstrap --from-backup, and post-restore verify
#                        (cluster must already be upgraded with plugins/apps)
#   RESUME_AT=restore    Skip backup; reinstall --clean and bootstrap
#                        --from-backup using the existing smoke-backup tar
#   SKIP_UPGRADE=1       Skip the local tarball --all-nodes update passes
#   SKIP_BACKUP=1        Skip cluster backup, wipe, bootstrap --from-backup,
#                        and post-restore verify
#   SKIP_CLI=1           Skip live flynn / flynn-host CLI function steps
#   SMOKE_TOPOLOGIES     Comma-separated topologies, each getting
#                        install/bootstrap/deploy/verify/upgrade/backup/CLI.
#                        1 = singleton; N>=3 = HA; 2 is invalid (Flynn).
#                        add (aliases: add-node, 3+1) = boot 3-node HA,
#                        wait until stable, join node4, re-verify, then
#                        upgrade --all-nodes (must include the new host).
#                        remove (aliases: remove-node, 3-1) = boot 3-node
#                        HA, drain node3, re-verify HTTP/DBs/deploys on
#                        the remaining hosts, then upgrade.
#                        [default: 1,3]
#                        CLUSTER_SIZE=N is a shortcut for one topology.
#                        Example: SMOKE_TOPOLOGIES=1,3,5,add,remove
#   SMOKE_MAX_NODES      Optional ceiling on N (host-only /24 already caps
#                        node IPs at 192.168.56.254). Unset = no extra cap.
#   UPGRADE_PASSES=N     How many --force tarball updates to run [default: 2]
#   SMOKE_SEED_ROWS=N    Dummy rows/keys seeded per datastore [default: 200]
#   SMOKE_BLOB_COUNT=N   Extra slug files embedded in the test app [default: 100]
#   SMOKE_DETAIL=1       Stream command output live (default: hide it; STEP/
#                        STEP OK/WARN stay visible; Ctrl+R expands the log
#                        without echoing ^R)
#   SKIP_TEARDOWN=1      Alias for KEEP_VMS=1
#   PLUGIN_SMOKE_APPS    Space-separated plugin aliases to flynn-host install
#                        after bootstrap, before resource add (default: redis
#                        mysql mongodb kafka clickhouse). mysql resolves to the
#                        mariadb checkout via flynn-plugin.json aliases.
#                        Restore does not install again; plugins.json + postgres
#                        already list and restore them.
#   PLUGIN_REPO_ROOT     Parent of flynn-plugin-* checkouts (default: ..)
#   SKIP_PLUGIN_INSTALL=1  Assume plugins are already installed
#

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${ROOT}"

source "${ROOT}/script/lib/ui.sh"

BUILD_VERSION="${BUILD_VERSION:-}"
BUILD_PHASE="${BUILD_PHASE:-auto}"
CLUSTER_DOMAIN="${CLUSTER_DOMAIN:-upgrade-smoke.localflynn.com}"
APP_NAME="${APP_NAME:-upgrade-smoke}"
DOCKER_APP_NAME="${DOCKER_APP_NAME:-upgrade-smoke-docker}"
REPO_IN_VM="/root/go/src/github.com/flynn/flynn"
CLI_REPO="${FLYNN_GITHUB_REPO:-randy-girard/flynn}"

# nodeN is 192.168.56.(19+N): node1=.20, node2=.21, … last octet <= 254.
# Size 2 is invalid (Flynn HA minimum is 3). apply_topology / Vagrantfile
# generate the first N names and IPs; FLYNN_MAX_NODES is exported so
# Vagrantfile defines enough machines for the largest topology this run.
CLUSTER_NET_PREFIX="192.168.56"
CLUSTER_IP_OFFSET=19
NODE1_IP="${CLUSTER_NET_PREFIX}.$((CLUSTER_IP_OFFSET + 1))"
ALL_CLUSTER_NODES=()
ALL_CLUSTER_IPS=()
# apply_topology sets NODES / NODE_IPS / PEER_IPS / MIN_HOSTS per run.
# NODES is the live Flynn cluster. TEARDOWN_NODES is every VM this topology
# created (includes a drained host after remove).
NODES=(node1)
NODE_IPS=("${NODE1_IP}")
PEER_IPS="${NODE1_IP}"
TEARDOWN_NODES=(node1)
MIN_HOSTS=1
TOPOLOGY_SIZE=1
TOPOLOGY_LABEL="1-node"
TOPOLOGY_ACTION=""
CHECK_PHASE_PREFIX=""
TOPOLOGIES=()
BOOTSTRAP_JOB_TIMEOUT="${BOOTSTRAP_JOB_TIMEOUT:-600}"

KEEP_VMS="${KEEP_VMS:-${SKIP_TEARDOWN:-0}}"
KEEP_BUILDER="${KEEP_BUILDER:-1}"
KEEP_VMS_ON_FAIL="${KEEP_VMS_ON_FAIL:-0}"
KEEP_LOGS="${KEEP_LOGS:-0}"
SKIP_UNIT_TESTS="${SKIP_UNIT_TESTS:-0}"
SKIP_BUILDER_UNIT_TESTS="${SKIP_BUILDER_UNIT_TESTS:-${SKIP_DOCKER_UNIT_TESTS:-0}}"
SKIP_VAGRANT_UP="${SKIP_VAGRANT_UP:-0}"
SKIP_BUILD="${SKIP_BUILD:-0}"
FLYNN_BUILD_ATTEMPTS="${FLYNN_BUILD_ATTEMPTS:-3}"
SKIP_INSTALL="${SKIP_INSTALL:-0}"
SKIP_DEPLOY="${SKIP_DEPLOY:-0}"
SKIP_VERIFY_BEFORE="${SKIP_VERIFY_BEFORE:-0}"
SKIP_UPGRADE="${SKIP_UPGRADE:-0}"
SKIP_BACKUP="${SKIP_BACKUP:-0}"
SKIP_CLI="${SKIP_CLI:-0}"
if [[ -n "${SMOKE_TOPOLOGIES:-}" ]]; then
  :
elif [[ -n "${CLUSTER_SIZE:-}" ]]; then
  SMOKE_TOPOLOGIES="${CLUSTER_SIZE}"
else
  SMOKE_TOPOLOGIES="1,3"
fi
UPGRADE_PASSES="${UPGRADE_PASSES:-2}"
SMOKE_SEED_ROWS="${SMOKE_SEED_ROWS:-200}"
SMOKE_BLOB_COUNT="${SMOKE_BLOB_COUNT:-100}"
SMOKE_DETAIL="${SMOKE_DETAIL:-0}"
RESUME_AT="${RESUME_AT:-}"
SHARED_LOG_DIRS=(builder)
DATASTORE_PROVIDERS=(postgres mysql mongodb redis kafka clickhouse)
PLUGIN_REPO_ROOT="${PLUGIN_REPO_ROOT:-$(cd "${ROOT}/.." && pwd)}"
PLUGIN_SMOKE_APPS="${PLUGIN_SMOKE_APPS:-redis mysql mongodb kafka clickhouse}"
SKIP_PLUGIN_INSTALL="${SKIP_PLUGIN_INSTALL:-0}"
# Host-side packages that compile without Linux netlink/ZFS. Run before Vagrant
# so a broken CLI/datastore change cannot burn a 3-node cluster boot.
SMOKE_UNIT_PACKAGES=(
  ./cli/
  ./controller/types/
  ./controller/authz/
  ./pkg/httphelper/
  ./pkg/updaterdeploy/
  ./pkg/sirenia/state/
  ./pkg/iptables/
  ./pkg/netpolicy/
  ./pkg/squashfs/
  ./pkg/dockerimage/
  ./pkg/plugin/
  ./appliance/postgresql/cmd/flynn-postgres-api/
  ./updater/
)

RESULT_NAMES=()
RESULT_STATUS=()
RESULT_SECONDS=()
RESULT_DETAIL=()
OVERALL_FAILED=0
BUILT_TARBALL=""
STARTED_AT="$(date +%s)"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/flynn-upgrade-smoke.XXXXXX")"
LAST_STEP_LOG="${WORK_DIR}/last-step.log"
SMOKE_RUN_LOG="${WORK_DIR}/smoke.log"
CHECK_FILE="${WORK_DIR}/checks.tsv"
UNIT_CHECK_FILE="${WORK_DIR}/unit-checks.tsv"
: > "${CHECK_FILE}"
: > "${UNIT_CHECK_FILE}"
: > "${SMOKE_RUN_LOG}"
CLEANUP_DONE=0
ABORTING=0
BUILDER_STARTED=0
CLUSTER_STARTED=0
SMOKE_DETAIL_LIVE=0
SMOKE_DETAIL_TAIL_PID=""
SMOKE_STTY_SAVED=""
STEP_PID=""
STEP_RC_FILE="${WORK_DIR}/step.rc"

cleanup() {
  smoke_stop_detail_tail
  smoke_restore_tty
  ui_session_end 2>/dev/null || true
  rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

# Catch unexpected failures outside run_step (e.g. SKIP_BUILD missing tarball).
on_err() {
  local ec=$1
  local line=$2
  if [[ "${ABORTING}" == "1" ]]; then
    return 0
  fi
  ABORTING=1
  local msg="unexpected error at line ${line} (exit ${ec})"
  if [[ -f "${LAST_STEP_LOG}" ]] && [[ -s "${LAST_STEP_LOG}" ]]; then
    msg="${msg}"$'\n'"$(tail -n 100 "${LAST_STEP_LOG}")"
  fi
  fail_shutdown "script error" 0 "${msg}"
}
trap 'on_err $? $LINENO' ERR

usage() {
  awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; next} {exit}' "$0"
}

require_bin() {
  local bin
  for bin in "$@"; do
    if ! command -v "${bin}" >/dev/null 2>&1; then
      fail "required binary not found: ${bin}"
    fi
  done
}

# Map Postgres boolean text to t/f. A bare boolean column is t/f; concatenating
# booleans with || prints true/false (seen 2026-09-13 cli-pg-connect).
smoke_pg_tf() {
  local s
  s="$(printf '%s' "${1:-}" | tr -d '[:space:]' | tr '[:upper:]' '[:lower:]')"
  s="${s//true/t}"
  s="${s//false/f}"
  printf '%s' "${s}"
}

# Collapse a detail string to one line of visible text (no ANSI, no tabs).
smoke_plain_detail() {
  local detail=$1
  detail="$(ui_strip_ansi "${detail}")"
  detail="${detail//$'\t'/ }"
  detail="${detail//$'\n'/ }"
  printf '%s' "${detail}" | tr -s ' '
}

record() {
  local name=$1 status=$2 seconds=$3 detail=${4:-}
  detail="$(smoke_plain_detail "${detail}")"
  RESULT_NAMES+=("${name}")
  RESULT_STATUS+=("${status}")
  RESULT_SECONDS+=("${seconds}")
  RESULT_DETAIL+=("${detail}")
  if [[ "${status}" != "PASS" && "${status}" != "SKIP" ]]; then
    OVERALL_FAILED=1
  fi
}

# Per-engine / HTTP checks. Written to CHECK_FILE because run_step executes the
# step in a subshell (tee), so bash arrays would be lost before the report.
record_check() {
  local phase=$1 name=$2 status=$3 detail=${4:-}
  if [[ -n "${CHECK_PHASE_PREFIX:-}" ]]; then
    phase="${CHECK_PHASE_PREFIX}${phase}"
  fi
  detail="$(smoke_plain_detail "${detail}")"
  printf '%s\t%s\t%s\t%s\n' "${phase}" "${name}" "${status}" "${detail}" >> "${CHECK_FILE}"
  if [[ "${status}" != "PASS" && "${status}" != "SKIP" ]]; then
    OVERALL_FAILED=1
  fi
}

# Host unit-test gate. Same file-backed pattern as record_check (run_step is a
# subshell). Failures here abort before Vagrant up.
record_unit_check() {
  local kind=$1 name=$2 status=$3 detail=${4:-}
  detail="$(smoke_plain_detail "${detail}")"
  printf '%s\t%s\t%s\t%s\n' "${kind}" "${name}" "${status}" "${detail}" >> "${UNIT_CHECK_FILE}"
  if [[ "${status}" != "PASS" && "${status}" != "SKIP" ]]; then
    OVERALL_FAILED=1
  fi
}

step_log_summary() {
  if [[ ! -s "${LAST_STEP_LOG}" ]]; then
    echo ""
    return
  fi
  awk 'NF { p=$0 } END { print p }' "${LAST_STEP_LOG}" \
    | tr '\n' ' ' \
    | ui_strip_ansi \
    | tr -s ' ' \
    | cut -c1-160
}

# Prefer the actual failure over go-test coverage noise / apt Get:1 lines.
# Search LAST_STEP_LOG first: fail_shutdown only passes the last 40 lines, which
# for `go test ./...` is often "coverage: 0.0%" for packages without tests.
step_fail_summary() {
  local detail=$1
  local err=""
  if [[ -n "${LAST_STEP_LOG:-}" && -s "${LAST_STEP_LOG}" ]]; then
    err="$(grep -E 'WARNING: DATA RACE|^--- FAIL:|^FAIL	|race detected|panic:|^E: |Failed to fetch|make: \*\*\*' "${LAST_STEP_LOG}" | tail -n 4 | tr '\n' ' ')"
  fi
  if [[ -z "${err}" ]]; then
    err="$(printf '%s\n' "${detail}" | grep -E 'WARNING: DATA RACE|^--- FAIL:|^FAIL	|race detected|panic:|^E: |Err:|Failed to fetch|apt-get .* failed|make: \*\*\*' | tail -n 3 | tr '\n' ' ')"
  fi
  if [[ -n "${err}" ]]; then
    echo "${err}" | ui_strip_ansi | tr -s ' ' | cut -c1-200
    return
  fi
  echo "${detail}" | tr '\n' ' ' | ui_strip_ansi | tr -s ' ' | cut -c1-160
}

# Terminal width for report tables. Prefer COLUMNS (Cursor/ssh often set it);
# fall back to tput, then 120 so a typical laptop window does not wrap.
smoke_term_cols() {
  local cols="${COLUMNS:-}"
  if [[ -z "${cols}" || "${cols}" -lt 40 ]]; then
    if [[ -t 1 ]] && command -v tput >/dev/null 2>&1; then
      cols="$(tput cols 2>/dev/null || true)"
    fi
  fi
  if [[ -z "${cols}" || "${cols}" -lt 80 ]]; then
    cols=120
  fi
  printf '%s' "${cols}"
}

# Grow a column to fit `n`, then cap so one long cell cannot blow the table.
smoke_col_cap() {
  local n=$1
  local cap=$2
  if [[ "${n}" -gt "${cap}" ]]; then
    printf '%s' "${cap}"
  else
    printf '%s' "${n}"
  fi
}

# Remaining columns for Detail. `used` is everything before the last cell
# (`| ` cells ` | ` ... ` | `). Min 24 so FAIL snippets stay readable; max 60
# so a huge terminal does not dump whole CLI tables.
smoke_detail_width() {
  local used=$1
  local remaining
  remaining=$(( $(smoke_term_cols) - used ))
  if [[ "${remaining}" -lt 24 ]]; then
    remaining=24
  fi
  if [[ "${remaining}" -gt 60 ]]; then
    remaining=60
  fi
  printf '%s' "${remaining}"
}

# `|----+----|` matching ui_table_cell widths (two padding spaces per cell).
smoke_table_rule() {
  local w
  printf '|'
  for w in "$@"; do
    printf '%s|' "$(printf '%*s' $((w + 2)) '' | tr ' ' '-')"
  done
  printf '\n'
}

print_unit_report() {
  echo
  ui_banner "================================================================================"
  ui_banner " Unit tests (host + builder Linux gate)"
  ui_banner "================================================================================"
  if [[ ! -s "${UNIT_CHECK_FILE}" ]]; then
    echo " (no host unit tests recorded)"
    echo "================================================================================"
    return
  fi
  local kind_w=4 check_w=5
  local kind name status detail failed=0 total=0
  while IFS=$'\t' read -r kind name status detail; do
    [[ -z "${kind}" ]] && continue
    total=$((total + 1))
    if [[ ${#kind} -gt ${kind_w} ]]; then
      kind_w=${#kind}
    fi
    if [[ ${#name} -gt ${check_w} ]]; then
      check_w=${#name}
    fi
  done < "${UNIT_CHECK_FILE}"
  local tot_label="${total} checks"
  if [[ ${#tot_label} -gt ${check_w} ]]; then
    check_w=${#tot_label}
  fi
  kind_w="$(smoke_col_cap "${kind_w}" 10)"
  check_w="$(smoke_col_cap "${check_w}" 48)"
  local detail_w
  detail_w="$(smoke_detail_width $((19 + kind_w + check_w)))"
  printf '| %s | %s | %s | %s |\n' \
    "$(ui_table_cell "Kind" "${kind_w}")" \
    "$(ui_table_cell "Check" "${check_w}")" \
    "$(ui_table_cell "Status" 6)" \
    "$(ui_table_cell "Detail" "${detail_w}")"
  smoke_table_rule "${kind_w}" "${check_w}" 6 "${detail_w}"
  while IFS=$'\t' read -r kind name status detail; do
    [[ -z "${kind}" ]] && continue
    if [[ "${status}" != "PASS" && "${status}" != "SKIP" ]]; then
      failed=$((failed + 1))
      OVERALL_FAILED=1
    fi
    printf '| %s | %s | %s | %s |\n' \
      "$(ui_table_cell "${kind}" "${kind_w}")" \
      "$(ui_table_cell "${name}" "${check_w}")" \
      "$(ui_status_text "${status}")" \
      "$(ui_table_cell "${detail}" "${detail_w}")"
  done < "${UNIT_CHECK_FILE}"
  local tot_status="PASS" tot_detail="all host unit tests passed"
  if [[ ${failed} -ne 0 ]]; then
    tot_status="FAIL"
    tot_detail="${failed} failed"
  fi
  printf '| %s | %s | %s | %s |\n' \
    "$(ui_table_cell "TOTAL" "${kind_w}")" \
    "$(ui_table_cell "${tot_label}" "${check_w}")" \
    "$(ui_status_text "${tot_status}")" \
    "$(ui_table_cell "${tot_detail}" "${detail_w}")"
  ui_banner "================================================================================"
}

print_datastore_report() {
  echo
  ui_banner "================================================================================"
  ui_banner " App, CLI & datastore persistence"
  echo " app=${APP_NAME}  docker_app=${DOCKER_APP_NAME}  seed_rows=${SMOKE_SEED_ROWS}  blobs=${SMOKE_BLOB_COUNT}  passes=${UPGRADE_PASSES}"
  echo " topologies=${SMOKE_TOPOLOGIES}  providers=${DATASTORE_PROVIDERS[*]}"
  ui_banner "================================================================================"
  if [[ ! -s "${CHECK_FILE}" ]]; then
    echo " (no app/datastore checks recorded — verify steps did not run)"
    echo "================================================================================"
    return
  fi
  local phase_w=5 check_w=5
  local phase name status detail failed=0 total=0
  while IFS=$'\t' read -r phase name status detail; do
    [[ -z "${phase}" ]] && continue
    total=$((total + 1))
    if [[ ${#phase} -gt ${phase_w} ]]; then
      phase_w=${#phase}
    fi
    if [[ ${#name} -gt ${check_w} ]]; then
      check_w=${#name}
    fi
  done < "${CHECK_FILE}"
  local tot_label="${total} checks"
  if [[ ${#tot_label} -gt ${check_w} ]]; then
    check_w=${#tot_label}
  fi
  # 3-node-remove/post-upgrade-2 is 28; docker-cli-run-image is 20.
  phase_w="$(smoke_col_cap "${phase_w}" 32)"
  check_w="$(smoke_col_cap "${check_w}" 24)"
  local detail_w
  detail_w="$(smoke_detail_width $((19 + phase_w + check_w)))"
  printf '| %s | %s | %s | %s |\n' \
    "$(ui_table_cell "Phase" "${phase_w}")" \
    "$(ui_table_cell "Check" "${check_w}")" \
    "$(ui_table_cell "Status" 6)" \
    "$(ui_table_cell "Detail" "${detail_w}")"
  smoke_table_rule "${phase_w}" "${check_w}" 6 "${detail_w}"
  while IFS=$'\t' read -r phase name status detail; do
    [[ -z "${phase}" ]] && continue
    if [[ "${status}" != "PASS" && "${status}" != "SKIP" ]]; then
      failed=$((failed + 1))
      OVERALL_FAILED=1
    fi
    printf '| %s | %s | %s | %s |\n' \
      "$(ui_table_cell "${phase}" "${phase_w}")" \
      "$(ui_table_cell "${name}" "${check_w}")" \
      "$(ui_status_text "${status}")" \
      "$(ui_table_cell "${detail}" "${detail_w}")"
  done < "${CHECK_FILE}"
  local tot_status="PASS" tot_detail="all app/CLI/DB checks passed"
  if [[ ${failed} -ne 0 ]]; then
    tot_status="FAIL"
    tot_detail="${failed} failed"
  fi
  printf '| %s | %s | %s | %s |\n' \
    "$(ui_table_cell "TOTAL" "${phase_w}")" \
    "$(ui_table_cell "${tot_label}" "${check_w}")" \
    "$(ui_status_text "${tot_status}")" \
    "$(ui_table_cell "${tot_detail}" "${detail_w}")"
  ui_banner "================================================================================"
}

print_results_table() {
  local total=$(( $(date +%s) - STARTED_AT ))
  local overall="PASS"
  if [[ "${OVERALL_FAILED}" -ne 0 ]]; then
    overall="FAIL"
  fi

  echo
  ui_banner "================================================================================"
  ui_banner " Flynn Vagrant upgrade smoke results (local build)"
  echo " build=${BUILD_VERSION:-n/a}  domain=${CLUSTER_DOMAIN}  app=${APP_NAME}  docker_app=${DOCKER_APP_NAME}"
  echo " topologies=${SMOKE_TOPOLOGIES}  seed_rows=${SMOKE_SEED_ROWS}  blobs=${SMOKE_BLOB_COUNT}  upgrade_passes=${UPGRADE_PASSES}"
  if [[ -n "${BUILT_TARBALL}" ]]; then
    echo " tarball=${BUILT_TARBALL}"
  fi
  ui_banner "================================================================================"
  local step_w=4
  local i
  for i in "${!RESULT_NAMES[@]}"; do
    if [[ ${#RESULT_NAMES[$i]} -gt ${step_w} ]]; then
      step_w=${#RESULT_NAMES[$i]}
    fi
  done
  if [[ ${step_w} -lt 8 ]]; then
    step_w=8
  fi
  # Verify app/DBs after upgrade 2/2 (3-node-remove) is 49.
  step_w="$(smoke_col_cap "${step_w}" 56)"
  local detail_w
  # `| ` step ` | ` status ` | ` duration ` | ` detail ` |`  => 27 + step + detail
  detail_w="$(smoke_detail_width $((27 + step_w)))"
  printf '| %s | %s | %s | %s |\n' \
    "$(ui_table_cell "Step" "${step_w}")" \
    "$(ui_table_cell "Status" 6)" \
    "$(ui_table_cell "Duration" 8)" \
    "$(ui_table_cell "Detail" "${detail_w}")"
  smoke_table_rule "${step_w}" 6 8 "${detail_w}"
  for i in "${!RESULT_NAMES[@]}"; do
    printf '| %s | %s | %s | %s |\n' \
      "$(ui_table_cell "${RESULT_NAMES[$i]}" "${step_w}")" \
      "$(ui_status_text "${RESULT_STATUS[$i]}")" \
      "$(printf '%8s' "${RESULT_SECONDS[$i]}s")" \
      "$(ui_table_cell "${RESULT_DETAIL[$i]}" "${detail_w}")"
  done
  printf '| %s | %s | %s | %s |\n' \
    "$(ui_table_cell "OVERALL" "${step_w}")" \
    "$(ui_status_text "${overall}")" \
    "$(printf '%8s' "${total}s")" \
    "$(ui_table_cell "" "${detail_w}")"
  ui_banner "================================================================================"
  print_unit_report
  print_datastore_report

  local preserved="${ROOT}/.vagrant-upgrade-smoke-last-results.txt"
  {
    echo "overall=${overall} duration=${total}s build=${BUILD_VERSION:-n/a} app=${APP_NAME}"
    echo "topologies=${SMOKE_TOPOLOGIES} seed_rows=${SMOKE_SEED_ROWS} blobs=${SMOKE_BLOB_COUNT} upgrade_passes=${UPGRADE_PASSES}"
    for i in "${!RESULT_NAMES[@]}"; do
      echo "step ${RESULT_STATUS[$i]} ${RESULT_SECONDS[$i]}s ${RESULT_NAMES[$i]} :: ${RESULT_DETAIL[$i]}"
    done
    echo "--- host unit tests ---"
    if [[ ! -s "${UNIT_CHECK_FILE}" ]]; then
      echo "(none recorded)"
    else
      cat "${UNIT_CHECK_FILE}"
    fi
    echo "--- app/datastore ---"
    if [[ ! -s "${CHECK_FILE}" ]]; then
      echo "(none recorded)"
    else
      cat "${CHECK_FILE}"
    fi
  } > "${preserved}" 2>/dev/null || true
  if [[ -f "${preserved}" ]]; then
    echo "Wrote ${preserved}"
  fi
}

print_failure_banner() {
  local name=$1
  local detail=$2
  echo >&2
  say "################################################################################" "red" >&2
  say "# FAILURE: ${name}" "red" >&2
  say "################################################################################" "red" >&2
  if [[ -n "${detail}" ]]; then
    echo "${detail}" >&2
  fi
  if [[ -f "${LAST_STEP_LOG}" ]] && [[ -s "${LAST_STEP_LOG}" ]]; then
    echo >&2
    echo "----- last step output (tail) --------------------------------------------------" >&2
    tail -n 120 "${LAST_STEP_LOG}" >&2 || true
    echo "--------------------------------------------------------------------------------" >&2
    echo "Full step log: ${LAST_STEP_LOG}" >&2
    # Preserve log outside WORK_DIR before EXIT cleanup removes it.
    local preserved="${ROOT}/.vagrant-upgrade-smoke-last-failure.log"
    cp -f "${LAST_STEP_LOG}" "${preserved}" 2>/dev/null || true
    if [[ -f "${preserved}" ]]; then
      echo "Copied to: ${preserved}" >&2
    fi
  fi
  echo "################################################################################" >&2
  echo >&2
}

destroy_all_vms() {
  expand_cluster_inventory
  info "destroying Vagrant VMs: builder ${ALL_CLUSTER_NODES[*]}"
  vagrant destroy -f builder "${ALL_CLUSTER_NODES[@]}" || true
}

smoke_stop_detail_tail() {
  local pid="${SMOKE_DETAIL_TAIL_PID:-}"
  SMOKE_DETAIL_TAIL_PID=""
  if [[ -n "${pid}" ]]; then
    kill "${pid}" 2>/dev/null || true
    wait "${pid}" 2>/dev/null || true
  fi
}

# macOS still has /dev/tty with no controlling terminal; opening it then
# fails with "Device not configured" and trips set -e (agent / no-TTY runs).
smoke_tty_usable() {
  { exec 3<>/dev/tty; } 2>/dev/null || return 1
  exec 3>&-
  return 0
}

smoke_restore_tty() {
  if [[ -n "${SMOKE_STTY_SAVED:-}" ]] && smoke_tty_usable; then
    stty "${SMOKE_STTY_SAVED}" < /dev/tty 2>/dev/null || true
  fi
  SMOKE_STTY_SAVED=""
}

# Consume Ctrl+R ourselves. The default tty "rprnt" character is also Ctrl+R
# and reprints as a literal ^R; a SIGINFO rebind does not work in Cursor's
# terminal, so we disable rprnt/echo and read the byte from /dev/tty.
smoke_prepare_tty() {
  [[ "${SMOKE_DETAIL}" == "1" ]] && return 0
  smoke_tty_usable || return 0
  SMOKE_STTY_SAVED="$(stty -g < /dev/tty 2>/dev/null || true)"
  [[ -n "${SMOKE_STTY_SAVED}" ]] || return 0
  stty -echo -echoctl < /dev/tty 2>/dev/null || true
  stty rprnt undef < /dev/tty 2>/dev/null \
    || stty rprnt ^- < /dev/tty 2>/dev/null \
    || true
  trap 'smoke_on_int' INT
}

smoke_on_int() {
  trap - INT
  smoke_stop_detail_tail
  if [[ -n "${STEP_PID:-}" ]]; then
    kill "${STEP_PID}" 2>/dev/null || true
    wait "${STEP_PID}" 2>/dev/null || true
  fi
  exit 130
}

smoke_toggle_detail() {
  if [[ "${SMOKE_DETAIL_LIVE}" == "1" ]]; then
    SMOKE_DETAIL_LIVE=0
    _UI_COLLAPSE_BODY=1
    smoke_stop_detail_tail
    info "command output hidden — Ctrl+R to show"
    return 0
  fi
  SMOKE_DETAIL_LIVE=1
  _UI_COLLAPSE_BODY=0
  info "command output shown — Ctrl+R to hide"
  if smoke_tty_usable && [[ -f "${LAST_STEP_LOG}" ]]; then
    tail -n 80 -f "${LAST_STEP_LOG}" >/dev/tty 2>/dev/null &
    SMOKE_DETAIL_TAIL_PID=$!
  fi
}

# Non-blocking-enough poll: bash 3.2 read -t is whole seconds. Timeout must
# not trip the ERR trap. Swallow the key so it never echoes as ^R.
smoke_poll_detail_key() {
  local key=""
  smoke_tty_usable || return 0
  read -t 1 -n 1 -s key < /dev/tty || true
  if [[ "${key}" == $'\x12' ]]; then
    smoke_toggle_detail
  fi
}

smoke_wait_collapsed_step() {
  local wrapper=$1
  if ! smoke_tty_usable; then
    wait "${wrapper}" || true
    return 0
  fi
  while [[ ! -f "${STEP_RC_FILE}" ]]; do
    smoke_poll_detail_key
  done
  wait "${wrapper}" 2>/dev/null || true
}

# Always stop the run, print the error, tear down VMs (unless opted out), and exit.
fail_shutdown() {
  local name=$1
  local elapsed=$2
  local detail=$3

  smoke_stop_detail_tail
  smoke_restore_tty
  _UI_COLLAPSE_BODY=0
  # Prevent ERR trap recursion while we are already shutting down.
  trap - ERR
  ABORTING=1

  record "${name}" "FAIL" "${elapsed}" "$(step_fail_summary "${detail}")"
  print_failure_banner "${name}" "${detail}"
  print_results_table

  if [[ "${CLEANUP_DONE}" == "1" ]]; then
    exit 1
  fi
  CLEANUP_DONE=1

  if [[ "${KEEP_VMS}" == "1" || "${KEEP_VMS_ON_FAIL}" == "1" ]]; then
    warn "keeping VMs for debugging (KEEP_VMS/KEEP_VMS_ON_FAIL=1)"
  elif [[ "${CLUSTER_STARTED}" == "1" ]]; then
    destroy_all_vms
  elif [[ "${BUILDER_STARTED}" == "1" ]]; then
    info "builder unit-test/pre-cluster failure: keeping builder, not destroying cluster nodes (none started)"
  else
    info "pre-cluster failure: not destroying VMs"
  fi
  exit 1
}

run_step() {
  local name=$1
  shift
  local start end elapsed rc=0
  info "STEP: ${name}"
  start="$(date +%s)"
  : > "${LAST_STEP_LOG}"

  # Subshell keeps errexit on for the step. Command output goes to the step
  # log (and the run log). Default is collapsed: the terminal only shows
  # STEP/STEP OK/WARN; the final report tables are printed after all steps.
  # SMOKE_DETAIL=1 streams chatter live. Ctrl+R toggles a tail of the step log.
  set +e
  if [[ "${SMOKE_DETAIL}" == "1" ]]; then
    (
      set -euo pipefail
      "$@"
    ) 2>&1 | tee -a "${LAST_STEP_LOG}" "${SMOKE_RUN_LOG}"
    rc=${PIPESTATUS[0]}
  else
    rm -f "${STEP_RC_FILE}"
    (
      set +e
      (
        set -euo pipefail
        "$@"
      ) </dev/null 2>&1 | tee -a "${LAST_STEP_LOG}" "${SMOKE_RUN_LOG}" >/dev/null
      echo "${PIPESTATUS[0]}" > "${STEP_RC_FILE}.tmp"
      mv "${STEP_RC_FILE}.tmp" "${STEP_RC_FILE}"
    ) &
    STEP_PID=$!
    smoke_wait_collapsed_step "${STEP_PID}"
    STEP_PID=""
    rc="$(cat "${STEP_RC_FILE}" 2>/dev/null || echo 1)"
  fi
  set -e

  end="$(date +%s)"
  elapsed=$((end - start))
  if [[ ${rc} -ne 0 ]]; then
    local detail
    detail="exit ${rc}"$'\n'"$(tail -n 40 "${LAST_STEP_LOG}" 2>/dev/null || true)"
    fail_shutdown "${name}" "${elapsed}" "${detail}"
  fi
  # Never advance to the next step without an explicit PASS record.
  record "${name}" "PASS" "${elapsed}" "$(step_log_summary)"
  ok "STEP OK: ${name} (${elapsed}s) — continuing"
}

node_ssh() {
  local node=$1
  shift
  node_ssh_exec "${node}" "$@"
}

# Direct ssh via a cached `vagrant ssh-config`. Concurrent `vagrant ssh`
# serializes on a per-machine lock (Vagrant 2.4.x), which races the overlay
# watcher against bootstrap ("Vagrant can't use the requested machine because
# it is locked").
node_ssh_config_path() {
  echo "${ROOT}/.vagrant-upgrade-smoke-tmp/ssh-${1}.config"
}

vagrant_vm_running() {
  local node=$1
  vagrant status "${node}" 2>/dev/null | grep -qE "^${node}[[:space:]]+running"
}

# SKIP_VAGRANT_UP=1 only works when that VM is already up. A failed smoke
# destroys VMs unless KEEP_VMS_ON_FAIL=1 (KEEP_BUILDER only applies on success).
require_vm_running_for_skip() {
  local node=$1
  if vagrant_vm_running "${node}"; then
    return 0
  fi
  echo "SKIP_VAGRANT_UP=1 but ${node} is not running." >&2
  echo "The previous smoke likely destroyed VMs (KEEP_VMS_ON_FAIL default is destroy; KEEP_BUILDER only keeps the builder on success)." >&2
  echo "Re-run without SKIP_VAGRANT_UP=1. SKIP_BUILD=1 is fine if build/release/flynn-\${BUILD_VERSION}.tar.gz exists." >&2
  return 1
}

cache_node_ssh_config() {
  local node=$1
  local cfg tmp i
  cfg="$(node_ssh_config_path "${node}")"
  tmp="${cfg}.tmp"
  mkdir -p "$(dirname "${cfg}")"
  for i in $(seq 1 30); do
    if vagrant ssh-config "${node}" > "${tmp}" 2>/dev/null && [[ -s "${tmp}" ]]; then
      mv "${tmp}" "${cfg}"
      return 0
    fi
    sleep 1
  done
  rm -f "${tmp}"
  echo "failed to read vagrant ssh-config for ${node}" >&2
  if ! vagrant_vm_running "${node}"; then
    echo "hint: ${node} is not running; omit SKIP_VAGRANT_UP=1 or run: vagrant up ${node}" >&2
  fi
  return 1
}

cache_all_node_ssh_configs() {
  local n
  for n in builder "${NODES[@]}"; do
    cache_node_ssh_config "${n}" || true
  done
}

node_ssh_exec() {
  local node=$1
  shift
  local cfg
  cfg="$(node_ssh_config_path "${node}")"
  if [[ ! -s "${cfg}" ]]; then
    cache_node_ssh_config "${node}"
    cfg="$(node_ssh_config_path "${node}")"
  fi
  if [[ "${NODE_SSH_FORCE_TTY:-0}" == "1" ]]; then
    ssh -F "${cfg}" -o BatchMode=yes -tt "${node}" -- "$@"
  else
    ssh -F "${cfg}" -o BatchMode=yes "${node}" -- "$@"
  fi
}

# Run a bash script as root on a node. Writes stdin to a synced-folder temp
# script so remote failures reliably propagate (vagrant ssh + sudo bash -s
# can otherwise mask exit codes).
#
# Each invocation uses a unique script/status pair so concurrent callers
# (e.g. bootstrap + overlay watcher) cannot clobber each other. SSH itself
# is direct (not `vagrant ssh`) so those callers are not serialized on
# Vagrant's machine lock.
node_root_script() {
  local node=$1
  local tmp_dir="${ROOT}/.vagrant-upgrade-smoke-tmp"
  local id script_name status_name host_script host_status vm_script vm_status
  local ssh_rc=0 remote_rc="" i

  mkdir -p "${tmp_dir}"
  # Include a sequence so two calls in the same second never collide.
  REMOTE_SCRIPT_SEQ=$(( ${REMOTE_SCRIPT_SEQ:-0} + 1 ))
  # Subshell pid (not $$) so background subshells (overlay watcher) get distinct
  # ids. BASHPID needs bash>=4; macOS /bin/bash is 3.2, so fall back to $PPID of
  # a child, which is unique per call.
  local pid="${BASHPID:-$(sh -c 'echo $PPID')}"
  id="${node}.${pid}.${REMOTE_SCRIPT_SEQ}.${RANDOM}.$(date +%s)"
  script_name="remote-${id}.sh"
  status_name="remote-${id}.status"
  host_script="${tmp_dir}/${script_name}"
  host_status="${tmp_dir}/${status_name}"
  vm_script="${REPO_IN_VM}/.vagrant-upgrade-smoke-tmp/${script_name}"
  vm_status="${REPO_IN_VM}/.vagrant-upgrade-smoke-tmp/${status_name}"

  rm -f "${host_status}"
  cat > "${host_script}"
  chmod +x "${host_script}"

  # Always persist the remote exit code, even when the script fails.
  set +e
  node_ssh_exec "${node}" "sudo bash -c 'bash \"${vm_script}\"; ec=\$?; echo \$ec > \"${vm_status}\"; exit \$ec'"
  ssh_rc=$?
  set -e

  remote_rc=""
  for i in $(seq 1 50); do
    if [[ -f "${host_status}" ]]; then
      remote_rc="$(tr -d '[:space:]' < "${host_status}")"
      break
    fi
    sleep 0.2
  done

  rm -f "${host_script}" "${host_status}"

  if [[ "${remote_rc}" =~ ^[0-9]+$ ]]; then
    return "${remote_rc}"
  fi
  echo "could not read remote exit status for ${node} id=${id} (ssh exit ${ssh_rc})" >&2
  if [[ ${ssh_rc} -ne 0 ]]; then
    return "${ssh_rc}"
  fi
  return 1
}

wait_for() {
  local desc=$1
  local timeout=$2
  shift 2
  local deadline=$(( $(date +%s) + timeout ))
  while true; do
    if "$@" >/dev/null 2>&1; then
      return 0
    fi
    if (( $(date +%s) >= deadline )); then
      echo "timed out waiting for ${desc} after ${timeout}s" >&2
      return 1
    fi
    sleep 5
  done
}

default_build_version() {
  local date_prefix iteration latest
  date_prefix="v$(date +%Y%m%d)"
  latest="$(git tag -l "${date_prefix}.*" 2>/dev/null | sort -V | tail -n1 || true)"
  if [[ -n "${latest}" ]]; then
    iteration="${latest##*.}"
    echo "${date_prefix}.$((iteration + 1))-smoke"
  else
    echo "${date_prefix}.0-smoke"
  fi
}

tarball_host_path() {
  echo "${ROOT}/build/release/flynn-${BUILD_VERSION}.tar.gz"
}

tarball_vm_path() {
  echo "${REPO_IN_VM}/build/release/flynn-${BUILD_VERSION}.tar.gz"
}

# Cluster backup lives on the synced folder (survives --clean) and a copy in
# /tmp on node1 (fast local read during bootstrap --from-backup).
backup_host_path() {
  echo "${ROOT}/build/release/smoke-backup-${TOPOLOGY_LABEL}.tar"
}

backup_vm_path() {
  echo "${REPO_IN_VM}/build/release/smoke-backup-${TOPOLOGY_LABEL}.tar"
}

backup_restore_path() {
  echo "/tmp/flynn-smoke-backup.tar"
}

resolve_built_tarball() {
  local path
  path="$(tarball_host_path)"
  if [[ ! -f "${path}" ]]; then
    echo "built tarball not found on host (synced folder): ${path}" >&2
    return 1
  fi
  BUILT_TARBALL="${path}"
  echo "${path}"
}

configure_node_dns() {
  local node=$1
  local marker="# flynn-upgrade-smoke ${CLUSTER_DOMAIN}"
  local hosts_body=""
  local i ip
  for i in "${!NODE_IPS[@]}"; do
    ip="${NODE_IPS[$i]}"
    if [[ "${i}" -eq 0 ]]; then
      hosts_body+="${ip} ${CLUSTER_DOMAIN} controller.${CLUSTER_DOMAIN} git.${CLUSTER_DOMAIN} images.${CLUSTER_DOMAIN} dashboard.${CLUSTER_DOMAIN} status.${CLUSTER_DOMAIN} ${APP_NAME}.${CLUSTER_DOMAIN} ${DOCKER_APP_NAME}.${CLUSTER_DOMAIN}"$'\n'
    else
      hosts_body+="${ip} ${CLUSTER_DOMAIN}"$'\n'
    fi
  done
  node_root_script "${node}" <<EOF
set -euo pipefail
if ! grep -qF "${marker}" /etc/hosts; then
  cat >> /etc/hosts <<'HOSTS'
${marker}
${hosts_body}HOSTS
fi
EOF
}

# Host HTTP API is up before discoverd; discoverd (:1111) only starts during bootstrap.
host_api_up() {
  local ip=$1
  curl -fsS --connect-timeout 2 --max-time 5 "http://${ip}:1113/host/status" >/dev/null
}

hosts_api_ready() {
  local ip
  for ip in "${NODE_IPS[@]}"; do
    host_api_up "${ip}" || return 1
  done
  return 0
}

dump_layer0_diagnostics() {
  local node
  for node in "${NODES[@]}"; do
    info "layer-0 diagnostics on ${node}"
    node_root_script "${node}" <<'EOF' || true
set +e
echo "--- systemctl status flynn-host ---"
systemctl status flynn-host.service --no-pager -l | tail -n 40
echo "--- host.json ---"
cat /etc/flynn/host.json 2>/dev/null || true
echo "--- flynn-host.log (tail) ---"
tail -n 80 /var/log/flynn/flynn-host.log 2>/dev/null || true
echo "--- listening 1113 ---"
ss -lntp 2>/dev/null | grep -E ':1113\b' || netstat -lntp 2>/dev/null | grep -E ':1113\b' || true
EOF
  done
}

# Wipe synced ./flynn-logs/{builder,node*} so each smoke run starts with empty
# logs. KEEP_LOGS=1 preserves prior run output for comparison.
clear_shared_logs() {
  local name dir
  if [[ "${KEEP_LOGS}" == "1" ]]; then
    info "keeping shared logs (KEEP_LOGS=1)"
    return 0
  fi
  info "clearing shared flynn-logs for a fresh run"
  for name in "${SHARED_LOG_DIRS[@]}"; do
    dir="${ROOT}/flynn-logs/${name}"
    mkdir -p "${dir}"
    # Remove everything under the synced log dir. flynn-host recreates
    # flynn-host.log and per-app UUID logs on the next run.
    find "${dir}" -mindepth 1 -maxdepth 1 -exec rm -rf {} + 2>/dev/null || true
  done
  echo "shared logs cleared under ${ROOT}/flynn-logs/"
}

# Between topologies, drop cluster-node logs so 1-node and 3-node output do
# not mix. Builder logs stay (same VM / same build).
clear_cluster_shared_logs() {
  local name dir
  if [[ "${KEEP_LOGS}" == "1" ]]; then
    return 0
  fi
  for name in "${NODES[@]}"; do
    dir="${ROOT}/flynn-logs/${name}"
    mkdir -p "${dir}"
    find "${dir}" -mindepth 1 -maxdepth 1 -exec rm -rf {} + 2>/dev/null || true
  done
}

# Collect current flynnbr0 gateway IPs (…1 on each host subnet) from all nodes.
overlay_bridge_ips() {
  local node ip
  for node in "${NODES[@]}"; do
    ip="$(
      node_root_script "${node}" <<'EOF' 2>/dev/null || true
set +e
ip -4 -o addr show flynnbr0 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -1
EOF
    )"
    ip="$(echo "${ip}" | tr -d '[:space:]')"
    if [[ "${ip}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      echo "${ip}"
    fi
  done
}

dump_overlay_diagnostics() {
  local node bridges bridge
  info "overlay / postgres diagnostics (cross-node flannel)"
  bridges="$(overlay_bridge_ips | sort -u | tr '\n' ' ')"
  info "discovered flynnbr0 addresses: ${bridges:-none}"
  for node in "${NODES[@]}"; do
    node_root_script "${node}" <<EOF || true
set +e
echo "=== \$(hostname) ==="
ip -br addr show flannel.1 flynnbr0 eth1 2>/dev/null || true
ip -4 addr show flannel.1 2>/dev/null || true
ip route | grep -E '100\.|flannel' || true
echo "-- iptables NAT POSTROUTING (flynn/flannel) --"
iptables -t nat -L POSTROUTING -n -v 2>/dev/null | head -n 12 || true
echo "-- VTEP MAC: device vs lease (must match; udev MACAddressPolicy can rewrite it) --"
dev_mac="\$(cat /sys/class/net/flannel.1/address 2>/dev/null || true)"
echo "device flannel.1 mac=\${dev_mac:-none} addr_assign_type=\$(cat /sys/class/net/flannel.1/addr_assign_type 2>/dev/null || echo n/a)"
curl -fsS --max-time 3 http://127.0.0.1:1111/services/flannel/meta 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin).get("data",{})
for sn,a in sorted((d.get("subnets") or {}).items()):
    print("lease", sn, a.get("PublicIP"), (a.get("BackendData") or {}).get("VtepMAC"))
' 2>/dev/null || echo "no flannel lease meta"
echo "-- fdb / neigh on flannel.1 --"
bridge fdb show dev flannel.1 2>/dev/null || true
ip neigh show dev flannel.1 2>/dev/null || true
echo "-- ping peer bridges --"
for ip in ${bridges}; do
  ping -c1 -W2 "\$ip" >/dev/null 2>&1 && echo "ok \$ip" || echo "FAIL \$ip"
done
echo "-- postgres meta --"
curl -fsS --max-time 3 http://127.0.0.1:1111/services/postgres/meta 2>&1 | head -c 500; echo
echo "-- flynn-host --"
systemctl is-active flynn-host.service 2>/dev/null || true
command -v ipset >/dev/null && echo "ipset=\$(command -v ipset)" || echo "ipset=MISSING"
journalctl -u flynn-host.service -n 40 --no-pager 2>/dev/null || true
grep -E 'error configuring network|error enabling job isolation|ipset not found' /var/log/flynn/flynn-host.log 2>/dev/null | tail -n 20 || true
EOF
  done
}

flannel_up_all() {
  local node
  for node in "${NODES[@]}"; do
    node_root_script "${node}" <<'EOF' >/dev/null 2>&1
set -euo pipefail
ip link show flannel.1 >/dev/null 2>&1
ip -4 addr show flynnbr0 2>/dev/null | grep -q 'inet '
EOF
  done
}

# Cross-node overlay sanity: each node must ping every other flynnbr0 .1 address.
overlay_peers_reachable() {
  local bridges bridge_list node
  bridges="$(overlay_bridge_ips | sort -u)"
  bridge_list="$(echo "${bridges}" | tr '\n' ' ')"
  # Need one bridge IP per cluster node.
  if [[ "$(echo "${bridges}" | grep -c .)" -lt "${#NODES[@]}" ]]; then
    echo "overlay: expected ${#NODES[@]} flynnbr0 addresses, got: ${bridge_list:-none}" >&2
    return 1
  fi
  for node in "${NODES[@]}"; do
    node_root_script "${node}" <<EOF
set -euo pipefail
failed=0
for ip in ${bridge_list}; do
  if ! ping -c1 -W2 "\$ip" >/dev/null 2>&1; then
    echo "overlay: \$(hostname) cannot ping \$ip" >&2
    failed=1
  fi
done
exit \$failed
EOF
  done
  echo "overlay peer bridges reachable: ${bridge_list}"
}

# After flannel is up, fail fast if overlay is broken (do not wait ~5m for postgres).
watch_overlay_during_bootstrap() {
  local fail_file=$1
  local deadline=$(( $(date +%s) + 240 ))
  while (( $(date +%s) < deadline )); do
    if flannel_up_all >/dev/null 2>&1; then
      break
    fi
    sleep 5
  done
  if ! flannel_up_all >/dev/null 2>&1; then
    echo "overlay watch: flannel/flynnbr0 not up within 240s" > "${fail_file}"
    return 1
  fi
  # ConfigureNetworking creates flynnbr0 then EnableJobIsolation; missing ipset
  # fatals flynn-host while the bridge is already up (wait-hosts then burns 10m).
  local node
  for node in "${NODES[@]}"; do
    if ! node_root_script "${node}" <<'EOF' >/dev/null 2>&1
set -euo pipefail
systemctl is-active --quiet flynn-host.service
EOF
    then
      echo "overlay watch: flynn-host died on ${node} after flynnbr0 came up (often missing ipset)" > "${fail_file}"
      dump_overlay_diagnostics >>"${LAST_STEP_LOG}" 2>&1 || true
      node_root_script node1 <<'EOF' >/dev/null 2>&1 || true
pkill -f 'flynn-host bootstrap' || true
EOF
      return 1
    fi
  done
  # Allow remote subnet routes / FDB entries to settle, then require reachability.
  sleep 20
  for node in "${NODES[@]}"; do
    if ! node_root_script "${node}" <<'EOF' >/dev/null 2>&1
set -euo pipefail
systemctl is-active --quiet flynn-host.service
EOF
    then
      echo "overlay watch: flynn-host died on ${node} after overlay settle (often missing ipset)" > "${fail_file}"
      dump_overlay_diagnostics >>"${LAST_STEP_LOG}" 2>&1 || true
      node_root_script node1 <<'EOF' >/dev/null 2>&1 || true
pkill -f 'flynn-host bootstrap' || true
EOF
      return 1
    fi
  done
  if ! overlay_peers_reachable >>"${LAST_STEP_LOG}" 2>&1; then
    dump_overlay_diagnostics >>"${LAST_STEP_LOG}" 2>&1 || true
    echo "overlay peer bridges unreachable after flannel up" > "${fail_file}"
    # Stop the hung bootstrap so run_step fails promptly.
    node_root_script node1 <<'EOF' >/dev/null 2>&1 || true
pkill -f 'flynn-host bootstrap' || true
EOF
    return 1
  fi
  return 0
}

verify_nic_promisc() {
  local node id line
  for node in "${NODES[@]}"; do
    id="$(cat "${ROOT}/.vagrant/machines/${node}/virtualbox/id" 2>/dev/null || true)"
    if [[ -z "${id}" ]]; then
      echo "missing VirtualBox id for ${node}" >&2
      return 1
    fi
    line="$(VBoxManage showvminfo "${id}" --machinereadable 2>/dev/null | grep -E '^nicpromisc2=' || true)"
    if [[ -z "${line}" ]]; then
      line="$(VBoxManage showvminfo "${id}" 2>/dev/null | grep -E '^NIC 2:' || true)"
    fi
    if ! echo "${line}" | grep -qiE 'allow-all|Allow All'; then
      echo "${node} NIC2 not promiscuous allow-all: ${line:-unknown}" >&2
      echo "hint: Vagrantfile sets --nicpromisc2 allow-all; recreate VMs or run:" >&2
      echo "  VBoxManage controlvm ${id} nicpromisc2 allow-all" >&2
      return 1
    fi
    echo "${node}: NIC2 promiscuous ok (${line})"
  done
}

# Appliance HTTP status is on the DB port+1 and only reachable on the overlay,
# so these checks always run on node1. Service names: postgres, mariadb, mongodb.
sirenia_primary_read_write() {
  local service=$1
  node_root_script node1 <<EOF
set -euo pipefail
export SIRENIA_SERVICE="${service}"
python3 - <<'PY'
import json, os, sys, urllib.request

def get(url):
    with urllib.request.urlopen(url, timeout=5) as r:
        return json.load(r)

service = os.environ["SIRENIA_SERVICE"]
meta = get("http://127.0.0.1:1111/services/%s/meta" % service)
data = meta.get("data", meta)
state = json.loads(data) if isinstance(data, str) else data
primary = state.get("primary") or {}
addr = primary.get("addr") or primary.get("Addr") or ""
if not addr:
    sys.exit("no %s primary in meta: %s" % (service, json.dumps(state)[:300]))
host, port = addr.rsplit(":", 1)
status = get("http://%s:%d/status" % (host, int(port) + 1))
db = status.get("database") or {}
if not (db.get("running") and db.get("read_write")):
    sys.exit("%s primary %s not read-write: %s" % (service, addr, json.dumps(status)[:300]))
print("%s primary %s running read-write (generation %s)" % (service, addr, state.get("generation")))
PY
EOF
}

postgres_is_read_write() { sirenia_primary_read_write postgres; }
mariadb_is_read_write() { sirenia_primary_read_write mariadb; }
mongodb_is_read_write() { sirenia_primary_read_write mongodb; }

redis_is_ready() {
  flynn1 -a "${APP_NAME}" redis redis-cli PING | grep -qi PONG
}

# Kafka/ClickHouse volume data is not in flynn cluster backup. After restore
# the brokers come back empty; these only prove the CLI/engine is up.
kafka_is_ready() {
  flynn1 -a "${APP_NAME}" kafka topics >/dev/null 2>&1
}

clickhouse_ping() {
  local out
  out="$(flynn1 -a "${APP_NAME}" clickhouse client -- --query "SELECT 1" 2>/dev/null || true)"
  [[ "$(echo "${out}" | tr -d '[:space:]')" == "1" ]]
}

# ClickHouse seed uses local MergeTree plus HTTP replica fan-out. After a node
# join/drain the CLI can briefly return CH "unknown_error" instead of a count;
# do not let that abort the step under set -e.
clickhouse_row_count() {
  local out
  out="$(flynn1 -a "${APP_NAME}" clickhouse client -- --query "SELECT count() FROM smoke_db.rows" 2>/dev/null || true)"
  numeric_count "${out}"
}

clickhouse_seed_ready() {
  local count
  count="$(clickhouse_row_count)"
  [[ -n "${count}" && "${count}" -ge "${SMOKE_SEED_ROWS}" ]]
}

# Sirenia appliances that exist as discoverd services. postgres is scaled during
# bootstrap; mariadb/mongodb stay at 0 processes until the first resource add.
# Redis is not sirenia — PING via the app's REDIS_URL after it is provisioned.
wait_datastores_ready() {
  local suffix=$1
  shift
  local svc
  local services=("$@")
  if [[ ${#services[@]} -eq 0 ]]; then
    echo "wait_datastores_ready: no services given" >&2
    return 1
  fi
  for svc in "${services[@]}"; do
    case "${svc}" in
      postgres)
        wait_for "postgres read-write ${suffix}" 900 postgres_is_read_write || return 1
        ;;
      mariadb|mysql)
        wait_for "mariadb read-write ${suffix}" 600 mariadb_is_read_write || return 1
        ;;
      mongodb)
        wait_for "mongodb read-write ${suffix}" 600 mongodb_is_read_write || return 1
        ;;
      redis)
        wait_for "redis PING ${suffix}" 180 redis_is_ready || return 1
        ;;
      kafka)
        wait_for "kafka topics ${suffix}" 180 kafka_is_ready || return 1
        ;;
      clickhouse)
        wait_for "clickhouse smoke_db.rows ${suffix}" 300 clickhouse_seed_ready || return 1
        ;;
      clickhouse-ping)
        wait_for "clickhouse SELECT 1 ${suffix}" 180 clickhouse_ping || return 1
        ;;
      *)
        echo "wait_datastores_ready: unknown service ${svc}" >&2
        return 1
        ;;
    esac
  done
}

ensure_flynn_cli_on_node1() {
  # Prefer the synced local CLI even when /usr/local/bin/flynn already works.
  # SKIP_BUILD leaves the tarball CLI in place; overlaying build/bin picks up
  # CLI fixes (e.g. redis-cli leader DNS) without rebuilding images.
  info "installing Flynn CLI on node1"
  node_root_script node1 <<EOF
set -euo pipefail
case "\$(uname -m)" in
  x86_64)          cli_arch=amd64 ;;
  aarch64|arm64)   cli_arch=arm64 ;;
  i386|i686)       cli_arch=386 ;;
  *) echo "unsupported node arch \$(uname -m)" >&2; exit 1 ;;
esac
src="${REPO_IN_VM}/build/bin/flynn-linux-\${cli_arch}"
if [[ -x "\${src}" ]]; then
  echo "installing \${src} (node arch \$(uname -m))"
  install -m 0755 "\${src}" /usr/local/bin/flynn
elif flynn version >/dev/null 2>&1; then
  echo "keeping existing flynn CLI (\${src} missing)"
else
  echo "no local CLI for \${cli_arch}; using installer from ${CLI_REPO}"
  curl -fsSL "https://raw.githubusercontent.com/${CLI_REPO}/main/script/install-flynn-cli" \
    | bash -s -- -r "${CLI_REPO}"
fi
command -v flynn
# Must actually run on this machine; a wrong-arch binary fails here.
flynn version
EOF
}

# Insert -f so a leftover ~/.flynnrc from a previous bootstrap (different TLS
# pin after --clean + re-bootstrap) is replaced instead of silently reused.
# Skipping the add when "default" already exists caused:
#   pinned: the peer leaf certificate did not match the provided pin
force_cluster_add_cmd() {
  local cmd=$1
  case "${cmd}" in
    *' cluster add -f '*|*' cluster add --force '*) echo "${cmd}" ;;
    *) echo "${cmd/flynn cluster add /flynn cluster add -f }" ;;
  esac
}

# Register the bootstrapped cluster with the CLI on node1. Always force-add
# (see force_cluster_add_cmd) then probe the controller so a stale pin fails
# here instead of later at git push / flynn create.
register_cli_cluster() {
  local add_cmd
  add_cmd="$(
    node_root_script node1 <<'EOF' | tee /dev/stderr | grep -E '^flynn cluster add ' | tail -1
set -euo pipefail
flynn-host cli-add-command
EOF
  )"
  if [[ -z "${add_cmd}" && -f "${WORK_DIR}/bootstrap.log" ]]; then
    add_cmd="$(grep -Eo 'flynn cluster add.*' "${WORK_DIR}/bootstrap.log" | tail -1 || true)"
  fi
  if [[ -z "${add_cmd}" ]]; then
    echo "could not find 'flynn cluster add' command after bootstrap" >&2
    return 1
  fi
  add_cmd="$(force_cluster_add_cmd "${add_cmd}")"
  node_root_script node1 <<EOF
set -euo pipefail
${add_cmd}
flynn cluster | grep -q '^default[[:space:]]'
# Controller TLS pin must match; a stale pin from a prior bootstrap fails here.
flynn apps >/dev/null
echo "CLI cluster 'default' registered and controller reachable"
EOF
}

flynn1() {
  local args_q
  args_q="$(printf '%q ' "$@")"
  # Close stdin. `flynn run` attaches it to the job, and clickhouse-client
  # INSERT VALUES (and mongo shells without --eval) wait forever on a TTY.
  node_ssh node1 "sudo -H flynn ${args_q}" </dev/null
}

cluster_host_count() {
  node_ssh node1 'sudo flynn-host list' </dev/null | awk 'NR>1 && NF>=2 {c++} END{print c+0}'
}

hosts_listed_at_least() {
  local want=$1
  local n
  n="$(cluster_host_count)"
  [[ "${n}" -ge "${want}" ]]
}

hosts_listed_exactly() {
  local want=$1
  local n
  n="$(cluster_host_count)"
  [[ "${n}" -eq "${want}" ]]
}

node_addr_listed() {
  local ip=$1
  node_ssh node1 'sudo flynn-host list' </dev/null | grep -F "${ip}"
}

host_addr_gone() {
  local ip=$1
  ! node_addr_listed "${ip}" >/dev/null 2>&1
}

# flynn-host systemd uses KillMode=process so `systemctl stop` leaves containers
# running (needed for non-destructive updater restarts). A real node drain must
# DELETE jobs via the local host API while the daemon is still up.
drain_host_jobs() {
  local node=$1
  info "draining jobs on ${node} via local host API"
  node_root_script "${node}" <<'EOF'
set -euo pipefail
python3 - <<'PY'
import json, sys, urllib.error, urllib.parse, urllib.request

def load_key():
    try:
        with open("/etc/flynn/host.json") as f:
            env = (json.load(f) or {}).get("env") or {}
        return env.get("FLYNN_HOST_AUTH_KEY") or ""
    except FileNotFoundError:
        return ""

def req(method, path, key):
    url = "http://127.0.0.1:1113" + path
    r = urllib.request.Request(url, method=method)
    if key:
        r.add_header("Auth-Key", key)
    try:
        with urllib.request.urlopen(r, timeout=60) as resp:
            body = resp.read()
            return resp.status, body
    except urllib.error.HTTPError as e:
        return e.code, e.read()

def list_active(key):
    code, body = req("GET", "/host/jobs?active=true", key)
    if code != 200:
        sys.exit("list jobs failed HTTP %s: %s" % (code, body[:300]))
    data = json.loads(body.decode() or "{}")
    if isinstance(data, dict):
        return data
    if isinstance(data, list):
        out = {}
        for job in data:
            jid = ((job.get("job") or {}).get("id")) or job.get("id")
            if jid:
                out[jid] = job
        return out
    sys.exit("unexpected /host/jobs payload: %s" % type(data).__name__)

def is_discoverd(job):
    j = job.get("job") or {}
    md = j.get("metadata") or {}
    name = (md.get("flynn-controller.app_name") or "").lower()
    args = " ".join((j.get("config") or {}).get("args") or [])
    blob = " ".join([name, args, j.get("id") or ""])
    return "discoverd" in blob

def stop_one(jid, key):
    path = "/host/jobs/" + urllib.parse.quote(jid, safe="")
    code, body = req("DELETE", path, key)
    if code in (200, 404):
        print("stopped %s (HTTP %s)" % (jid, code))
        return
    print("stop %s failed HTTP %s: %s" % (jid, code, body[:200]), file=sys.stderr)

import time
key = load_key()
stopped = 0
left = {}
for _pass in range(5):
    jobs = list_active(key)
    if not jobs:
        left = {}
        break
    first = [(jid, job) for jid, job in jobs.items() if not is_discoverd(job)]
    last = [(jid, job) for jid, job in jobs.items() if is_discoverd(job)]
    for jid, _job in first + last:
        stop_one(jid, key)
        stopped += 1
    time.sleep(1)
    left = list_active(key)
if left:
    print("jobs still active after drain (scheduler may have replaced them; daemon stop + kill leftovers next): %s" % ",".join(sorted(left)))
else:
    print("drained %d jobs" % stopped)
PY
EOF
}

# systemd KillMode=process leaves containers running after flynn-host exits.
kill_leftover_containers() {
  local node=$1
  info "killing leftover containers on ${node}"
  node_root_script "${node}" <<'EOF'
set -euo pipefail
pkill -KILL -f '/.containerinit' || true
pkill -KILL -f '^/bin/discoverd' || true
pkill -KILL -f '^/usr/bin/flanneld' || true
sleep 1
leftover="$(pgrep -af '/.containerinit' || true)"
if [[ -n "${leftover}" ]]; then
  echo "containerinit still running:" >&2
  echo "${leftover}" >&2
  exit 1
fi
echo "no leftover containerinit on $(hostname)"
EOF
}

# True when no discoverd instance advertises a job that ran on prefix (e.g. node3).
discoverd_host_jobs_gone() {
  local prefix=$1
  node_root_script node1 <<EOF
set -euo pipefail
export DRAIN_PREFIX="${prefix}"
python3 - <<'PY'
import json, os, sys, urllib.request

prefix = os.environ["DRAIN_PREFIX"] + "-"
services = ("postgres", "mariadb", "mongodb", "redis", "flynn-host")

def get(url):
    with urllib.request.urlopen(url, timeout=5) as r:
        return json.load(r)

leftover = []
for svc in services:
    try:
        insts = get("http://127.0.0.1:1111/services/%s/instances" % svc)
    except Exception as e:
        # Service may not exist yet (or already gone).
        continue
    if not isinstance(insts, list):
        continue
    for inst in insts:
        meta = inst.get("meta") or {}
        jid = meta.get("FLYNN_JOB_ID") or ""
        hid = meta.get("id") or inst.get("id") or ""
        if jid.startswith(prefix) or hid == os.environ["DRAIN_PREFIX"] or str(hid).startswith(prefix):
            leftover.append("%s:%s" % (svc, jid or hid))
if leftover:
    sys.exit("discoverd still has %s jobs: %s" % (os.environ["DRAIN_PREFIX"], ",".join(leftover)))
print("discoverd has no %s jobs" % os.environ["DRAIN_PREFIX"])
PY
EOF
}

sirenia_primary_not_on_host() {
  local service=$1
  local prefix=$2
  node_root_script node1 <<EOF
set -euo pipefail
export SIRENIA_SERVICE="${service}"
export DRAIN_PREFIX="${prefix}"
python3 - <<'PY'
import json, os, sys, urllib.request

def get(url):
    with urllib.request.urlopen(url, timeout=5) as r:
        return json.load(r)

service = os.environ["SIRENIA_SERVICE"]
prefix = os.environ["DRAIN_PREFIX"] + "-"
meta = get("http://127.0.0.1:1111/services/%s/meta" % service)
data = meta.get("data", meta)
state = json.loads(data) if isinstance(data, str) else data
primary = state.get("primary") or {}
job_id = (primary.get("meta") or {}).get("FLYNN_JOB_ID") or ""
if not job_id:
    sys.exit("no %s primary job id in meta" % service)
if job_id.startswith(prefix):
    sys.exit("%s primary still on %s: %s" % (service, os.environ["DRAIN_PREFIX"], job_id))
print("%s primary %s" % (service, job_id))
PY
EOF
}

sync_peer_ips_from_nodes() {
  local saved_ifs="${IFS}"
  IFS=,
  PEER_IPS="${NODE_IPS[*]}"
  IFS="${saved_ifs}"
}

append_live_node() {
  local node=$1
  local n="${node#node}"
  local ip i
  for i in "${!NODES[@]}"; do
    if [[ "${NODES[$i]}" == "${node}" ]]; then
      return 0
    fi
  done
  ip="$(cluster_node_ip "${n}")"
  NODES+=("${node}")
  NODE_IPS+=("${ip}")
  sync_peer_ips_from_nodes
}

remember_teardown_node() {
  local node=$1 i
  for i in "${!TEARDOWN_NODES[@]}"; do
    if [[ "${TEARDOWN_NODES[$i]}" == "${node}" ]]; then
      return 0
    fi
  done
  TEARDOWN_NODES+=("${node}")
}

drop_live_node() {
  local drop=$1
  local new_nodes=() new_ips=() i
  for i in "${!NODES[@]}"; do
    if [[ "${NODES[$i]}" != "${drop}" ]]; then
      new_nodes+=("${NODES[$i]}")
      new_ips+=("${NODE_IPS[$i]}")
    fi
  done
  NODES=("${new_nodes[@]}")
  NODE_IPS=("${new_ips[@]}")
  sync_peer_ips_from_nodes
}

# Backup still lists every original host. After drain, NODES is the live
# subset; --clean + bootstrap --from-backup on that subset waits forever
# (3-node-remove 2026-09-13: min-hosts=3, 0 online, no flynnbr0).
restore_drained_inventory() {
  if [[ ${#TEARDOWN_NODES[@]} -le ${#NODES[@]} ]]; then
    return 0
  fi
  info "restore inventory ${NODES[*]} -> ${TEARDOWN_NODES[*]} (backup lists drained hosts)"
  NODES=("${TEARDOWN_NODES[@]}")
  NODE_IPS=()
  local node num
  for node in "${NODES[@]}"; do
    num="${node#node}"
    NODE_IPS+=("$(cluster_node_ip "${num}")")
  done
  sync_peer_ips_from_nodes
  MIN_HOSTS="${#NODES[@]}"
}

step_host_unit_tests() {
  local failed=0 start elapsed rc
  local script pkg name
  local packages=( "${SMOKE_UNIT_PACKAGES[@]}" )

  echo "host unit-test gate: gofmt -s, smoke regressions, Darwin-safe Go packages"
  echo "failures abort before Vagrant up / cluster build"

  echo "==> gofmt (util/commit-validator/validate-gofmt, same as GitHub Actions)"
  start="$(date +%s)"
  if ( cd "${ROOT}" && util/commit-validator/validate-gofmt ); then
    elapsed=$(( $(date +%s) - start ))
    record_unit_check "gofmt" "validate-gofmt" "PASS" "${elapsed}s"
  else
    rc=$?
    elapsed=$(( $(date +%s) - start ))
    record_unit_check "gofmt" "validate-gofmt" "FAIL" "exit ${rc} ${elapsed}s"
    failed=1
  fi

  local smoke_scripts=( "${ROOT}"/script/test-vagrant-smoke-*.sh )
  if [[ ${#smoke_scripts[@]} -eq 0 ]]; then
    echo "no script/test-vagrant-smoke-*.sh found" >&2
    record_unit_check "script" "test-vagrant-smoke-*.sh" "FAIL" "none found"
    return 1
  fi
  for script in "${smoke_scripts[@]}"; do
    name="$(basename "${script}")"
    echo "==> bash script/${name}"
    start="$(date +%s)"
    if bash "${script}"; then
      elapsed=$(( $(date +%s) - start ))
      record_unit_check "script" "${name}" "PASS" "${elapsed}s"
    else
      rc=$?
      elapsed=$(( $(date +%s) - start ))
      record_unit_check "script" "${name}" "FAIL" "exit ${rc} ${elapsed}s"
      failed=1
    fi
  done

  if [[ "$(uname -s)" == "Linux" ]]; then
    packages+=( ./flannel/backend/vxlan/ ./builder/ )
  fi

  for pkg in "${packages[@]}"; do
    echo "==> go test ${pkg}"
    start="$(date +%s)"
    # shellcheck disable=SC2086
    if go test -mod=vendor -count=1 ${FLYNN_GO_TEST_FLAGS:-} "${pkg}"; then
      elapsed=$(( $(date +%s) - start ))
      record_unit_check "go" "${pkg}" "PASS" "${elapsed}s"
    else
      rc=$?
      elapsed=$(( $(date +%s) - start ))
      record_unit_check "go" "${pkg}" "FAIL" "exit ${rc} ${elapsed}s"
      failed=1
    fi
  done

  echo "==> go test test/apps/upgrade-smoke"
  start="$(date +%s)"
  if ( cd "${ROOT}/test/apps/upgrade-smoke" && go test -count=1 ${FLYNN_GO_TEST_FLAGS:-} . ); then
    elapsed=$(( $(date +%s) - start ))
    record_unit_check "go" "test/apps/upgrade-smoke" "PASS" "${elapsed}s"
  else
    rc=$?
    elapsed=$(( $(date +%s) - start ))
    record_unit_check "go" "test/apps/upgrade-smoke" "FAIL" "exit ${rc} ${elapsed}s"
    failed=1
  fi

  if [[ "${failed}" -ne 0 ]]; then
    echo "host unit tests failed; not starting Vagrant cluster" >&2
    return 1
  fi
  echo "host unit tests passed"
}

# Full Linux suite on the builder VM (real kernel: zfs, netlink, redis-server,
# postgres, mariadb, mongodb). Docker Desktop cannot load ZFS, so this is the
# smoke Linux gate — not script/run-unit-tests in Docker.
step_builder_unit_tests() {
  local start elapsed rc
  echo "builder unit-test gate: native Linux suite via script/run-unit-tests"
  echo "builder provides redis, postgresql, mariadb, mongodb, zfsutils, netlink"
  echo "failures abort before booting node1/2/3"

  start="$(date +%s)"
  # Force a remote PTY so pkg/term can open /dev/tty (same reason Docker uses -t).
  if NODE_SSH_FORCE_TTY=1 node_root_script builder <<EOF
set -euo pipefail
export PATH=/usr/local/go/bin:\$PATH
export GOFLAGS="-mod=vendor -buildvcs=false"
export FLYNN_TEST_DOCKER=0
export FLYNN_TEST_SKIP_CHECKS="${FLYNN_TEST_SKIP_CHECKS:-1}"
export PGHOST="\${PGHOST:-/var/run/postgresql}"
export PGSSLMODE="\${PGSSLMODE:-disable}"
cd "${REPO_IN_VM}"
# VirtualBox synced .git is owned by the host UID; git 2.35+ exits 128
# ("dubious ownership") and Go fails with "error obtaining VCS status".
# shellcheck disable=SC1091
source "${REPO_IN_VM}/script/lib/git-safe-dir.sh"
flynn_git_safe_directory "${REPO_IN_VM}"

if [[ ! -x /usr/local/go/bin/go ]]; then
  echo "Go toolchain missing on builder; run: vagrant provision builder" >&2
  exit 1
fi

echo "==> Starting PostgreSQL"
pg_version="\$(ls /usr/lib/postgresql 2>/dev/null | sort -V | tail -n1 || true)"
if [[ -n "\${pg_version}" ]]; then
  pg_ctlcluster "\${pg_version}" main start || service postgresql start || true
else
  service postgresql start || true
fi
sudo -u postgres createuser -s root 2>/dev/null || true

echo "==> Starting MariaDB"
service mariadb start 2>/dev/null || service mysql start 2>/dev/null || true

echo "==> Starting Redis"
service redis-server start 2>/dev/null || service redis start 2>/dev/null || true

echo "==> ZFS"
if modprobe zfs 2>/dev/null && [[ -e /dev/zfs ]] && command -v zpool >/dev/null 2>&1; then
  echo "ZFS kernel module loaded; host/volume tests enabled"
else
  echo "ZFS unavailable; skipping host/volume tests"
  export FLYNN_SKIP_VOLUME_TESTS=1
fi

script/run-unit-tests
EOF
  then
    elapsed=$(( $(date +%s) - start ))
    record_unit_check "builder" "script/run-unit-tests" "PASS" "${elapsed}s"
  else
    rc=$?
    elapsed=$(( $(date +%s) - start ))
    record_unit_check "builder" "script/run-unit-tests" "FAIL" "exit ${rc} ${elapsed}s"
    echo "builder unit tests failed; not starting cluster nodes" >&2
    return 1
  fi
  echo "builder unit tests passed"
}

step_vagrant_up_builder() {
  local builder_mem="${BUILDER_MEMORY:-30000}"
  local builder_cpus="${BUILDER_CPUS:-8}"
  info "starting builder (memory=${builder_mem} cpus=${builder_cpus})"
  VAGRANT_MEMORY="${builder_mem}" VAGRANT_CPUS="${builder_cpus}" vagrant up builder
  echo "builder up"
}

step_vagrant_up_nodes() {
  local node_mem="${VAGRANT_MEMORY:-6144}"
  local node_cpus="${VAGRANT_CPUS:-2}"
  info "starting ${TOPOLOGY_LABEL} nodes (${NODES[*]}) memory=${node_mem} cpus=${node_cpus}"
  VAGRANT_MEMORY="${node_mem}" VAGRANT_CPUS="${node_cpus}" vagrant up "${NODES[@]}"
  info "verifying VirtualBox NIC2 promiscuous mode (required for flannel VXLAN)"
  verify_nic_promisc
  echo "${TOPOLOGY_LABEL} nodes up"
}

builder_has_base_squashfs() {
  node_root_script builder <<'EOF'
set -euo pipefail
test -f /var/lib/flynn/base-layer.squashfs
EOF
}

step_build_on_builder() {
  local phase="${BUILD_PHASE}"
  if [[ "${phase}" == "auto" ]]; then
    if builder_has_base_squashfs; then
      phase="cluster"
      info "found base squashfs on builder; using build.sh phase=cluster"
    else
      phase="all"
      info "no base squashfs on builder; using build.sh phase=all (includes base)"
    fi
  elif [[ "${phase}" == "cluster" ]]; then
    # Refuse to run cluster-only when the base layer is missing.
    if ! builder_has_base_squashfs; then
      warn "BUILD_PHASE=cluster but base squashfs is missing; falling back to phase=all"
      phase="all"
    fi
  fi

  info "building Flynn ${BUILD_VERSION} on builder (phase=${phase}, attempts=${FLYNN_BUILD_ATTEMPTS})"
  node_root_script builder <<EOF
set -euo pipefail
export PATH=/usr/local/go/bin:\$PATH
cd "${REPO_IN_VM}"
# shellcheck disable=SC1091
source "${REPO_IN_VM}/script/lib/git-safe-dir.sh"
flynn_git_safe_directory "${REPO_IN_VM}"
if [[ ! -x /usr/local/go/bin/go ]]; then
  echo "Go toolchain missing on builder; run: vagrant provision builder" >&2
  exit 1
fi

# Job isolation (EnableJobIsolation) needs ipset on this VM, not only in the
# cluster host squashfs. Existing builders skipped the setup.sh package add.
# shellcheck disable=SC1091
source "${REPO_IN_VM}/script/lib/apt-retry.sh"
flynn_apt_install_conf
if ! command -v ipset >/dev/null 2>&1; then
  echo "===> installing ipset on builder (required for flynn-host job isolation)"
  export DEBIAN_FRONTEND=noninteractive
  flynn_apt_cmd install -y ipset
fi

transient_build_failure() {
  [[ -f "\$1" ]] || return 1
  grep -qE 'Failed to fetch|Hash Sum mismatch|Temporary failure resolving|Connection timed out|Could not resolve|Network is unreachable|502 Bad Gateway|503 Service|download.docker.com|Unable to lock directory|Could not get lock|I/O error|Connection reset|TLS handshake|the remote end hung up|Clearing|Splitting up|503  |504  |522 |Couldn.t create temporary file /tmp/apt.conf' "\$1"
}

attempt=1
max="${FLYNN_BUILD_ATTEMPTS}"
mkdir -p "${REPO_IN_VM}/build"
log="${REPO_IN_VM}/build/flynn-build-attempt.log"
while true; do
  echo "===> build attempt \${attempt}/\${max} version=${BUILD_VERSION} phase=${phase}"
  rc=0
  ./build.sh --version "${BUILD_VERSION}" "${phase}" 2>&1 | tee "\${log}" || rc=\$?
  if [[ "\${rc}" -eq 0 ]]; then
    ./script/release --target tarball --version "${BUILD_VERSION}" --output "${REPO_IN_VM}/build/release"
    break
  fi
  if [[ "\${attempt}" -ge "\${max}" ]]; then
    echo "build failed after \${max} attempts" >&2
    exit "\${rc}"
  fi
  if ! transient_build_failure "\${log}"; then
    echo "build failure does not look like apt/network; not retrying" >&2
    exit "\${rc}"
  fi
  echo "===> transient apt/network failure (attempt \${attempt}/\${max}); retrying in 20s..."
  sleep 20
  attempt=\$((attempt + 1))
done
test -f "$(tarball_vm_path)"
ls -lh "$(tarball_vm_path)"
if [[ -f "${REPO_IN_VM}/build/images.json" ]]; then
  echo "===> unique squashfs layer sizes"
  bash "${REPO_IN_VM}/script/report-image-sizes.sh" "${REPO_IN_VM}/build/images.json" \
    | tee "${REPO_IN_VM}/build/image-size-report.txt"
fi
EOF

  # Synced folder: builder write is visible on the host and on cluster nodes.
  # VirtualBox sync can lag briefly after a large write.
  local path i
  path="$(tarball_host_path)"
  for i in $(seq 1 60); do
    if [[ -f "${path}" ]]; then
      break
    fi
    sleep 2
  done
  resolve_built_tarball
  echo "built $(basename "${BUILT_TARBALL}") ($(du -h "${BUILT_TARBALL}" | awk '{print $1}'))"
}

install_flynn_on_node() {
  local node=$1
  local tarball_in_vm
  tarball_in_vm="$(tarball_vm_path)"
  info "installing local build ${BUILD_VERSION} on ${node} from synced tarball"
  node_root_script "${node}" <<EOF
set -euo pipefail
tarball="${tarball_in_vm}"
# Wait briefly for VirtualBox synced-folder visibility after builder write.
for i in \$(seq 1 60); do
  if [[ -f "\${tarball}" ]]; then
    break
  fi
  sleep 2
done
test -f "\${tarball}" || { echo "tarball not visible on ${node}: \${tarball}" >&2; exit 1; }
tmpdir="\$(mktemp -d)"
trap 'rm -rf "\${tmpdir}"' EXIT
tar xzf "\${tarball}" -C "\${tmpdir}"
install_script="\$(find "\${tmpdir}" -maxdepth 2 -type f -name install-flynn | head -1)"
test -n "\${install_script}"
# Reruns on existing VMs (SKIP_VAGRANT_UP=1 / RESUME) find a previous install;
# the installer refuses unless --clean. Wipe it and any stale overlay devices so
# the new flanneld creates flannel.1 (and its VTEP MAC) from scratch.
extra_args=()
if [[ -e /usr/local/bin/flynn-host || -d /var/lib/flynn ]]; then
  echo "existing Flynn install detected on ${node}; reinstalling with --clean"
  extra_args+=(--clean)
fi
bash "\${install_script}" --yes --no-ntp "\${extra_args[@]}" --tarball "\${tarball}"
# EnableJobIsolation runs in the node flynn-host process (not the host squashfs).
# Older tarballs' install-flynn omit ipset; install it here so SKIP_BUILD still works.
if ! command -v ipset >/dev/null 2>&1; then
  echo "===> installing ipset on ${node} (required for flynn-host job isolation)"
  export DEBIAN_FRONTEND=noninteractive
  apt-get install -y ipset
fi
command -v ipset >/dev/null
src="${REPO_IN_VM}/build/bin/flynn-host"
if [[ -x "\${src}" && "\$(head -c 4 "\${src}")" == $'\x7fELF' ]]; then
  echo "overlaying \${src} onto flynn-host (host-side restore/CLI fixes)"
  install -m 0755 "\${src}" /usr/local/bin/flynn-host
  if [[ -e /usr/bin/flynn-host ]]; then
    install -m 0755 "\${src}" /usr/bin/flynn-host
  fi
fi
for link in flannel.1 flynnbr0; do
  if ip link show "\${link}" &>/dev/null; then
    echo "removing stale \${link}"
    ip link del "\${link}" || true
  fi
done
EOF
  configure_node_dns "${node}"
  node_root_script "${node}" <<'EOF'
set -euo pipefail
command -v flynn-host >/dev/null
flynn-host version
test -x /usr/bin/flynn-host || test -x /usr/local/bin/flynn-host
EOF
}

step_install_flynn() {
  resolve_built_tarball
  local node
  for node in "${NODES[@]}"; do
    install_flynn_on_node "${node}"
  done
  echo "installed and verified ${BUILD_VERSION} on ${NODES[*]} from $(tarball_vm_path)"
}

step_init_cluster() {
  local i node ip
  for i in "${!NODES[@]}"; do
    node="${NODES[$i]}"
    ip="${NODE_IPS[$i]}"
    # peer-ips is written into host.json so daemons can reconnect after
    # bootstrap starts discoverd. Discoverd itself is not up yet.
    info "configuring ${node} (external-ip=${ip}, peer-ips=${PEER_IPS})"
    node_root_script "${node}" <<EOF
set -euo pipefail
flynn-host init --peer-ips "${PEER_IPS}" --external-ip "${ip}"
systemctl enable flynn-host.service
systemctl restart flynn-host.service
EOF
    configure_node_dns "${node}"
  done

  # Match bootstrap's online-hosts check: host HTTP API on :1113.
  # Discoverd (:1111 / flynn-host service) only appears after bootstrap.
  info "waiting for flynn-host HTTP API on all nodes (:1113)"
  if ! wait_for "flynn-host HTTP API on ${PEER_IPS}" 300 hosts_api_ready; then
    dump_layer0_diagnostics
    return 1
  fi
  # Explicit post-init checks before bootstrap is allowed to start.
  local ip
  for ip in "${NODE_IPS[@]}"; do
    host_api_up "${ip}"
    echo "verified host API http://${ip}:1113/host/status"
  done
  echo "layer-0 host APIs up (peer-ips=${PEER_IPS})"
}

# Extra args are appended to flynn-host bootstrap (e.g. --from-backup FILE).
run_layer1_bootstrap() {
  local overlay_fail="${WORK_DIR}/overlay-fail.txt"
  local watch_pid=""
  local extra_args=""
  if [[ $# -gt 0 ]]; then
    extra_args="$(printf '%q ' "$@")"
  fi
  rm -f "${overlay_fail}"

  # Cache ssh-config before the overlay watcher and bootstrap race on node1.
  local n
  for n in "${NODES[@]}"; do
    cache_node_ssh_config "${n}"
  done

  # Fail fast if flannel comes up but cross-node overlay is broken, instead of
  # waiting ~5 minutes for postgres-wait to time out.
  (
    set +e
    watch_overlay_during_bootstrap "${overlay_fail}"
  ) &
  watch_pid=$!

  set +e
  node_root_script node1 <<EOF | tee "${WORK_DIR}/bootstrap.log"
set -euo pipefail
export CLUSTER_DOMAIN="${CLUSTER_DOMAIN}"
export FLANNEL_NETWORK="${FLANNEL_NETWORK:-100.64.0.0/16}"
export DISCOVERD="http://${NODE1_IP}:1111"
flynn-host bootstrap \
  --min-hosts "${MIN_HOSTS}" \
  --peer-ips "${PEER_IPS}" \
  --timeout "${BOOTSTRAP_JOB_TIMEOUT}" \
  --job-timeout "${BOOTSTRAP_JOB_TIMEOUT}" \
  ${extra_args}
EOF
  local boot_rc=$?
  set -e

  kill "${watch_pid}" 2>/dev/null || true
  wait "${watch_pid}" 2>/dev/null || true

  if [[ -f "${overlay_fail}" ]]; then
    echo "bootstrap aborted: $(cat "${overlay_fail}")" >&2
    dump_overlay_diagnostics
    return 1
  fi
  if [[ ${boot_rc} -ne 0 ]]; then
    dump_layer0_diagnostics
    dump_overlay_diagnostics
    return 1
  fi

  info "verifying overlay after bootstrap"
  if ! overlay_peers_reachable; then
    dump_overlay_diagnostics
    return 1
  fi

  ensure_flynn_cli_on_node1
  configure_node_dns node1
  register_cli_cluster
}

step_bootstrap() {
  run_layer1_bootstrap
  info "waiting for postgres primary read-write"
  # mariadb/mongodb are bootstrapped at scale 0; they have no primary until
  # the first `resource add mysql|mongodb`.
  if ! wait_datastores_ready "after bootstrap" postgres; then
    dump_overlay_diagnostics
    return 1
  fi
  echo "bootstrapped ${CLUSTER_DOMAIN} (${TOPOLOGY_LABEL} min-hosts=${MIN_HOSTS})"
}

# True when flynn-plugin.json name, aliases, provider, or CLI command is $2.
plugin_manifest_matches() {
  local json=$1 want=$2
  python3 - "$json" "$want" <<'PY'
import json, sys

path, want = sys.argv[1], sys.argv[2]
with open(path) as f:
    m = json.load(f)
if m.get("name") == want:
    raise SystemExit(0)
aliases = m.get("aliases") or []
if isinstance(aliases, str):
    aliases = [aliases]
if want in aliases:
    raise SystemExit(0)
prov = m.get("provider") or {}
if isinstance(prov, dict) and prov.get("name") == want:
    raise SystemExit(0)
cli = m.get("cli") or {}
if isinstance(cli, dict) and cli.get("command") == want:
    raise SystemExit(0)
raise SystemExit(1)
PY
}

plugin_checkout() {
  local name=$1
  local root="${PLUGIN_REPO_ROOT}"
  local direct="${root}/flynn-plugin-${name}"
  if [[ -d "${direct}" ]]; then
    echo "${direct}"
    return 0
  fi
  local dir json
  for dir in "${root}"/flynn-plugin-*; do
    [[ -d "${dir}" ]] || continue
    json="${dir}/flynn-plugin.json"
    [[ -f "${json}" ]] || continue
    if plugin_manifest_matches "${json}" "${name}"; then
      echo "${dir}"
      return 0
    fi
  done
  echo "${direct}"
}

# Long-lived builders (KEEP_BUILDER=1) may predate sibling plugin folders.
# Vagrantfile globs flynn-plugin-* at `vagrant up`; reload to attach new mounts.
ensure_plugin_vm_mounts() {
  local name dir vm_path vm n
  local vms="builder"
  local reload_list=" "
  for n in "${NODES[@]}"; do
    if vagrant_vm_running "${n}"; then
      vms="${vms} ${n}"
    fi
  done
  # shellcheck disable=SC2086
  for name in ${PLUGIN_SMOKE_APPS}; do
    dir="$(plugin_checkout "${name}")"
    if [[ ! -d "${dir}" ]]; then
      echo "plugin checkout missing for ${name}: ${dir}" >&2
      return 1
    fi
    vm_path="/opt/flynn-plugins/$(basename "${dir}")"
    for vm in ${vms}; do
      if node_root_script "${vm}" <<EOF
test -f "${vm_path}/flynn-plugin.json"
EOF
      then
        continue
      fi
      info "plugin ${name} not synced on ${vm} (${vm_path})"
      if [[ "${reload_list}" != *" ${vm} "* ]]; then
        reload_list="${reload_list}${vm} "
      fi
    done
  done
  if [[ "${reload_list}" == " " ]]; then
    echo "plugin mounts present on ${vms}"
    return 0
  fi
  for vm in ${reload_list}; do
    info "vagrant reload ${vm} so Vagrantfile flynn-plugin-* synced_folder takes effect"
    vagrant reload "${vm}" --no-provision
    rm -f "$(node_ssh_config_path "${vm}")"
    cache_node_ssh_config "${vm}" || return 1
  done
  # shellcheck disable=SC2086
  for name in ${PLUGIN_SMOKE_APPS}; do
    dir="$(plugin_checkout "${name}")"
    vm_path="/opt/flynn-plugins/$(basename "${dir}")"
    for vm in ${reload_list}; do
      if ! node_root_script "${vm}" <<EOF
test -f "${vm_path}/flynn-plugin.json"
EOF
      then
        echo "plugin still not synced after reload: ${vm}:${vm_path}" >&2
        return 1
      fi
    done
  done
}

ensure_plugin_image() {
  local dir=$1
  if [[ ! -d "${dir}" ]]; then
    echo "plugin checkout missing: ${dir}" >&2
    return 1
  fi
  # A leftover dist/image.json is not enough: pre-stack plugin-build wrote a
  # ~35MiB overlay-only squashfs. Installing that makes redis scale hang 5m.
  if plugin_dist_ready "${dir}"; then
    echo "plugin image ready (${dir}/dist, ubuntu-noble + delta)"
    return 0
  fi
  # GitHub ubuntu-noble shares Flynn layer IDs with a local build but not the
  # squashfs bytes (IDs hash recipe inputs, not GOARCH). Overlaying GitHub
  # amd64 binaries on the cluster's arm64 layer makes jobs exit 126. Build on
  # the builder against the ubuntu-noble squashfs from this smoke tarball.
  info "building plugin image on builder (${dir})"
  node_root_script builder <<EOF
set -euo pipefail
export PATH=/usr/local/go/bin:\$PATH
export FLYNN_IMAGES_JSON="${REPO_IN_VM}/build/images.json"
export FLYNN_LAYERS_DIR=/tmp/flynn-plugin-layers-${BUILD_VERSION}
export PLUGIN_BUILD_DOCKER=0
mkdir -p "\$FLYNN_LAYERS_DIR"
id=\$(python3 -c "import json; art=json.load(open('${REPO_IN_VM}/build/images.json')); img=art.get('ubuntu-noble') or art.get('postgres'); layers=[l for rf in (img.get('manifest') or {}).get('rootfs') or [] for l in rf.get('layers') or []]; print(layers[0]['id'])")
tarball="${REPO_IN_VM}/build/release/flynn-${BUILD_VERSION}.tar.gz"
dest="\$FLYNN_LAYERS_DIR/\$id.squashfs"
# Layer IDs hash recipe inputs, not bytes. Always extract from this smoke
# tarball so a KEEP_BUILDER cache cannot feed plugin-build the previous build.
tar -xOf "\$tarball" "flynn-${BUILD_VERSION}/\$id.squashfs" > "\$dest"
cd "/opt/flynn-plugins/$(basename "${dir}")"
test -f flynn-plugin.json
./script/plugin-build
EOF
  if ! plugin_dist_ready "${dir}"; then
    echo "plugin-build did not produce a stacked ubuntu-noble image in ${dir}/dist" >&2
    return 1
  fi
}

# True when dist/image.json is Flynn ubuntu-noble plus a plugin delta, and every
# referenced squashfs exists. Matches pkg/plugin.ValidatePluginLayers.
plugin_dist_ready() {
  local dir=$1
  python3 - "${dir}" <<'PY'
import json, os, sys
root = sys.argv[1]
dist = os.path.join(root, "dist")
path = os.path.join(dist, "image.json")
if not os.path.isfile(path):
    sys.exit(1)
art = json.load(open(path))
manifest = art.get("manifest") or {}
layers = []
for rootfs in manifest.get("rootfs") or []:
    layers.extend(rootfs.get("layers") or [])
if len(layers) < 2:
    sys.exit(1)
if int(layers[0].get("length") or 0) < 32 * 1024 * 1024:
    sys.exit(1)
files = (art.get("meta") or {}).get("flynn.plugin.files") or ""
plugin_path = os.path.join(root, "flynn-plugin.json")
if os.path.isfile(plugin_path):
    plugin = json.load(open(plugin_path))
    for entry in (plugin.get("build") or {}).get("entrypoint") or []:
        rel = entry.lstrip("/")
        if rel not in files.replace("\\", "/"):
            sys.exit(1)
if not files:
    sys.exit(1)
arch = (art.get("meta") or {}).get("flynn.plugin.arch") or ""
machine = os.uname().machine.lower()
if machine in ("arm64", "aarch64"):
    want = "arm64"
elif machine in ("x86_64", "amd64"):
    want = "amd64"
else:
    want = machine
if arch != want:
    sys.exit(1)
for layer in layers:
    lid = layer.get("id") or ""
    p1 = os.path.join(dist, "layers", lid + ".squashfs")
    p2 = os.path.join(dist, lid + ".squashfs")
    if not os.path.isfile(p1) and not os.path.isfile(p2):
        sys.exit(1)
sys.exit(0)
PY
}

dump_plugin_install_diagnostics() {
  local name=$1
  info "plugin install diagnostics (${name})"
  node_root_script node1 <<EOF || true
set +e
echo "=== flynn-host ps ==="
flynn-host ps 2>/dev/null | head -n 80
echo "=== ${name} job failures ==="
grep -E 'error in change state|container exited|failed to connect|fork/exec|exec format' /var/log/flynn/flynn-host.log 2>/dev/null | tail -n 40
echo "=== ${name} / squashfs in flynn-host.log ==="
grep -E '${name}|squashfs|missing URL|unexpected HTTP|error getting squashfs' /var/log/flynn/flynn-host.log 2>/dev/null | tail -n 50
echo "=== flynn-host journal ==="
journalctl -u flynn-host.service -n 60 --no-pager 2>/dev/null
EOF
}

# True when flynn-plugin.json publishes doc+actions so the user CLI fetches
# usage from the cluster instead of a compiled handler.
plugin_has_delegated_cli() {
  local dir
  dir="$(plugin_checkout "$1")"
  python3 - "$dir" <<'PY'
import json, os, sys
path = os.path.join(sys.argv[1], "flynn-plugin.json")
if not os.path.isfile(path):
    sys.exit(1)
cli = (json.load(open(path)).get("cli") or {})
sys.exit(0 if cli.get("doc") and cli.get("actions") else 1)
PY
}

# flynn help command list: a line whose first field is the plugin command.
help_lists_plugin_command() {
  local name=$1
  local out
  out="$(flynn1 help 2>&1)" || true
  printf '%s\n' "${out}" | awk -v cmd="${name}" '$1==cmd {found=1; exit} END {exit found?0:1}'
}

probe_delegated_plugin_cli_hidden() {
  local name=$1
  local out rc=0
  if help_lists_plugin_command "${name}"; then
    echo "flynn help listed ${name} before flynn-host plugin install" >&2
    return 1
  fi
  out="$(flynn1 "${name}" 2>&1)" || rc=$?
  if [[ "${rc}" -eq 0 ]]; then
    echo "flynn ${name} succeeded before plugin install: ${out}" >&2
    return 1
  fi
  echo "flynn help hides ${name}; flynn ${name} fails until plugin install (rc=${rc})"
}

probe_delegated_plugin_cli_visible() {
  local name=$1
  cli_probe "plugin-install" "cli-help-${name}" "${name}" \
    flynn1 help || return 1
  cli_probe "plugin-install" "cli-help-${name}-doc" "${name}" \
    flynn1 help "${name}" || return 1
}

step_install_plugins() {
  if [[ "${SKIP_PLUGIN_INSTALL}" == "1" ]]; then
    echo "SKIP_PLUGIN_INSTALL=1"
    return 0
  fi
  local name dir vm_path
  ensure_flynn_cli_on_node1
  ensure_plugin_vm_mounts || return 1
  # shellcheck disable=SC2086
  for name in ${PLUGIN_SMOKE_APPS}; do
    if plugin_has_delegated_cli "${name}"; then
      if help_lists_plugin_command "${name}"; then
        echo "plugin ${name} already in CLI catalog; skipping hidden-CLI probe"
        continue
      fi
      probe_delegated_plugin_cli_hidden "${name}" || return 1
    fi
  done
  # shellcheck disable=SC2086
  for name in ${PLUGIN_SMOKE_APPS}; do
    dir="$(plugin_checkout "${name}")"
    ensure_plugin_image "${dir}" || return 1
    vm_path="/opt/flynn-plugins/$(basename "${dir}")"
    info "flynn-host plugin install ${name} (${vm_path})"
    if ! node_root_script node1 <<EOF
set -euo pipefail
if [[ ! -f "${vm_path}/flynn-plugin.json" ]]; then
  echo "plugin not synced into VM: ${vm_path}" >&2
  exit 1
fi
flynn-host plugin install --no-build "${vm_path}"
EOF
    then
      dump_plugin_install_diagnostics "${name}"
      return 1
    fi
    if plugin_has_delegated_cli "${name}"; then
      probe_delegated_plugin_cli_visible "${name}" || return 1
    fi
  done
  echo "plugins installed: ${PLUGIN_SMOKE_APPS}"
}

# Write the backup to the synced folder (host can inspect it) and copy to /tmp
# on node1 so bootstrap --from-backup still works after install --clean wipes
# /var/lib/flynn and /usr/local/bin/flynn*.
step_cluster_backup() {
  ensure_flynn_cli_on_node1
  local vm_path restore_path host_path
  vm_path="$(backup_vm_path)"
  restore_path="$(backup_restore_path)"
  host_path="$(backup_host_path)"
  mkdir -p "$(dirname "${host_path}")"
  rm -f "${host_path}"

  info "creating cluster backup ${vm_path}"
  node_root_script node1 <<EOF
set -euo pipefail
mkdir -p "$(dirname "${vm_path}")"
rm -f "${vm_path}" "${restore_path}"
flynn cluster backup --file "${vm_path}"
test -s "${vm_path}"
# List once. Do not tar -tf | grep -q: grep -q closes the pipe on the first
# match and tar gets SIGPIPE (exit 141) under pipefail. Seen 2026-09-13.
members="\$(tar -tf "${vm_path}")"
printf '%s\n' "\${members}" | grep -F 'flynn.json' >/dev/null
printf '%s\n' "\${members}" | grep -F 'plugins.json' >/dev/null
printf '%s\n' "\${members}" | grep -F 'postgres.sql.gz' >/dev/null
printf '%s\n' "\${members}" | grep -F 'mysql.sql.gz' >/dev/null
printf '%s\n' "\${members}" | grep -F 'mongodb.archive.gz' >/dev/null
cp -f "${vm_path}" "${restore_path}"
test -s "${restore_path}"
ls -lh "${vm_path}" "${restore_path}"
EOF
  if [[ ! -s "${host_path}" ]]; then
    echo "cluster backup missing on host synced folder: ${host_path}" >&2
    return 1
  fi
  echo "cluster backup $(ls -lh "${host_path}" | awk '{print $5}') at ${host_path}"
}

step_bootstrap_from_backup() {
  local restore_path vm_path
  restore_path="$(backup_restore_path)"
  vm_path="$(backup_vm_path)"
  node_root_script node1 <<EOF
set -euo pipefail
if [[ ! -s "${restore_path}" && -s "${vm_path}" ]]; then
  cp -f "${vm_path}" "${restore_path}"
fi
test -s "${restore_path}"
EOF
  # CLUSTER_DOMAIN is ignored for --from-backup; the domain from the backup
  # is reused (same upgrade-smoke.localflynn.com /etc/hosts entries).
  run_layer1_bootstrap --from-backup "${restore_path}"
  info "waiting for restored postgres/mariadb/mongodb/redis"
  if ! wait_datastores_ready "after restore" postgres mariadb mongodb redis kafka clickhouse-ping; then
    dump_overlay_diagnostics
    return 1
  fi
  echo "restored ${CLUSTER_DOMAIN} from ${restore_path} (${TOPOLOGY_LABEL} min-hosts=${MIN_HOSTS})"
}

step_deploy_app() {
  ensure_flynn_cli_on_node1
  configure_node_dns node1

  local blobs="${SMOKE_BLOB_COUNT}"
  node_root_script node1 <<EOF
set -euo pipefail
test -d "${REPO_IN_VM}/test/apps/upgrade-smoke"
rm -rf "/tmp/${APP_NAME}"
mkdir -p "/tmp/${APP_NAME}/data"
cp -a "${REPO_IN_VM}/test/apps/upgrade-smoke/." "/tmp/${APP_NAME}/"
cd "/tmp/${APP_NAME}"
# Extra blob files are git-added then compiled into the slug via //go:embed
# so a later slugrunner restart still reports the same blob_count on /status.
seq 1 ${blobs} | while read -r i; do
  {
    printf 'smoke-blob-%s\n' "\$i"
    dd if=/dev/zero bs=1024 count=1 2>/dev/null | tr '\\0' 'A'
  } > "data/blob-\$i.txt"
done
test -f go.mod
test -f Procfile
git init
git config user.email "smoke@flynn.test"
git config user.name "smoke"
git add -A
git commit -m init
if flynn apps | grep -qE "(^|\\s)${APP_NAME}(\\s|\$)"; then
  flynn -a "${APP_NAME}" delete --yes || true
fi
flynn create --remote flynn "${APP_NAME}"
git push flynn master
EOF

  local provider
  for provider in "${DATASTORE_PROVIDERS[@]}"; do
    info "adding ${provider} resource"
    if ! wait_for "${provider} resource add" 600 flynn1 -a "${APP_NAME}" resource add "${provider}"; then
      record_check "deploy" "${provider}" "FAIL" "resource add timed out"
      echo "failed to add ${provider} resource" >&2
      return 1
    fi
    record_check "deploy" "${provider}" "PASS" "resource added"
  done

  wait_datastores_ready "after resource add" postgres mariadb mongodb redis || return 1
  seed_datastores
  echo "app ${APP_NAME} deployed from test/apps/upgrade-smoke with ${DATASTORE_PROVIDERS[*]} (${SMOKE_SEED_ROWS} rows, ${blobs} slug blobs)"
}

# git-push a Dockerfile on the container stack. This is the only live coverage
# of slimmed dockerbuilder-24 (ubuntu-noble + BuildKit + runc) and of tarreceive
# converting the resulting image to squashfs.
step_deploy_docker_app() {
  ensure_flynn_cli_on_node1
  configure_node_dns node1

  node_root_script node1 <<EOF
set -euo pipefail
test -f "${REPO_IN_VM}/test/apps/upgrade-smoke-docker/Dockerfile"
rm -rf "/tmp/${DOCKER_APP_NAME}"
mkdir -p "/tmp/${DOCKER_APP_NAME}"
cp -a "${REPO_IN_VM}/test/apps/upgrade-smoke-docker/." "/tmp/${DOCKER_APP_NAME}/"
cd "/tmp/${DOCKER_APP_NAME}"
chmod +x start.sh
test -f Dockerfile
git init
git config user.email "smoke@flynn.test"
git config user.name "smoke"
git add -A
git commit -m init
if flynn apps | grep -qE "(^|\\s)${DOCKER_APP_NAME}(\\s|\$)"; then
  flynn -a "${DOCKER_APP_NAME}" delete --yes || true
fi
flynn create --remote flynn "${DOCKER_APP_NAME}"
flynn -a "${DOCKER_APP_NAME}" stack set container
timeout 600 git push flynn master
# Container-stack releases use process type "app", not "web". gitreceive's
# default scale only sets web=1, and stack set already created a release, so
# the Dockerfile app would stay at 0 processes without this.
flynn -a "${DOCKER_APP_NAME}" scale app=1
flynn -a "${DOCKER_APP_NAME}" ps
EOF
  echo "docker app ${DOCKER_APP_NAME} deployed from test/apps/upgrade-smoke-docker (container stack)"
}

# Build a comma-separated SQL VALUES list  (1,'dummy-1'),(2,'dummy-2'),...
smoke_sql_values() {
  local n=$1
  local i
  local out=""
  for i in $(seq 1 "${n}"); do
    if [[ -n "${out}" ]]; then
      out+=","
    fi
    out+="(${i},'dummy-${i}')"
  done
  echo "${out}"
}

seed_datastores() {
  local rows="${SMOKE_SEED_ROWS}"
  local mysql_vals
  mysql_vals="$(smoke_sql_values "${rows}")"

  info "seeding ${rows} dummy rows/keys/payloads per datastore"
  node_root_script node1 <<EOF
set -euo pipefail
APP="${APP_NAME}"
ROWS="${rows}"

flynn -a "\${APP}" pg psql -- -v ON_ERROR_STOP=1 -c "
CREATE TABLE IF NOT EXISTS smoke_probe (id serial PRIMARY KEY, data text);
DELETE FROM smoke_probe;
INSERT INTO smoke_probe (data) VALUES ('pre-upgrade');
CREATE TABLE IF NOT EXISTS smoke_rows (id int PRIMARY KEY, data text);
DELETE FROM smoke_rows;
INSERT INTO smoke_rows (id, data) SELECT g, 'dummy-' || g FROM generate_series(1, \${ROWS}) g;
CREATE TABLE IF NOT EXISTS smoke_payload (id int PRIMARY KEY, payload text);
DELETE FROM smoke_payload;
INSERT INTO smoke_payload (id, payload) SELECT g, repeat('A', 1024) FROM generate_series(1, \${ROWS}) g;
"
exts="\$(flynn -a "\${APP}" pg psql -- -tAc "SELECT name FROM pg_available_extensions WHERE name IN ('postgis','pgrouting','timescaledb') ORDER BY 1")"
echo "\${exts}" | grep -qx postgis
echo "\${exts}" | grep -qx pgrouting
echo "\${exts}" | grep -qx timescaledb
echo "postgres extensions available: \${exts}"

flynn -a "\${APP}" mysql console -- -e "
CREATE TABLE IF NOT EXISTS smoke_probe (id INT PRIMARY KEY, data TEXT);
DELETE FROM smoke_probe;
INSERT INTO smoke_probe (id, data) VALUES (1, 'pre-upgrade');
CREATE TABLE IF NOT EXISTS smoke_rows (id INT PRIMARY KEY, data TEXT);
DELETE FROM smoke_rows;
INSERT INTO smoke_rows (id, data) VALUES ${mysql_vals};
CREATE TABLE IF NOT EXISTS smoke_payload (id INT PRIMARY KEY, payload TEXT);
DELETE FROM smoke_payload;
INSERT INTO smoke_payload (id, payload) SELECT id, REPEAT('A', 1024) FROM smoke_rows;
"

flynn -a "\${APP}" mongodb mongo -- --quiet --eval "
db.smoke_probe.deleteMany({});
db.smoke_probe.insertOne({id:1, data:'pre-upgrade'});
db.smoke_rows.deleteMany({});
var docs=[];
var blob = Array(257).join('A');
for (var i=1; i<=\${ROWS}; i++) { docs.push({n:i, data:'dummy-'+i, blob:blob}); }
db.smoke_rows.insertMany(docs);
"

flynn -a "\${APP}" redis redis-cli EVAL "
local pad = string.rep('A', 128)
for i=1,tonumber(ARGV[1]) do redis.call('SET', 'dummy:'..i, pad) end
redis.call('SET', 'smoke_probe', 'pre-upgrade')
return ARGV[1]
" 0 "\${ROWS}"

# Topic metadata lives on the kafka /data volume; recreate is ok if a prior
# failed run left the name behind.
ok=0
for i in \$(seq 1 30); do
  if flynn -a "\${APP}" kafka topics create smoke_probe --partitions 1 --replication 1 \\
    || flynn -a "\${APP}" kafka topics | grep -q smoke_probe; then
    ok=1
    break
  fi
  sleep 10
done
test "\$ok" = 1
flynn -a "\${APP}" kafka topics | grep -q smoke_probe

ok=0
for i in \$(seq 1 30); do
  # Local MergeTree on the leader (Keeper is often unreachable for ON CLUSTER
  # DDL). Fan-out the same schema/rows to every replica over HTTP :8123 so a
  # later node drain still has smoke_db on the remaining hosts.
  flynn -a "\${APP}" clickhouse client -- --query "CREATE DATABASE IF NOT EXISTS smoke_db ENGINE = Atomic" || true
  if flynn -a "\${APP}" clickhouse client -- --query "CREATE TABLE IF NOT EXISTS smoke_db.rows (id UInt32, data String) ENGINE = MergeTree ORDER BY id"; then
    ok=1
    break
  fi
  sleep 10
done
test "\$ok" = 1
flynn -a "\${APP}" clickhouse client -- --query "TRUNCATE TABLE IF EXISTS smoke_db.rows"
flynn -a "\${APP}" clickhouse client -- --query "INSERT INTO smoke_db.rows SELECT number+1, concat('dummy-', toString(number+1), repeat('A', 64)) FROM numbers(\${ROWS})"
CH_APP="\$(flynn -a "\${APP}" env get FLYNN_CLICKHOUSE)"
CH_USER="\$(flynn -a "\${APP}" env get CLICKHOUSE_USER)"
CH_PWD="\$(flynn -a "\${APP}" env get CLICKHOUSE_PASSWORD)"
export CH_APP CH_USER CH_PWD
CH_ROWS="\${ROWS}" python3 - <<'PY'
import json, os, sys, urllib.error, urllib.parse, urllib.request

app = os.environ["CH_APP"]
user = os.environ.get("CH_USER") or "default"
pwd = os.environ.get("CH_PWD") or ""
rows = os.environ["CH_ROWS"]
insts = json.load(urllib.request.urlopen("http://127.0.0.1:1111/services/%s/instances" % app, timeout=10))
if not insts:
    sys.exit("no clickhouse replicas in discoverd")
queries = [
    "CREATE DATABASE IF NOT EXISTS smoke_db ENGINE = Atomic",
    "CREATE TABLE IF NOT EXISTS smoke_db.rows (id UInt32, data String) ENGINE = MergeTree ORDER BY id",
    "TRUNCATE TABLE IF EXISTS smoke_db.rows",
    "INSERT INTO smoke_db.rows SELECT number+1, concat('dummy-', toString(number+1), repeat('A', 64)) FROM numbers(%s)" % rows,
]
failed = 0
for inst in insts:
    host = inst.get("addr", "").rsplit(":", 1)[0]
    if not host:
        continue
    print("clickhouse replica %s" % host)
    for q in queries:
        url = "http://%s:8123/?user=%s&password=%s" % (
            host, urllib.parse.quote(user), urllib.parse.quote(pwd),
        )
        req = urllib.request.Request(url, data=q.encode(), method="POST")
        try:
            with urllib.request.urlopen(req, timeout=60) as resp:
                resp.read()
        except Exception as e:
            print("clickhouse replica %s failed: %s" % (host, e), file=sys.stderr)
            failed = 1
            break
if failed:
    sys.exit(1)
print("clickhouse seeded on %d replicas" % len(insts))
PY
echo "seed complete"
EOF
  record_seed_counts
}

probe_docker_http() {
  local body
  body="$(curl -fsS --max-time 30 -H "Host: ${DOCKER_APP_NAME}.${CLUSTER_DOMAIN}" "http://${NODE1_IP}/")" || return 1
  echo "${body}" | grep -q 'docker-smoke ok'
}

assert_docker_http() {
  local label=$1
  local body
  body="$(curl -fsS --max-time 30 -H "Host: ${DOCKER_APP_NAME}.${CLUSTER_DOMAIN}" "http://${NODE1_IP}/")" || {
    record_check "${label}" "docker-http" "FAIL" "GET / curl failed"
    echo "docker HTTP check (${label}) failed: curl error" >&2
    return 1
  }
  if ! echo "${body}" | grep -q 'docker-smoke ok'; then
    record_check "${label}" "docker-http" "FAIL" "body=${body}"
    echo "docker HTTP check (${label}) failed: body=${body}" >&2
    return 1
  fi
  record_check "${label}" "docker-http" "PASS" "GET / => docker-smoke ok"
  echo "docker-http ${label}: ok"
}

assert_docker_ps() {
  local label=$1
  local out rc=0 attempt
  # After restore on 4-host add, HTTP can pass before controller lists the
  # app job (header-only `flynn ps`). Do not grep "up" in the whole buffer:
  # CREATED matches -iE 'up'. Require a data row with type app.
  for attempt in $(seq 1 12); do
    rc=0
    out="$(flynn1 -a "${DOCKER_APP_NAME}" ps -t app 2>&1)" || rc=$?
    if [[ "${rc}" -eq 0 ]] && echo "${out}" | awk 'NR>1 && $2=="app" && ($3=="up" || $3=="pending") { found=1 } END { exit !found }'; then
      record_check "${label}" "docker-ps" "PASS" "$(echo "${out}" | tr '\n' ' ' | cut -c1-80)"
      echo "docker-ps ${label}: ok"
      return 0
    fi
    echo "docker-ps ${label}: retry ${attempt}/12 rc=${rc} $(echo "${out}" | tr '\n' ' ' | cut -c1-60)"
    sleep 5
  done
  record_check "${label}" "docker-ps" "FAIL" "rc=${rc} $(echo "${out}" | tr '\n' ' ' | cut -c1-80)"
  echo "docker-ps ${label}: FAIL rc=${rc} ${out}" >&2
  return 1
}

probe_app_http() {
  local body
  body="$(curl -fsS --max-time 30 -H "Host: ${APP_NAME}.${CLUSTER_DOMAIN}" "http://${NODE1_IP}/")" || return 1
  echo "${body}" | grep -qi 'ok'
}

probe_app_status() {
  local body
  body="$(curl -sS --max-time 30 -H "Host: ${APP_NAME}.${CLUSTER_DOMAIN}" "http://${NODE1_IP}/status")" || return 1
  MIN_BLOBS="$((SMOKE_BLOB_COUNT + 1))" python3 -c '
import json, os, sys
raw = sys.stdin.read()
min_blobs = int(os.environ["MIN_BLOBS"])
try:
    d = json.loads(raw)
except Exception:
    sys.exit(2)
r = d.get("resources") or {}
need = ["postgres", "mysql", "mongodb", "redis", "kafka", "clickhouse"]
if any(not r.get(k) for k in need):
    sys.exit(1)
if int(d.get("blob_count") or 0) < min_blobs:
    sys.exit(3)
' <<<"${body}"
}

assert_app_http() {
  local label=$1
  local body
  body="$(curl -fsS --max-time 30 -H "Host: ${APP_NAME}.${CLUSTER_DOMAIN}" "http://${NODE1_IP}/")" || {
    record_check "${label}" "http" "FAIL" "GET / curl failed"
    echo "HTTP check (${label}) failed: curl error" >&2
    return 1
  }
  if ! echo "${body}" | grep -qi 'ok'; then
    record_check "${label}" "http" "FAIL" "body=${body}"
    echo "HTTP check (${label}) failed: body=${body}" >&2
    return 1
  fi
  record_check "${label}" "http" "PASS" "GET / => ok"
  echo "http ${label}: ok"
}

# GET /status from test/apps/upgrade-smoke: slug blob_count + FLYNN_* env for
# every attached datastore. curl without -f so a 503 still yields JSON.
assert_app_status() {
  local label=$1
  local body parsed rc=0
  body="$(curl -sS --max-time 30 -H "Host: ${APP_NAME}.${CLUSTER_DOMAIN}" "http://${NODE1_IP}/status")" || {
    record_check "${label}" "app-status" "FAIL" "GET /status curl failed"
    echo "app-status (${label}) curl failed" >&2
    return 1
  }
  parsed="$(MIN_BLOBS="$((SMOKE_BLOB_COUNT + 1))" python3 -c '
import json, os, sys
raw = sys.stdin.read()
min_blobs = int(os.environ["MIN_BLOBS"])
try:
    d = json.loads(raw)
except Exception as e:
    print("invalid json: %s raw=%r" % (e, raw[:240]))
    sys.exit(2)
r = d.get("resources") or {}
need = ["postgres", "mysql", "mongodb", "redis", "kafka", "clickhouse"]
missing = [k for k in need if not r.get(k)]
blobs = int(d.get("blob_count") or 0)
parts = ["blobs=%d" % blobs] + ["%s=%s" % (k, int(bool(r.get(k)))) for k in need]
print(" ".join(parts))
if missing or blobs < min_blobs:
    sys.exit(1)
' <<<"${body}")" || rc=$?
  if [[ ${rc} -ne 0 ]]; then
    record_check "${label}" "app-status" "FAIL" "${parsed:-parse failed} body=${body:0:160}"
    echo "app-status (${label}) failed: ${parsed} body=${body}" >&2
    return 1
  fi
  record_check "${label}" "app-status" "PASS" "${parsed}"
  echo "app-status ${label}: ${parsed}"
}

# Strip redis-cli warnings / tabular padding so COUNT(*) can be compared as an int.
numeric_count() {
  echo "${1:-}" | grep -Eo '[0-9]+' | tail -1
}

record_seed_counts() {
  local rows="${SMOKE_SEED_ROWS}"
  local failed=0
  local count payload topics

  count="$(numeric_count "$(flynn1 -a "${APP_NAME}" pg psql -- -tAc "SELECT COUNT(*) FROM smoke_rows")")"
  payload="$(numeric_count "$(flynn1 -a "${APP_NAME}" pg psql -- -tAc "SELECT COUNT(*) FROM smoke_payload")")"
  if [[ -n "${count}" && "${count}" -ge "${rows}" && -n "${payload}" && "${payload}" -ge "${rows}" ]]; then
    record_check "seed" "postgres" "PASS" "rows=${count} payload=${payload}"
  else
    record_check "seed" "postgres" "FAIL" "rows=${count:-?} payload=${payload:-?} want>=${rows}"
    failed=1
  fi

  count="$(numeric_count "$(flynn1 -a "${APP_NAME}" mysql console -- -N -e "SELECT COUNT(*) FROM smoke_rows")")"
  payload="$(numeric_count "$(flynn1 -a "${APP_NAME}" mysql console -- -N -e "SELECT COUNT(*) FROM smoke_payload")")"
  if [[ -n "${count}" && "${count}" -ge "${rows}" && -n "${payload}" && "${payload}" -ge "${rows}" ]]; then
    record_check "seed" "mysql" "PASS" "rows=${count} payload=${payload}"
  else
    record_check "seed" "mysql" "FAIL" "rows=${count:-?} payload=${payload:-?} want>=${rows}"
    failed=1
  fi

  count="$(numeric_count "$(flynn1 -a "${APP_NAME}" mongodb mongo -- --quiet --eval 'db.smoke_rows.count()')")"
  if [[ -n "${count}" && "${count}" -ge "${rows}" ]]; then
    record_check "seed" "mongodb" "PASS" "docs=${count}"
  else
    record_check "seed" "mongodb" "FAIL" "docs=${count:-?} want>=${rows}"
    failed=1
  fi

  count="$(numeric_count "$(flynn1 -a "${APP_NAME}" redis redis-cli DBSIZE)")"
  if [[ -n "${count}" && "${count}" -ge $((rows + 1)) ]]; then
    record_check "seed" "redis" "PASS" "dbsize=${count}"
  else
    record_check "seed" "redis" "FAIL" "dbsize=${count:-?} want>=$((rows + 1))"
    failed=1
  fi

  topics="$(flynn1 -a "${APP_NAME}" kafka topics)"
  if echo "${topics}" | grep -q smoke_probe; then
    record_check "seed" "kafka" "PASS" "topic=smoke_probe"
  else
    record_check "seed" "kafka" "FAIL" "missing smoke_probe: ${topics}"
    failed=1
  fi

  count="$(clickhouse_row_count)"
  if [[ -n "${count}" && "${count}" -ge "${rows}" ]]; then
    record_check "seed" "clickhouse" "PASS" "rows=${count}"
  else
    record_check "seed" "clickhouse" "FAIL" "rows=${count:-?} want>=${rows}"
    failed=1
  fi

  if [[ "${failed}" -ne 0 ]]; then
    echo "seed count verification failed" >&2
    return 1
  fi
  echo "seed verified: postgres/mysql/mongodb/redis/kafka/clickhouse rows>=${rows}"
}

# Require seeded dummy data (and any earlier verify-pass markers) to still be
# present, then append a row/key for this label so the next upgrade pass can
# prove it persisted. Each engine is recorded individually so the final report
# shows which datastore survived.
assert_databases() {
  local label=$1
  local rows="${SMOKE_SEED_ROWS}"
  local failed=0
  local out count payload

  echo "db-check ${label}: postgres"
  out="$(flynn1 -a "${APP_NAME}" pg psql -- -tAc "SELECT data FROM smoke_probe WHERE data='pre-upgrade'")"
  count="$(numeric_count "$(flynn1 -a "${APP_NAME}" pg psql -- -tAc "SELECT COUNT(*) FROM smoke_rows")")"
  payload="$(numeric_count "$(flynn1 -a "${APP_NAME}" pg psql -- -tAc "SELECT COUNT(*) FROM smoke_payload")")"
  if echo "${out}" | grep -q 'pre-upgrade' && [[ -n "${count}" && "${count}" -ge "${rows}" && -n "${payload}" && "${payload}" -ge "${rows}" ]]; then
    record_check "${label}" "postgres" "PASS" "probe=pre-upgrade rows=${count} payload=${payload}"
  else
    record_check "${label}" "postgres" "FAIL" "probe=${out} rows=${count:-?} payload=${payload:-?} want>=${rows}"
    echo "postgres (${label}) failed: probe=${out} rows=${count} payload=${payload}" >&2
    failed=1
  fi

  echo "db-check ${label}: mysql"
  out="$(flynn1 -a "${APP_NAME}" mysql console -- -N -e "SELECT data FROM smoke_probe WHERE data='pre-upgrade'")"
  count="$(numeric_count "$(flynn1 -a "${APP_NAME}" mysql console -- -N -e "SELECT COUNT(*) FROM smoke_rows")")"
  payload="$(numeric_count "$(flynn1 -a "${APP_NAME}" mysql console -- -N -e "SELECT COUNT(*) FROM smoke_payload")")"
  if echo "${out}" | grep -q 'pre-upgrade' && [[ -n "${count}" && "${count}" -ge "${rows}" && -n "${payload}" && "${payload}" -ge "${rows}" ]]; then
    record_check "${label}" "mysql" "PASS" "probe=pre-upgrade rows=${count} payload=${payload}"
  else
    record_check "${label}" "mysql" "FAIL" "probe=${out} rows=${count:-?} payload=${payload:-?} want>=${rows}"
    echo "mysql (${label}) failed: probe=${out} rows=${count} payload=${payload}" >&2
    failed=1
  fi

  echo "db-check ${label}: mongodb"
  out="$(flynn1 -a "${APP_NAME}" mongodb mongo -- --quiet --eval 'db.smoke_probe.findOne({data:"pre-upgrade"}).data')"
  count="$(numeric_count "$(flynn1 -a "${APP_NAME}" mongodb mongo -- --quiet --eval 'db.smoke_rows.count()')")"
  if echo "${out}" | grep -q 'pre-upgrade' && [[ -n "${count}" && "${count}" -ge "${rows}" ]]; then
    record_check "${label}" "mongodb" "PASS" "probe=pre-upgrade docs=${count}"
  else
    record_check "${label}" "mongodb" "FAIL" "probe=${out} docs=${count:-?} want>=${rows}"
    echo "mongodb (${label}) failed: probe=${out} docs=${count}" >&2
    failed=1
  fi

  echo "db-check ${label}: redis"
  out="$(flynn1 -a "${APP_NAME}" redis redis-cli GET smoke_probe)"
  local aof
  aof="$(flynn1 -a "${APP_NAME}" redis redis-cli INFO persistence)"
  count="$(numeric_count "$(flynn1 -a "${APP_NAME}" redis redis-cli DBSIZE)")"
  if echo "${out}" | grep -q 'pre-upgrade' && echo "${aof}" | grep -q 'aof_enabled:1' && [[ -n "${count}" && "${count}" -ge $((rows + 1)) ]]; then
    record_check "${label}" "redis" "PASS" "probe=pre-upgrade dbsize=${count} aof=1"
  else
    record_check "${label}" "redis" "FAIL" "probe=${out} dbsize=${count:-?} aof=$(echo "${aof}" | grep -E 'aof_enabled' || echo missing)"
    echo "redis (${label}) failed: probe=${out} dbsize=${count}" >&2
    failed=1
  fi

  echo "db-check ${label}: kafka"
  out="$(flynn1 -a "${APP_NAME}" kafka topics)"
  if echo "${out}" | grep -q smoke_probe; then
    record_check "${label}" "kafka" "PASS" "topic=smoke_probe"
  else
    record_check "${label}" "kafka" "FAIL" "missing smoke_probe topics=${out}"
    echo "kafka topic (${label}) missing smoke_probe: ${out}" >&2
    failed=1
  fi

  echo "db-check ${label}: clickhouse"
  if wait_for "clickhouse rows ${label}" 180 clickhouse_seed_ready; then
    count="$(clickhouse_row_count)"
    record_check "${label}" "clickhouse" "PASS" "rows=${count}"
  else
    count="$(clickhouse_row_count)"
    record_check "${label}" "clickhouse" "FAIL" "rows=${count:-?} want>=${rows}"
    echo "clickhouse row count (${label}) = ${count}, want >= ${rows}" >&2
    failed=1
  fi

  # Previous upgrade-pass markers must survive the next --force update.
  if [[ "${label}" == "post-upgrade-2" ]]; then
    out="$(flynn1 -a "${APP_NAME}" pg psql -- -tAc "SELECT data FROM smoke_probe WHERE data='post-upgrade-1'")"
    if echo "${out}" | grep -q 'post-upgrade-1'; then
      record_check "${label}" "pg-marker" "PASS" "post-upgrade-1 still present"
    else
      record_check "${label}" "pg-marker" "FAIL" "lost post-upgrade-1: ${out}"
      echo "postgres lost post-upgrade-1 marker before pass 2: ${out}" >&2
      failed=1
    fi
    out="$(flynn1 -a "${APP_NAME}" redis redis-cli GET smoke_probe_post-upgrade-1)"
    if echo "${out}" | grep -q 1; then
      record_check "${label}" "redis-marker" "PASS" "smoke_probe_post-upgrade-1=1"
    else
      record_check "${label}" "redis-marker" "FAIL" "lost key: ${out}"
      echo "redis lost smoke_probe_post-upgrade-1: ${out}" >&2
      failed=1
    fi
  fi

  if [[ "${failed}" -ne 0 ]]; then
    echo "datastore checks failed for ${label}" >&2
    return 1
  fi

  echo "db-check ${label}: writing persistence markers"
  flynn1 -a "${APP_NAME}" pg psql -- -c "INSERT INTO smoke_probe (data) VALUES ('${label}');" >/dev/null
  flynn1 -a "${APP_NAME}" mysql console -- -e "INSERT INTO smoke_probe (id, data) VALUES ($((RANDOM % 100000 + 2)), '${label}');" >/dev/null
  flynn1 -a "${APP_NAME}" mongodb mongo -- --eval "db.smoke_probe.insertOne({data:'${label}'});" >/dev/null
  flynn1 -a "${APP_NAME}" redis redis-cli SET "smoke_probe_${label}" 1 >/dev/null
  # INSERT SELECT, not VALUES: clickhouse-client treats VALUES as "read more
  # rows from stdin" and never exits while flynn run keeps stdin open.
  flynn1 -a "${APP_NAME}" clickhouse client -- --query "INSERT INTO smoke_db.rows SELECT toUInt32(100000 + ${RANDOM}), '${label}'" >/dev/null || true

  echo "databases ${label}: postgres/mysql/mongodb/redis/kafka/clickhouse PASS (rows>=${rows})"
}

# Cluster backup dumps postgres (incl. blobstore + app DBs), MariaDB, and
# MongoDB. Redis/Kafka/ClickHouse keep data on volumes that --clean destroys,
# so after restore those engines must come up empty while SQL data survives.
assert_restored_datastores() {
  local label="post-restore"
  local rows="${SMOKE_SEED_ROWS}"
  local failed=0
  local out count payload marker

  echo "db-check ${label}: postgres"
  out="$(flynn1 -a "${APP_NAME}" pg psql -- -tAc "SELECT data FROM smoke_probe WHERE data='pre-upgrade'")"
  count="$(numeric_count "$(flynn1 -a "${APP_NAME}" pg psql -- -tAc "SELECT COUNT(*) FROM smoke_rows")")"
  payload="$(numeric_count "$(flynn1 -a "${APP_NAME}" pg psql -- -tAc "SELECT COUNT(*) FROM smoke_payload")")"
  if echo "${out}" | grep -q 'pre-upgrade' && [[ -n "${count}" && "${count}" -ge "${rows}" && -n "${payload}" && "${payload}" -ge "${rows}" ]]; then
    record_check "${label}" "postgres" "PASS" "probe=pre-upgrade rows=${count} payload=${payload}"
  else
    record_check "${label}" "postgres" "FAIL" "probe=${out} rows=${count:-?} payload=${payload:-?} want>=${rows}"
    echo "postgres (${label}) failed: probe=${out} rows=${count} payload=${payload}" >&2
    failed=1
  fi

  echo "db-check ${label}: mysql"
  out="$(flynn1 -a "${APP_NAME}" mysql console -- -N -e "SELECT data FROM smoke_probe WHERE data='pre-upgrade'")"
  count="$(numeric_count "$(flynn1 -a "${APP_NAME}" mysql console -- -N -e "SELECT COUNT(*) FROM smoke_rows")")"
  payload="$(numeric_count "$(flynn1 -a "${APP_NAME}" mysql console -- -N -e "SELECT COUNT(*) FROM smoke_payload")")"
  if echo "${out}" | grep -q 'pre-upgrade' && [[ -n "${count}" && "${count}" -ge "${rows}" && -n "${payload}" && "${payload}" -ge "${rows}" ]]; then
    record_check "${label}" "mysql" "PASS" "probe=pre-upgrade rows=${count} payload=${payload}"
  else
    record_check "${label}" "mysql" "FAIL" "probe=${out} rows=${count:-?} payload=${payload:-?} want>=${rows}"
    echo "mysql (${label}) failed: probe=${out} rows=${count} payload=${payload}" >&2
    failed=1
  fi

  echo "db-check ${label}: mongodb"
  out="$(flynn1 -a "${APP_NAME}" mongodb mongo -- --quiet --eval 'db.smoke_probe.findOne({data:"pre-upgrade"}).data')"
  count="$(numeric_count "$(flynn1 -a "${APP_NAME}" mongodb mongo -- --quiet --eval 'db.smoke_rows.count()')")"
  if echo "${out}" | grep -q 'pre-upgrade' && [[ -n "${count}" && "${count}" -ge "${rows}" ]]; then
    record_check "${label}" "mongodb" "PASS" "probe=pre-upgrade docs=${count}"
  else
    record_check "${label}" "mongodb" "FAIL" "probe=${out} docs=${count:-?} want>=${rows}"
    echo "mongodb (${label}) failed: probe=${out} docs=${count}" >&2
    failed=1
  fi

  if [[ "${SKIP_UPGRADE}" != "1" ]]; then
    local pass
    for pass in $(seq 1 "${UPGRADE_PASSES}"); do
      marker="post-upgrade-${pass}"
      echo "db-check ${label}: ${marker} markers"
      out="$(flynn1 -a "${APP_NAME}" pg psql -- -tAc "SELECT data FROM smoke_probe WHERE data='${marker}'")"
      if echo "${out}" | grep -q "${marker}"; then
        record_check "${label}" "pg-${marker}" "PASS" "still present"
      else
        record_check "${label}" "pg-${marker}" "FAIL" "lost ${marker}: ${out}"
        echo "postgres (${label}) lost ${marker}: ${out}" >&2
        failed=1
      fi
    done
  fi

  echo "db-check ${label}: redis (volume data not in cluster backup)"
  out="$(flynn1 -a "${APP_NAME}" redis redis-cli PING 2>/dev/null || true)"
  if echo "${out}" | grep -qi PONG; then
    record_check "${label}" "redis" "PASS" "PING (keys not in cluster backup)"
  else
    record_check "${label}" "redis" "FAIL" "PING failed: ${out}"
    echo "redis (${label}) PING failed: ${out}" >&2
    failed=1
  fi

  echo "db-check ${label}: kafka (volume data not in cluster backup)"
  if kafka_is_ready; then
    record_check "${label}" "kafka" "PASS" "topics CLI (topic data not in cluster backup)"
  else
    record_check "${label}" "kafka" "FAIL" "kafka topics failed"
    echo "kafka (${label}) topics CLI failed" >&2
    failed=1
  fi

  echo "db-check ${label}: clickhouse (volume data not in cluster backup)"
  if clickhouse_ping; then
    record_check "${label}" "clickhouse" "PASS" "SELECT 1 (rows not in cluster backup)"
  else
    record_check "${label}" "clickhouse" "FAIL" "SELECT 1 failed"
    echo "clickhouse (${label}) SELECT 1 failed" >&2
    failed=1
  fi

  if [[ "${failed}" -ne 0 ]]; then
    echo "datastore checks failed for ${label}" >&2
    return 1
  fi
  echo "databases ${label}: postgres/mysql/mongodb restored; redis/kafka/clickhouse up empty"
}

# Record one live CLI / flynn-host probe. Empty pattern means exit 0 is enough.
cli_probe() {
  local label=$1 name=$2 pattern=$3
  shift 3
  local out rc=0 snippet
  out="$("$@" 2>&1)" || rc=$?
  snippet="$(printf '%s' "${out}" | tr '\n' ' ' | cut -c1-80)"
  if [[ "${rc}" -eq 0 ]]; then
    if [[ -z "${pattern}" ]] || printf '%s' "${out}" | grep -qE "${pattern}"; then
      record_check "${label}" "${name}" "PASS" "${snippet}"
      echo "cli ${label} ${name}: PASS"
      return 0
    fi
  fi
  record_check "${label}" "${name}" "FAIL" "rc=${rc} ${snippet}"
  echo "cli ${label} ${name}: FAIL rc=${rc} ${snippet}" >&2
  return 1
}

# One-off flynn run (new job from the app release image). Args after needle
# are passed after -- so they replace the image CMD.
cli_run_job() {
  local label=$1 name=$2 app=$3 needle=$4
  shift 4
  local cmd_q="" a rc=0 out
  for a in "$@"; do
    cmd_q+=" $(printf '%q' "${a}")"
  done
  out="$(node_ssh node1 "sudo -H timeout 90 flynn -a $(printf '%q' "${app}") run --${cmd_q}" </dev/null)" || rc=$?
  if [[ "${rc}" -eq 0 ]] && echo "${out}" | grep -qE "${needle}"; then
    record_check "${label}" "${name}" "PASS" "$(echo "${out}" | tr '\n' ' ' | cut -c1-80)"
    echo "cli ${label} ${name}: PASS"
    return 0
  fi
  record_check "${label}" "${name}" "FAIL" "rc=${rc} $(echo "${out}" | tr '\n' ' ' | cut -c1-80)"
  echo "cli ${label} ${name}: FAIL rc=${rc} ${out}" >&2
  return 1
}

# Expect a one-off to fail (NXDOMAIN, iptables DROP, or timeout). Used to
# prove user jobs cannot reach other apps or internal discoverd names.
# Retry unexpected success: after bootstrap --from-backup, flynn-net-user
# can still be empty so discoverd DNS treats the client as non-user and
# answers upgrade-smoke-web.discoverd (seen 2026-09-13 3-node-add restore).
cli_run_must_fail() {
  local label=$1 name=$2 app=$3
  shift 3
  local cmd_q="" a rc=0 out attempt
  for a in "$@"; do
    cmd_q+=" $(printf '%q' "${a}")"
  done
  for attempt in $(seq 1 8); do
    rc=0
    out="$(node_ssh node1 "sudo -H timeout 20 flynn -a $(printf '%q' "${app}") run --${cmd_q}" </dev/null)" || rc=$?
    if [[ "${rc}" -ne 0 ]]; then
      if echo "${out}" | grep -qiE 'No app release|stat /runner/init|unknown app'; then
        record_check "${label}" "${name}" "FAIL" "run failed to start $(echo "${out}" | tr '\n' ' ' | cut -c1-80)"
        echo "cli ${label} ${name}: FAIL job did not start: ${out}" >&2
        return 1
      fi
      record_check "${label}" "${name}" "PASS" "isolated rc=${rc}"
      echo "cli ${label} ${name}: PASS (blocked)"
      return 0
    fi
    echo "cli ${label} ${name}: retry ${attempt}/8 still reachable: $(echo "${out}" | tr '\n' ' ' | cut -c1-60)"
    sleep 3
  done
  record_check "${label}" "${name}" "FAIL" "unexpected success $(echo "${out}" | tr '\n' ' ' | cut -c1-80)"
  echo "cli ${label} ${name}: FAIL unexpectedly succeeded: ${out}" >&2
  return 1
}

# Live flynn + flynn-host commands against the cluster. Unit tests cover CLI
# packages (./cli on the host gate, ./cli + ./host/cli on the builder); this
# catches controller/scheduler/logaggregator drift after an upgrade. Does not
# scale or env-set (those create releases and restart web).
step_cli_functions() {
  local label=$1
  local failed=0
  local out rc hosts blob_ok attempt

  ensure_flynn_cli_on_node1
  echo "cli ${label}: flynn + flynn-host against ${APP_NAME}"

  cli_probe "${label}" "cli-apps" "${APP_NAME}" \
    flynn1 apps || failed=1
  cli_probe "${label}" "cli-info" "${APP_NAME}|Git URL|Web URL" \
    flynn1 -a "${APP_NAME}" info || failed=1
  cli_probe "${label}" "cli-ps" "web" \
    flynn1 -a "${APP_NAME}" ps || failed=1
  cli_probe "${label}" "cli-scale" "web=" \
    flynn1 -a "${APP_NAME}" scale || failed=1
  cli_probe "${label}" "cli-env" "FLYNN_POSTGRES" \
    flynn1 -a "${APP_NAME}" env || failed=1
  cli_probe "${label}" "cli-env-get" "." \
    flynn1 -a "${APP_NAME}" env get FLYNN_POSTGRES || failed=1
  rc=0
  out="$(flynn1 -a "${APP_NAME}" resource 2>&1)" || rc=$?
  if [[ "${rc}" -eq 0 ]]; then
    local missing=()
    local provider
    for provider in "${DATASTORE_PROVIDERS[@]}"; do
      if ! printf '%s' "${out}" | grep -qi "${provider}"; then
        missing+=("${provider}")
      fi
    done
    if [[ ${#missing[@]} -eq 0 ]]; then
      record_check "${label}" "cli-resource" "PASS" "providers=${DATASTORE_PROVIDERS[*]}"
      echo "cli ${label} cli-resource: PASS"
    else
      record_check "${label}" "cli-resource" "FAIL" "missing ${missing[*]}"
      echo "cli ${label} resource: missing ${missing[*]}" >&2
      failed=1
    fi
  else
    record_check "${label}" "cli-resource" "FAIL" "rc=${rc} $(printf '%s' "${out}" | tr '\n' ' ' | cut -c1-80)"
    echo "cli ${label} resource: FAIL rc=${rc}" >&2
    failed=1
  fi
  cli_probe "${label}" "cli-provider" "postgres" \
    flynn1 provider || failed=1
  cli_probe "${label}" "cli-route" "http|${APP_NAME}" \
    flynn1 -a "${APP_NAME}" route || failed=1
  cli_probe "${label}" "cli-release" "." \
    flynn1 -a "${APP_NAME}" release || failed=1
  cli_probe "${label}" "cli-limit" "memory=|web" \
    flynn1 -a "${APP_NAME}" limit || failed=1
  cli_probe "${label}" "cli-log" "" \
    flynn1 -a "${APP_NAME}" log -n 20 || failed=1

  cli_run_job "${label}" "cli-run" "${APP_NAME}" "smoke-cli" echo smoke-cli || failed=1

  # Container-stack app: same image as the running app process, no /runner/init.
  cli_probe "${label}" "docker-cli-info" "${DOCKER_APP_NAME}|Git URL|Web URL" \
    flynn1 -a "${DOCKER_APP_NAME}" info || failed=1
  cli_probe "${label}" "docker-cli-ps" "app" \
    flynn1 -a "${DOCKER_APP_NAME}" ps || failed=1
  cli_probe "${label}" "docker-cli-scale" "app=" \
    flynn1 -a "${DOCKER_APP_NAME}" scale || failed=1
  cli_probe "${label}" "docker-cli-route" "http|${DOCKER_APP_NAME}" \
    flynn1 -a "${DOCKER_APP_NAME}" route || failed=1
  cli_probe "${label}" "docker-cli-release" "." \
    flynn1 -a "${DOCKER_APP_NAME}" release || failed=1
  cli_probe "${label}" "docker-cli-log" "" \
    flynn1 -a "${DOCKER_APP_NAME}" log -n 20 || failed=1
  cli_run_job "${label}" "docker-cli-run" "${DOCKER_APP_NAME}" "docker-cli" \
    echo docker-cli || failed=1
  cli_run_job "${label}" "docker-cli-run-image" "${DOCKER_APP_NAME}" "httpd|PORT" \
    cat /start.sh || failed=1
  cli_run_must_fail "${label}" "net-isolate-peer" "${DOCKER_APP_NAME}" \
    wget -q -T 5 -O - "http://${APP_NAME}-web.discoverd:8080/" || failed=1
  cli_run_must_fail "${label}" "net-isolate-internal" "${DOCKER_APP_NAME}" \
    wget -q -T 5 -O - "http://postgres.discoverd:5432/" || failed=1
  cli_run_must_fail "${label}" "net-isolate-api" "${DOCKER_APP_NAME}" \
    wget -q -T 5 -O - "http://postgres-api.discoverd/" || failed=1
  cli_run_job "${label}" "net-db-leader" "${APP_NAME}" "db-leader-ok" \
    bash -c 'echo >/dev/tcp/leader.postgres.discoverd/5432 && echo db-leader-ok' || failed=1

  flynn1 -a "${APP_NAME}" meta set "smoke_cli=${label}" >/dev/null || true
  cli_probe "${label}" "cli-meta" "smoke_cli" \
    flynn1 -a "${APP_NAME}" meta || failed=1
  flynn1 -a "${APP_NAME}" meta unset smoke_cli >/dev/null || true

  rc=0
  out="$(node_ssh node1 'sudo flynn-host list' </dev/null)" || rc=$?
  hosts="$(printf '%s\n' "${out}" | awk 'NR>1 && NF>=2 {c++} END{print c+0}')"
  if [[ "${rc}" -eq 0 && "${hosts}" -ge "${#NODES[@]}" ]]; then
    record_check "${label}" "cli-host-list" "PASS" "hosts=${hosts}"
    echo "cli ${label} host-list: PASS hosts=${hosts}"
  else
    record_check "${label}" "cli-host-list" "FAIL" "rc=${rc} hosts=${hosts:-?} want>=${#NODES[@]}"
    echo "cli ${label} host-list: FAIL rc=${rc} hosts=${hosts} ${out}" >&2
    failed=1
  fi
  cli_probe "${label}" "cli-host-ps" "." \
    node_ssh node1 'sudo flynn-host ps' || failed=1
  cli_probe "${label}" "cli-host-version" "." \
    node_ssh node1 'sudo flynn-host version' || failed=1

  rc=0
  out="$(flynn1 -a "${APP_NAME}" pg psql -- -tAc "SELECT name FROM pg_available_extensions WHERE name IN ('postgis','pgrouting','timescaledb') ORDER BY 1" 2>&1)" || rc=$?
  if [[ "${rc}" -eq 0 ]] && echo "${out}" | grep -q postgis && echo "${out}" | grep -q pgrouting && echo "${out}" | grep -q timescaledb; then
    record_check "${label}" "cli-pg-extensions" "PASS" "$(echo "${out}" | tr '\n' ' ')"
    echo "cli ${label} pg-extensions: PASS"
  else
    record_check "${label}" "cli-pg-extensions" "FAIL" "rc=${rc} ${out}"
    echo "cli ${label} pg-extensions: FAIL rc=${rc} ${out}" >&2
    failed=1
  fi

  # User-app role: CONNECT to its own DB, not postgres/template1, not the
  # controller database. Cluster key can still open platform consoles.
  # Emit t/f in SQL: boolean||boolean prints true/false, which failed 2026-09-13.
  rc=0
  out="$(flynn1 -a "${APP_NAME}" pg psql -- -tAc "SELECT CASE WHEN has_database_privilege(current_user, current_database(), 'CONNECT') THEN 't' ELSE 'f' END||','||CASE WHEN has_database_privilege(current_user, 'postgres', 'CONNECT') THEN 't' ELSE 'f' END||','||CASE WHEN has_database_privilege(current_user, 'template1', 'CONNECT') THEN 't' ELSE 'f' END" 2>&1)" || rc=$?
  out="$(smoke_pg_tf "${out}")"
  if [[ "${rc}" -eq 0 && "${out}" == "t,f,f" ]]; then
    record_check "${label}" "cli-pg-connect" "PASS" "own=t postgres=f template1=f"
    echo "cli ${label} pg-connect: PASS"
  else
    record_check "${label}" "cli-pg-connect" "FAIL" "rc=${rc} ${out} want t,f,f"
    echo "cli ${label} pg-connect: FAIL rc=${rc} ${out}" >&2
    failed=1
  fi
  local ctl_db=""
  ctl_db="$(flynn1 -a controller env get PGDATABASE 2>/dev/null || true)"
  ctl_db="$(printf '%s' "${ctl_db}" | tr -d '[:space:]')"
  if [[ -z "${ctl_db}" ]]; then
    local ctl_url=""
    ctl_url="$(flynn1 -a controller env get DATABASE_URL 2>/dev/null || true)"
    ctl_url="$(printf '%s' "${ctl_url}" | tr -d '[:space:]')"
    ctl_db="${ctl_url##*/}"
    ctl_db="${ctl_db%%\?*}"
  fi
  if [[ -n "${ctl_db}" && "${ctl_db}" =~ ^[A-Za-z0-9_]+$ ]]; then
    rc=0
    out="$(flynn1 -a "${APP_NAME}" pg psql -- -tAc "SELECT CASE WHEN has_database_privilege(current_user, '${ctl_db}', 'CONNECT') THEN 't' ELSE 'f' END" 2>&1)" || rc=$?
    out="$(smoke_pg_tf "${out}")"
    if [[ "${rc}" -eq 0 && "${out}" == "f" ]]; then
      record_check "${label}" "cli-pg-no-controller" "PASS" "no CONNECT on ${ctl_db}"
      echo "cli ${label} pg-no-controller: PASS"
    else
      record_check "${label}" "cli-pg-no-controller" "FAIL" "rc=${rc} ${out} db=${ctl_db} want f"
      echo "cli ${label} pg-no-controller: FAIL rc=${rc} ${out} db=${ctl_db}" >&2
      failed=1
    fi
  else
    record_check "${label}" "cli-pg-no-controller" "FAIL" "controller PGDATABASE missing or unsafe: ${ctl_db:-empty}"
    echo "cli ${label} pg-no-controller: FAIL missing controller PGDATABASE" >&2
    failed=1
  fi
  cli_probe "${label}" "cli-pg-controller" "." \
    flynn1 -a controller pg psql -- -tAc "SELECT 1" || failed=1
  cli_probe "${label}" "cli-pg-blobstore" "." \
    flynn1 -a blobstore pg psql -- -tAc "SELECT 1" || failed=1

  # Plugin CLI: usage from the cluster catalog, job on the redis image.
  if plugin_has_delegated_cli redis; then
    cli_probe "${label}" "cli-help-redis" "redis" \
      flynn1 help || failed=1
    cli_probe "${label}" "cli-help-redis-doc" "redis-cli" \
      flynn1 help redis || failed=1
    rc=0
    out="$(flynn1 -a "${APP_NAME}" redis dump -q -f /tmp/smoke-redis.dump 2>&1)" || rc=$?
    if [[ "${rc}" -eq 0 ]] && node_ssh node1 'test -s /tmp/smoke-redis.dump' </dev/null; then
      record_check "${label}" "cli-redis-dump" "PASS" "rdb=$(node_ssh node1 'wc -c </tmp/smoke-redis.dump' </dev/null | tr -d ' ')"
      echo "cli ${label} cli-redis-dump: PASS"
    else
      record_check "${label}" "cli-redis-dump" "FAIL" "rc=${rc} $(printf '%s' "${out}" | tr '\n' ' ' | cut -c1-80)"
      echo "cli ${label} cli-redis-dump: FAIL rc=${rc} ${out}" >&2
      failed=1
    fi
    cli_probe "${label}" "cli-redis-restore" "" \
      flynn1 -a "${APP_NAME}" redis restore -q -f /tmp/smoke-redis.dump || failed=1
  fi

  if plugin_has_delegated_cli mysql; then
    cli_probe "${label}" "cli-help-mysql" "mysql" \
      flynn1 help || failed=1
    cli_probe "${label}" "cli-help-mysql-doc" "console" \
      flynn1 help mysql || failed=1
    cli_probe "${label}" "cli-mysql-dump" "" \
      flynn1 -a "${APP_NAME}" mysql dump -q -f /tmp/smoke-mysql.dump || failed=1
  fi

  if plugin_has_delegated_cli mongodb; then
    cli_probe "${label}" "cli-help-mongodb" "mongodb" \
      flynn1 help || failed=1
    cli_probe "${label}" "cli-help-mongodb-doc" "mongo" \
      flynn1 help mongodb || failed=1
    cli_probe "${label}" "cli-mongo-dump" "" \
      flynn1 -a "${APP_NAME}" mongodb dump -q -f /tmp/smoke-mongo.dump || failed=1
  fi

  if plugin_has_delegated_cli kafka; then
    cli_probe "${label}" "cli-help-kafka" "kafka" \
      flynn1 help || failed=1
    cli_probe "${label}" "cli-help-kafka-doc" "topics" \
      flynn1 help kafka || failed=1
    cli_probe "${label}" "cli-kafka-topics" "" \
      flynn1 -a "${APP_NAME}" kafka topics || failed=1
  fi

  if plugin_has_delegated_cli clickhouse; then
    cli_probe "${label}" "cli-help-clickhouse" "clickhouse" \
      flynn1 help || failed=1
    cli_probe "${label}" "cli-help-clickhouse-doc" "client" \
      flynn1 help clickhouse || failed=1
    cli_probe "${label}" "cli-clickhouse-databases" "" \
      flynn1 -a "${APP_NAME}" clickhouse databases || failed=1
  fi

  # GET / lists every blob and 500s if postgres is briefly unavailable after
  # an update. /.well-known/status is the health check (SELECT 1). Retry:
  # discoverd can return 500 while blobstore is re-registering.
  blob_ok=0
  out=""
  rc=0
  for attempt in $(seq 1 12); do
    rc=0
    out="$(node_ssh node1 "sudo -H timeout 90 flynn -a blobstore run -- wget -qO- http://blobstore.discoverd/.well-known/status" </dev/null)" || rc=$?
    if [[ "${rc}" -eq 0 ]] && echo "${out}" | grep -q healthy; then
      blob_ok=1
      break
    fi
    echo "cli ${label} blobstore: retry ${attempt}/12 rc=${rc}"
    sleep 5
  done
  if [[ "${blob_ok}" -eq 1 ]]; then
    record_check "${label}" "cli-blobstore" "PASS" "blobstore.discoverd healthy"
    echo "cli ${label} blobstore: PASS"
  else
    record_check "${label}" "cli-blobstore" "FAIL" "rc=${rc} $(echo "${out}" | tr '\n' ' ' | cut -c1-80)"
    echo "cli ${label} blobstore: FAIL rc=${rc} ${out}" >&2
    failed=1
  fi

  if [[ "${failed}" -ne 0 ]]; then
    echo "CLI function checks failed for ${label}" >&2
    return 1
  fi
  echo "cli ${label}: apps/ps/scale/env/resource/route/release/log/run/docker-run/meta/host PASS"
}

step_verify_before() {
  wait_for "app HTTP pre-upgrade" 180 probe_app_http
  assert_app_http pre-upgrade
  wait_for "app /status pre-upgrade" 120 probe_app_status
  assert_app_status pre-upgrade
  wait_for "docker app HTTP pre-upgrade" 180 probe_docker_http
  assert_docker_http pre-upgrade
  assert_docker_ps pre-upgrade
  assert_databases pre-upgrade
}

step_upgrade() {
  resolve_built_tarball
  local tarball_in_vm
  tarball_in_vm="$(tarball_vm_path)"
  local pass=${1:-1}

  info "running local tarball update --all-nodes pass ${pass} (${BUILD_VERSION})"
  node_root_script node1 <<EOF
set -euo pipefail
test -f "${tarball_in_vm}"
flynn-host update --all-nodes --tarball "${tarball_in_vm}" --force
EOF
  wait_datastores_ready "after upgrade ${pass}" postgres mariadb mongodb redis || return 1
  echo "local tarball update pass ${pass} complete"
}

step_verify_after() {
  local pass=${1:-1}
  local label="post-upgrade-${pass}"
  wait_for "app HTTP ${label}" 300 probe_app_http
  assert_app_http "${label}"
  wait_for "app /status ${label}" 180 probe_app_status
  assert_app_status "${label}"
  wait_for "docker app HTTP ${label}" 180 probe_docker_http
  assert_docker_http "${label}"
  assert_docker_ps "${label}"
  assert_databases "${label}"
  node_ssh node1 'sudo flynn-host version' || true
}

step_verify_after_restore() {
  local label="post-restore"
  wait_for "app HTTP ${label}" 300 probe_app_http
  assert_app_http "${label}"
  wait_for "app /status ${label}" 180 probe_app_status
  assert_app_status "${label}"
  wait_for "docker app HTTP ${label}" 180 probe_docker_http
  assert_docker_http "${label}"
  assert_docker_ps "${label}"
  assert_restored_datastores
  node_ssh node1 'sudo flynn-host version' || true
}

# Git-push a new docker release and run a one-off so membership changes prove
# deploys still work (not only HTTP to the existing formation).
step_membership_deploy() {
  local label=$1
  ensure_flynn_cli_on_node1
  info "membership deploy (${label}): git-push ${DOCKER_APP_NAME} + flynn run"
  node_root_script node1 <<EOF
set -euo pipefail
dir="/tmp/${DOCKER_APP_NAME}"
test -d "\${dir}/.git" || { echo "docker app git dir missing; deploy step must run first" >&2; exit 1; }
cd "\${dir}"
git commit --allow-empty -m "membership ${label}"
ok=0
for i in \$(seq 1 5); do
  if timeout 600 git push flynn master; then
    ok=1
    break
  fi
  echo "git push attempt \$i failed; waiting for scheduler after membership change" >&2
  sleep 30
done
test "\$ok" = 1
flynn -a "${DOCKER_APP_NAME}" ps
EOF
  wait_for "docker app HTTP ${label}" 180 probe_docker_http
  assert_docker_http "${label}"
  assert_docker_ps "${label}"
  cli_run_job "${label}" "membership-run" "${APP_NAME}" "membership-ok" echo membership-ok
  echo "membership deploy ${label}: docker git-push + slug flynn run ok"
}

step_verify_membership() {
  local label=$1
  wait_datastores_ready "${label}" postgres mariadb mongodb redis clickhouse || return 1
  wait_for "app HTTP ${label}" 180 probe_app_http
  assert_app_http "${label}"
  wait_for "app /status ${label}" 120 probe_app_status
  assert_app_status "${label}"
  wait_for "docker app HTTP ${label}" 180 probe_docker_http
  assert_docker_http "${label}"
  assert_docker_ps "${label}"
  assert_databases "${label}"
  step_membership_deploy "${label}"
}

# Join node4 to a running 3-node cluster (documented flynn-host init --peer-ips).
step_add_cluster_node() {
  local extra="node4"
  local extra_n=4
  local extra_ip join_ips
  extra_ip="$(cluster_node_ip "${extra_n}")"
  join_ips="${PEER_IPS}"
  if [[ "${#NODES[@]}" -lt 3 ]]; then
    echo "add-node requires a running HA cluster (got ${#NODES[@]} hosts)" >&2
    return 1
  fi
  remember_teardown_node "${extra}"
  info "adding ${extra} (${extra_ip}) to stable cluster peer-ips=${join_ips}"
  local node_mem="${VAGRANT_MEMORY:-6144}"
  local node_cpus="${VAGRANT_CPUS:-2}"
  VAGRANT_MEMORY="${node_mem}" VAGRANT_CPUS="${node_cpus}" vagrant up "${extra}"
  cache_node_ssh_config "${extra}"
  local saved_nodes=("${NODES[@]}")
  NODES=("${extra}")
  verify_nic_promisc
  NODES=("${saved_nodes[@]}")
  resolve_built_tarball
  install_flynn_on_node "${extra}"
  info "joining ${extra} with flynn-host init --peer-ips ${join_ips}"
  node_root_script "${extra}" <<EOF
set -euo pipefail
flynn-host init --peer-ips "${join_ips}" --external-ip "${extra_ip}"
systemctl enable flynn-host.service
systemctl restart flynn-host.service
EOF
  if ! wait_for "flynn-host HTTP API on ${extra_ip}" 180 host_api_up "${extra_ip}"; then
    dump_layer0_diagnostics
    return 1
  fi
  append_live_node "${extra}"
  local n
  for n in "${NODES[@]}"; do
    configure_node_dns "${n}"
  done
  if ! wait_for "flynn-host list includes ${extra_ip}" 180 node_addr_listed "${extra_ip}"; then
    echo "new host ${extra} (${extra_ip}) did not appear in flynn-host list" >&2
    node_ssh node1 'sudo flynn-host list' || true
    return 1
  fi
  if ! wait_for "overlay after adding ${extra}" 180 overlay_peers_reachable; then
    dump_overlay_diagnostics
    return 1
  fi
  echo "joined ${extra} (${extra_ip}); cluster hosts=${#NODES[@]} peer-ips=${PEER_IPS}"
  sync_cluster_monitor_hosts
}

# Flynn redis is a singleton with a host-local /data volume. Draining that
# host cannot reattach the AOF, so pick a different HA node when redis lives
# on the default drain target.
redis_job_host() {
  local app out
  app="$(flynn1 -a "${APP_NAME}" env get FLYNN_REDIS)" || return 1
  out="$(flynn1 -a "${app}" ps)" || return 1
  echo "${out}" | awk 'NR>1 && $2=="redis" && tolower($3) ~ /up|running/ {
    split($1, a, "-")
    print a[1]
    exit
  }'
}

pick_remove_node() {
  local drop redis_host
  drop="${NODES[$((${#NODES[@]} - 1))]}"
  redis_host="$(redis_job_host || true)"
  if [[ -n "${redis_host}" && "${redis_host}" == "${drop}" && "${#NODES[@]}" -ge 3 ]]; then
    echo "redis singleton is on ${drop}; draining ${NODES[$((${#NODES[@]} - 2))]} instead (host-local AOF)" >&2
    drop="${NODES[$((${#NODES[@]} - 2))]}"
  fi
  echo "${drop}"
}

controller_scheduler_up() {
  flynn1 -a controller ps | awk 'NR>1 && $2=="scheduler" && tolower($3) ~ /up|running/ {found=1} END { exit !found }'
}

controller_scheduler_replaced() {
  local old=$1 id
  id="$(flynn1 -a controller ps | awk 'NR>1 && $2=="scheduler" && tolower($3) ~ /up|running/ {print $1; exit}')"
  [[ -n "${id}" && "${id}" != "${old}" ]]
}

# Host drain can panic the leader scheduler (nil volume on persistJob). The
# process may stay "up" with its loop dead, so new deploys sit pending until
# we replace it.
bounce_controller_scheduler() {
  local id
  id="$(flynn1 -a controller ps | awk 'NR>1 && $2=="scheduler" && tolower($3) !~ /pending|down/ {print $1; exit}')"
  if [[ -z "${id}" ]]; then
    echo "no running controller scheduler found after drain" >&2
    wait_for "controller scheduler running" 180 controller_scheduler_up
    return
  fi
  echo "bouncing controller scheduler ${id} after host drain" >&2
  flynn1 -a controller kill "${id}" || true
  wait_for "new controller scheduler after ${id}" 180 controller_scheduler_replaced "${id}"
  # Give the replacement time to elect and recover host/job state before deploys.
  sleep 15
}

# flynn-host update waits for cluster-monitor's bootstrap host count. After
# add/remove that metadata is stale (3 vs 4, or 3 vs 2) and a drain then
# upgrade hangs waiting for the missing peer.
sync_cluster_monitor_hosts() {
  local want=${#NODES[@]}
  info "setting cluster-monitor hosts=${want} (live membership)"
  node_root_script node1 <<EOF
set -euo pipefail
export WANT_HOSTS="${want}"
python3 - <<'PY'
import json, os, urllib.request

want = int(os.environ["WANT_HOSTS"])
url = "http://127.0.0.1:1111/services/cluster-monitor/meta"

class PutRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if code not in (301, 302, 303, 307, 308):
            return None
        return urllib.request.Request(
            newurl,
            data=req.data,
            method=req.get_method(),
            headers={k: v for k, v in req.header_items() if k.lower() not in ("host", "content-length")},
        )

opener = urllib.request.build_opener(PutRedirect)
with opener.open(url, timeout=10) as resp:
    meta = json.load(resp)
data = meta.get("data") or {}
if isinstance(data, str):
    data = json.loads(data)
data["hosts"] = want
data["enabled"] = True
body = json.dumps({"index": meta["index"], "data": data}).encode()
req = urllib.request.Request(
    url, data=body, method="PUT", headers={"Content-Type": "application/json"}
)
with opener.open(req, timeout=10) as resp:
    out = json.load(resp)
print("cluster-monitor hosts=%s index=%s" % (want, out.get("index")))
PY
EOF
}

# Drain an HA node (not node1). Demote if it is a Raft peer, stop its
# jobs while flynn-host is still up (systemd stop does not kill containers),
# then stop the daemon so the scheduler reschedules. The VM stays until
# topology teardown.
step_remove_cluster_node() {
  local drop ip i
  if [[ "${#NODES[@]}" -lt 3 ]]; then
    echo "remove-node requires a running HA cluster (got ${#NODES[@]} hosts)" >&2
    return 1
  fi
  drop="$(pick_remove_node)"
  ip=""
  for i in "${!NODES[@]}"; do
    if [[ "${NODES[$i]}" == "${drop}" ]]; then
      ip="${NODE_IPS[$i]}"
      break
    fi
  done
  if [[ -z "${ip}" ]]; then
    echo "could not resolve IP for drain target ${drop}" >&2
    return 1
  fi
  if [[ "${drop}" == "node1" ]]; then
    echo "refusing to remove node1 (CLI/bootstrap host)" >&2
    return 1
  fi
  printf '%s\n' "${drop}" > "${WORK_DIR}/drained-node"
  info "removing ${drop} (${ip}) from stable cluster"
  # Graceful Raft demote while the peer is still reachable; --force if it is not.
  if ! node_root_script node1 <<EOF
set -euo pipefail
flynn-host demote "${ip}"
EOF
  then
    info "graceful demote failed; retrying flynn-host demote --force ${ip}"
    node_root_script node1 <<EOF
set -euo pipefail
flynn-host demote --force "${ip}"
EOF
  fi
  drain_host_jobs "${drop}"
  node_root_script "${drop}" <<'EOF'
set -euo pipefail
systemctl stop flynn-host.service || true
systemctl disable flynn-host.service || true
EOF
  kill_leftover_containers "${drop}"
  drop_live_node "${drop}"
  if ! wait_for "flynn-host list without ${ip}" 180 host_addr_gone "${ip}"; then
    echo "removed host ${drop} (${ip}) still listed in flynn-host list" >&2
    node_ssh node1 'sudo flynn-host list' || true
    return 1
  fi
  if ! wait_for "remaining hosts listed (${#NODES[@]})" 120 hosts_listed_exactly "${#NODES[@]}"; then
    echo "expected ${#NODES[@]} live hosts after removing ${drop}" >&2
    node_ssh node1 'sudo flynn-host list' || true
    return 1
  fi
  if ! wait_for "discoverd jobs gone from ${drop}" 180 discoverd_host_jobs_gone "${drop}"; then
    echo "discoverd still advertises jobs from ${drop}" >&2
    return 1
  fi
  local svc
  for svc in postgres mariadb mongodb; do
    if ! wait_for "${svc} primary not on ${drop}" 300 sirenia_primary_not_on_host "${svc}" "${drop}"; then
      echo "${svc} primary still on drained host ${drop}" >&2
      return 1
    fi
  done
  if ! wait_for "overlay after removing ${drop}" 180 overlay_peers_reachable; then
    dump_overlay_diagnostics
    return 1
  fi
  bounce_controller_scheduler
  sync_cluster_monitor_hosts
  echo "removed ${drop} (${ip}); cluster hosts=${#NODES[@]} peer-ips=${PEER_IPS}"
}

teardown_cluster_nodes() {
  local victims=()
  if [[ ${#TEARDOWN_NODES[@]} -gt 0 ]]; then
    victims=("${TEARDOWN_NODES[@]}")
  else
    victims=("${NODES[@]}")
  fi
  info "destroying cluster nodes: ${victims[*]}"
  vagrant destroy -f "${victims[@]}"
  CLUSTER_STARTED=0
  echo "cluster nodes destroyed (${TOPOLOGY_LABEL})"
}

teardown_builder() {
  info "destroying builder"
  vagrant destroy -f builder
  BUILDER_STARTED=0
  echo "builder destroyed"
}

teardown_vms() {
  if [[ "${KEEP_VMS}" == "1" ]]; then
    echo "keeping VMs (KEEP_VMS=1)"
    return 0
  fi
  teardown_cluster_nodes
  if [[ "${KEEP_BUILDER}" != "1" ]]; then
    teardown_builder
  else
    echo "keeping builder (KEEP_BUILDER=1)"
  fi
  echo "teardown done"
}

# nodeN → 192.168.56.(19+N). Host-only /24 last octet must stay <= 254.
cluster_node_ip() {
  echo "${CLUSTER_NET_PREFIX}.$((CLUSTER_IP_OFFSET + $1))"
}

cluster_ip_octet() {
  echo $((CLUSTER_IP_OFFSET + $1))
}

# 1 = singleton; >=3 = HA. 2 is Flynn-invalid. Optional SMOKE_MAX_NODES ceiling.
valid_topology_size() {
  local size=$1 octet
  [[ "${size}" =~ ^[0-9]+$ ]] || return 1
  if [[ "${size}" -lt 1 || "${size}" -eq 2 ]]; then
    return 1
  fi
  octet="$(cluster_ip_octet "${size}")"
  if [[ "${octet}" -gt 254 ]]; then
    return 1
  fi
  if [[ -n "${SMOKE_MAX_NODES:-}" && "${size}" -gt "${SMOKE_MAX_NODES}" ]]; then
    return 1
  fi
  if [[ "${size}" -eq 1 || "${size}" -ge 3 ]]; then
    return 0
  fi
  return 1
}

# add = 3-node then join node4; remove = 3-node then drain node3.
normalize_topology_spec() {
  case "$1" in
    add|add-node|3+1) echo add ;;
    remove|remove-node|3-1) echo remove ;;
    *) echo "$1" ;;
  esac
}

valid_topology_spec() {
  local spec
  spec="$(normalize_topology_spec "$1")"
  case "${spec}" in
    add)
      if [[ -n "${SMOKE_MAX_NODES:-}" && 4 -gt "${SMOKE_MAX_NODES}" ]]; then
        return 1
      fi
      return 0
      ;;
    remove)
      if [[ -n "${SMOKE_MAX_NODES:-}" && 3 -gt "${SMOKE_MAX_NODES}" ]]; then
        return 1
      fi
      return 0
      ;;
    *) valid_topology_size "${spec}" ;;
  esac
}

# How many Vagrant nodeN machines this spec needs (add reserves node4).
topology_inventory_size() {
  local spec
  spec="$(normalize_topology_spec "$1")"
  case "${spec}" in
    add) echo 4 ;;
    remove) echo 3 ;;
    *) echo "${spec}" ;;
  esac
}

# Highest nodeN already present under .vagrant/machines (leftover VMs).
discover_vagrant_node_count() {
  local d n max=0
  for d in "${ROOT}/.vagrant/machines"/node*; do
    [[ -d "${d}" ]] || continue
    n="${d##*/node}"
    [[ "${n}" =~ ^[0-9]+$ ]] || continue
    if [[ "${n}" -gt "${max}" ]]; then
      max="${n}"
    fi
  done
  echo "${max}"
}

# Build node1..N names/IPs and export FLYNN_MAX_NODES for the Vagrantfile loop.
expand_cluster_inventory() {
  local need=0 t discovered n
  if [[ ${#TOPOLOGIES[@]} -gt 0 ]]; then
    for t in "${TOPOLOGIES[@]}"; do
      n="$(topology_inventory_size "${t}")"
      if [[ "${n}" -gt "${need}" ]]; then
        need="${n}"
      fi
    done
  fi
  discovered="$(discover_vagrant_node_count)"
  if [[ "${discovered}" -gt "${need}" ]]; then
    need="${discovered}"
  fi
  if [[ "${need}" -lt 1 ]]; then
    need=1
  fi
  FLYNN_MAX_NODES="${need}"
  export FLYNN_MAX_NODES
  ALL_CLUSTER_NODES=()
  ALL_CLUSTER_IPS=()
  local i
  for i in $(seq 1 "${need}"); do
    ALL_CLUSTER_NODES+=("node${i}")
    ALL_CLUSTER_IPS+=("$(cluster_node_ip "${i}")")
  done
  NODE1_IP="$(cluster_node_ip 1)"
  SHARED_LOG_DIRS=(builder "${ALL_CLUSTER_NODES[@]}")
}

parse_smoke_topologies() {
  local raw item parts
  raw="${SMOKE_TOPOLOGIES// /}"
  if [[ "${raw}" == "both" ]]; then
    raw="1,3"
  fi
  TOPOLOGIES=()
  IFS=',' read -r -a parts <<< "${raw}"
  for item in "${parts[@]}"; do
    [[ -z "${item}" ]] && continue
    if ! valid_topology_spec "${item}"; then
      echo "SMOKE_TOPOLOGIES sizes must be 1 or >=3 (not 2), or add/remove; got '${item}' in '${SMOKE_TOPOLOGIES}'" >&2
      return 1
    fi
    item="$(normalize_topology_spec "${item}")"
    # bash 3.2 + set -u treats ${arr[*]} on an empty array as unbound.
    if [[ ${#TOPOLOGIES[@]} -gt 0 && " ${TOPOLOGIES[*]} " == *" ${item} "* ]]; then
      continue
    fi
    TOPOLOGIES+=("${item}")
  done
  if [[ ${#TOPOLOGIES[@]} -eq 0 ]]; then
    echo "SMOKE_TOPOLOGIES is empty (got '${SMOKE_TOPOLOGIES}')" >&2
    return 1
  fi
  if [[ ${#TOPOLOGIES[@]} -gt 1 ]]; then
    if [[ -n "${RESUME_AT}" ]]; then
      echo "RESUME_AT requires a single SMOKE_TOPOLOGIES value (got ${SMOKE_TOPOLOGIES})" >&2
      return 1
    fi
    if [[ "${SKIP_INSTALL}" == "1" ]]; then
      echo "SKIP_INSTALL requires a single SMOKE_TOPOLOGIES value (got ${SMOKE_TOPOLOGIES})" >&2
      return 1
    fi
    if [[ "${SKIP_DEPLOY}" == "1" || "${SKIP_VERIFY_BEFORE}" == "1" ]]; then
      echo "SKIP_DEPLOY/SKIP_VERIFY_BEFORE require a single SMOKE_TOPOLOGIES value (got ${SMOKE_TOPOLOGIES})" >&2
      return 1
    fi
  fi
  expand_cluster_inventory
}

apply_topology() {
  local size=$1 i
  if ! valid_topology_size "${size}"; then
    echo "unsupported topology size ${size} (want 1 or >=3, not 2)" >&2
    return 1
  fi
  TOPOLOGY_SIZE="${size}"
  NODES=()
  NODE_IPS=()
  for i in $(seq 1 "${size}"); do
    NODES+=("node${i}")
    NODE_IPS+=("$(cluster_node_ip "${i}")")
  done
  TOPOLOGY_LABEL="${size}-node"
  local saved_ifs="${IFS}"
  IFS=,
  PEER_IPS="${NODE_IPS[*]}"
  IFS="${saved_ifs}"
  MIN_HOSTS="${size}"
  CHECK_PHASE_PREFIX="${TOPOLOGY_LABEL}/"
  TEARDOWN_NODES=("${NODES[@]}")
  TOPOLOGY_ACTION=""
}

apply_topology_spec() {
  local spec
  spec="$(normalize_topology_spec "$1")"
  TOPOLOGY_ACTION=""
  case "${spec}" in
    add)
      apply_topology 3
      TOPOLOGY_ACTION=add
      TOPOLOGY_LABEL="3-node-add"
      CHECK_PHASE_PREFIX="${TOPOLOGY_LABEL}/"
      # run_step is a subshell; record node4 here so teardown still destroys it.
      remember_teardown_node node4
      ;;
    remove)
      apply_topology 3
      TOPOLOGY_ACTION=remove
      TOPOLOGY_LABEL="3-node-remove"
      CHECK_PHASE_PREFIX="${TOPOLOGY_LABEL}/"
      ;;
    *)
      apply_topology "${spec}"
      ;;
  esac
}

# One full install → bootstrap → plugin install → deploy → verify → CLI →
# --force upgrades → cluster backup → --clean reinstall → bootstrap
# --from-backup (plugins restore with postgres) → re-verify.
# idx is 0-based; is_last=1 means KEEP_VMS can retain these cluster nodes.
run_one_topology() {
  local size=$1
  local idx=$2
  local is_last=$3
  local pass
  apply_topology_spec "${size}"
  info "topology ${TOPOLOGY_LABEL} ($((idx + 1))/${#TOPOLOGIES[@]}): nodes=${NODES[*]} peer-ips=${PEER_IPS} min-hosts=${MIN_HOSTS} action=${TOPOLOGY_ACTION:-none}"
  clear_cluster_shared_logs

  if [[ "${SKIP_VAGRANT_UP}" == "1" && "${idx}" -eq 0 ]]; then
    local n
    for n in "${NODES[@]}"; do
      require_vm_running_for_skip "${n}"
    done
    record "Vagrant up (cluster nodes) (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_VAGRANT_UP=1"
    CLUSTER_STARTED=1
    cache_all_node_ssh_configs
  else
    run_step "Vagrant up (cluster nodes) (${TOPOLOGY_LABEL})" step_vagrant_up_nodes
    CLUSTER_STARTED=1
    cache_all_node_ssh_configs
  fi

  if [[ "${SKIP_INSTALL}" == "1" && "${RESUME_BOOTSTRAP:-0}" != "1" ]]; then
    record "Install local tarball on nodes (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_INSTALL=1"
    record "Init layer-0 (peer-ips) (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_INSTALL=1"
    record "Bootstrap cluster (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_INSTALL=1"
    ensure_flynn_cli_on_node1
    configure_node_dns node1
    if [[ "${RESUME_AT}" == "restore" ]]; then
      echo "RESUME_AT=restore: skipping CLI cluster add (cluster will be --clean reinstalled)"
    else
      register_cli_cluster || fail_shutdown "Bootstrap cluster (${TOPOLOGY_LABEL})" 0 "SKIP_INSTALL=1 but CLI cluster registration failed"
    fi
  elif [[ "${RESUME_BOOTSTRAP:-0}" == "1" ]]; then
    record "Install local tarball on nodes (${TOPOLOGY_LABEL})" "SKIP" 0 "RESUME_AT=bootstrap"
    record "Init layer-0 (peer-ips) (${TOPOLOGY_LABEL})" "SKIP" 0 "RESUME_AT=bootstrap"
    info "resume: verifying layer-0 host APIs before bootstrap"
    if ! wait_for "flynn-host HTTP API on ${PEER_IPS}" 120 hosts_api_ready; then
      dump_layer0_diagnostics
      fail_shutdown "Init layer-0 (peer-ips) (${TOPOLOGY_LABEL})" 0 "RESUME_AT=bootstrap but host APIs not ready"
    fi
    run_step "Bootstrap cluster (${TOPOLOGY_LABEL})" step_bootstrap
  else
    run_step "Install local tarball on nodes (${TOPOLOGY_LABEL})" step_install_flynn
    run_step "Init layer-0 (peer-ips) (${TOPOLOGY_LABEL})" step_init_cluster
    run_step "Bootstrap cluster (${TOPOLOGY_LABEL})" step_bootstrap
  fi

  if [[ "${SKIP_PLUGIN_INSTALL}" == "1" ]]; then
    record "Install plugins (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_PLUGIN_INSTALL=1"
  else
    run_step "Install plugins (${TOPOLOGY_LABEL})" step_install_plugins
  fi

  if [[ "${SKIP_DEPLOY}" == "1" ]]; then
    record "Deploy app + DB resources (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_DEPLOY=1"
    record "Deploy Dockerfile app (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_DEPLOY=1"
  else
    run_step "Deploy app + DB resources (${TOPOLOGY_LABEL})" step_deploy_app
    run_step "Deploy Dockerfile app (${TOPOLOGY_LABEL})" step_deploy_docker_app
  fi
  if [[ "${SKIP_VERIFY_BEFORE}" == "1" ]]; then
    record "Verify app/DBs before upgrade (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_VERIFY_BEFORE=1"
  else
    run_step "Verify app/DBs before upgrade (${TOPOLOGY_LABEL})" step_verify_before
  fi
  if [[ "${SKIP_CLI}" == "1" || "${SKIP_VERIFY_BEFORE}" == "1" ]]; then
    record "CLI functions (pre-upgrade) (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_CLI/SKIP_VERIFY_BEFORE=1"
  else
    run_step "CLI functions (pre-upgrade) (${TOPOLOGY_LABEL})" step_cli_functions pre-upgrade
  fi

  if [[ "${TOPOLOGY_ACTION}" == "add" ]]; then
    run_step "Add node to running cluster (${TOPOLOGY_LABEL})" step_add_cluster_node
    # run_step is a subshell; re-apply live membership in this shell for CLI/overlay.
    append_live_node node4
    run_step "Verify app/DBs after add-node (${TOPOLOGY_LABEL})" step_verify_membership after-add
    if [[ "${SKIP_CLI}" == "1" ]]; then
      record "CLI functions after add-node (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_CLI=1"
    else
      run_step "CLI functions after add-node (${TOPOLOGY_LABEL})" step_cli_functions after-add
    fi
  elif [[ "${TOPOLOGY_ACTION}" == "remove" ]]; then
    run_step "Remove node from running cluster (${TOPOLOGY_LABEL})" step_remove_cluster_node
    drop_live_node "$(cat "${WORK_DIR}/drained-node")"
    run_step "Verify app/DBs after remove-node (${TOPOLOGY_LABEL})" step_verify_membership after-remove
    if [[ "${SKIP_CLI}" == "1" ]]; then
      record "CLI functions after remove-node (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_CLI=1"
    else
      run_step "CLI functions after remove-node (${TOPOLOGY_LABEL})" step_cli_functions after-remove
    fi
  fi

  if [[ "${SKIP_UPGRADE}" == "1" ]]; then
    record "Upgrade --all-nodes (local tarball) (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_UPGRADE=1"
    record "Verify app/DBs after upgrade (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_UPGRADE=1"
  else
    for pass in $(seq 1 "${UPGRADE_PASSES}"); do
      run_step "Upgrade pass ${pass}/${UPGRADE_PASSES} --all-nodes (${TOPOLOGY_LABEL})" step_upgrade "${pass}"
      run_step "Verify app/DBs after upgrade ${pass}/${UPGRADE_PASSES} (${TOPOLOGY_LABEL})" step_verify_after "${pass}"
      if [[ "${SKIP_CLI}" == "1" ]]; then
        record "CLI functions after upgrade ${pass}/${UPGRADE_PASSES} (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_CLI=1"
      else
        run_step "CLI functions after upgrade ${pass}/${UPGRADE_PASSES} (${TOPOLOGY_LABEL})" \
          step_cli_functions "post-upgrade-${pass}"
      fi
    done
  fi

  if [[ "${SKIP_BACKUP}" == "1" ]]; then
    record "Cluster backup (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_BACKUP=1"
    record "Reinstall for restore (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_BACKUP=1"
    record "Init layer-0 for restore (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_BACKUP=1"
    record "Bootstrap from backup (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_BACKUP=1"
    record "Verify app/DBs after restore (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_BACKUP=1"
    record "CLI functions after restore (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_BACKUP=1"
  else
    if [[ "${RESUME_AT}" == "restore" ]]; then
      record "Cluster backup (${TOPOLOGY_LABEL})" "SKIP" 0 "RESUME_AT=restore"
    else
      run_step "Cluster backup (${TOPOLOGY_LABEL})" step_cluster_backup
    fi
    restore_drained_inventory
    run_step "Reinstall for restore (${TOPOLOGY_LABEL})" step_install_flynn
    run_step "Init layer-0 for restore (${TOPOLOGY_LABEL})" step_init_cluster
    run_step "Bootstrap from backup (${TOPOLOGY_LABEL})" step_bootstrap_from_backup
    run_step "Verify app/DBs after restore (${TOPOLOGY_LABEL})" step_verify_after_restore
    if [[ "${SKIP_CLI}" == "1" ]]; then
      record "CLI functions after restore (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_CLI=1"
    else
      run_step "CLI functions after restore (${TOPOLOGY_LABEL})" step_cli_functions post-restore
    fi
  fi

  if [[ "${KEEP_VMS}" == "1" && "${is_last}" -eq 1 ]]; then
    record "Teardown cluster nodes (${TOPOLOGY_LABEL})" "SKIP" 0 "KEEP_VMS=1"
  else
    if [[ "${KEEP_VMS}" == "1" && "${is_last}" -ne 1 ]]; then
      info "KEEP_VMS=1: destroying ${TEARDOWN_NODES[*]} before next topology; last topology VMs will be kept"
    fi
    run_step "Teardown cluster nodes (${TOPOLOGY_LABEL})" teardown_cluster_nodes
  fi
}

main() {
  if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
  fi

  require_bin vagrant curl python3 git
  if [[ "${SKIP_UNIT_TESTS}" != "1" ]]; then
    require_bin go
  fi

  if [[ -z "${BUILD_VERSION}" ]]; then
    BUILD_VERSION="$(default_build_version)"
  fi

  parse_smoke_topologies || fail_shutdown "Parse SMOKE_TOPOLOGIES" 0 "invalid SMOKE_TOPOLOGIES=${SMOKE_TOPOLOGIES}"

  ui_session_begin
  if [[ "${SMOKE_DETAIL}" == "1" ]]; then
    _UI_COLLAPSE_BODY=0
  else
    _UI_COLLAPSE_BODY=1
    smoke_prepare_tty
  fi
  info "local build smoke: version=${BUILD_VERSION} topologies=${SMOKE_TOPOLOGIES}"
  if [[ "${SMOKE_DETAIL}" == "1" ]]; then
    info "command output live (SMOKE_DETAIL=1)"
  else
    info "command output collapsed — Ctrl+R expands the current step (SMOKE_DETAIL=1 for always on)"
  fi

  # RESUME_AT=bootstrap: pick up after a failed/aborted run when layer-0 is already up.
  if [[ "${RESUME_AT}" == "bootstrap" ]]; then
    SKIP_VAGRANT_UP=1
    SKIP_BUILD=1
    SKIP_INSTALL=1
    # Re-enable bootstrap despite SKIP_INSTALL's usual "skip all cluster setup".
    RESUME_BOOTSTRAP=1
  fi
  if [[ "${RESUME_AT}" == "upgrade" ]]; then
    SKIP_VAGRANT_UP=1
    SKIP_BUILD=1
    SKIP_INSTALL=1
    SKIP_DEPLOY=1
    SKIP_VERIFY_BEFORE=1
  fi
  if [[ "${RESUME_AT}" == "backup" ]]; then
    SKIP_VAGRANT_UP=1
    SKIP_BUILD=1
    SKIP_INSTALL=1
    SKIP_PLUGIN_INSTALL=1
    SKIP_DEPLOY=1
    SKIP_VERIFY_BEFORE=1
    SKIP_UPGRADE=1
  fi
  if [[ "${RESUME_AT}" == "restore" ]]; then
    SKIP_VAGRANT_UP=1
    SKIP_BUILD=1
    SKIP_INSTALL=1
    SKIP_PLUGIN_INSTALL=1
    SKIP_DEPLOY=1
    SKIP_VERIFY_BEFORE=1
    SKIP_UPGRADE=1
  fi

  # Fail before touching VMs or wiping flynn-logs if the host-side tree is broken.
  if [[ "${SKIP_UNIT_TESTS}" == "1" ]]; then
    record "Host unit tests (pre-cluster)" "SKIP" 0 "SKIP_UNIT_TESTS=1"
    record_unit_check "gate" "host" "SKIP" "SKIP_UNIT_TESTS=1"
  else
    run_step "Host unit tests (pre-cluster)" step_host_unit_tests
  fi

  clear_shared_logs

  if [[ "${SKIP_VAGRANT_UP}" == "1" ]]; then
    require_vm_running_for_skip builder
    record "Vagrant up (builder)" "SKIP" 0 "SKIP_VAGRANT_UP=1"
    cache_node_ssh_config builder
    BUILDER_STARTED=1
  else
    run_step "Vagrant up (builder)" step_vagrant_up_builder
    BUILDER_STARTED=1
    cache_node_ssh_config builder
  fi

  if [[ "${SKIP_UNIT_TESTS}" == "1" || "${SKIP_BUILDER_UNIT_TESTS}" == "1" ]]; then
    record "Builder unit tests (pre-cluster)" "SKIP" 0 "SKIP_UNIT_TESTS/SKIP_BUILDER_UNIT_TESTS=1"
    record_unit_check "builder" "script/run-unit-tests" "SKIP" "skipped"
  else
    run_step "Builder unit tests (pre-cluster)" step_builder_unit_tests
  fi

  if [[ "${SKIP_BUILD}" == "1" ]]; then
    record "Build Flynn on builder (${BUILD_VERSION})" "SKIP" 0 "SKIP_BUILD=1"
    if ! resolve_built_tarball >/dev/null; then
      fail_shutdown "Build Flynn on builder (${BUILD_VERSION})" 0 \
        "SKIP_BUILD=1 but tarball missing: $(tarball_host_path)"
    fi
  else
    run_step "Build Flynn on builder (${BUILD_VERSION})" step_build_on_builder
  fi

  local idx topo is_last
  for idx in "${!TOPOLOGIES[@]}"; do
    topo="${TOPOLOGIES[$idx]}"
    is_last=0
    if [[ $((idx + 1)) -eq ${#TOPOLOGIES[@]} ]]; then
      is_last=1
    fi
    run_one_topology "${topo}" "${idx}" "${is_last}"
  done

  if [[ "${KEEP_VMS}" == "1" ]]; then
    record "Teardown builder" "SKIP" 0 "KEEP_VMS=1"
  elif [[ "${KEEP_BUILDER}" == "1" ]]; then
    record "Teardown builder" "SKIP" 0 "KEEP_BUILDER=1"
  else
    run_step "Teardown builder" teardown_builder
  fi

  smoke_stop_detail_tail
  smoke_restore_tty
  _UI_COLLAPSE_BODY=0
  print_results_table
  exit "${OVERALL_FAILED}"
}

main "$@"
