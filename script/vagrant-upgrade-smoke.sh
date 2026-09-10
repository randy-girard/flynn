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
#      datastore provider, verify HTTP/status/rows, exercise flynn /
#      flynn-host, run flynn-host update --all-nodes --tarball --force twice,
#      re-verify, then destroy the cluster nodes (builder is kept) before the
#      next topology. Sizes are 1 (singleton) or >=3 (HA); 2 is invalid.
#      Vagrant nodes are generated as node1..max(N) — e.g. 1,3,5 or 1,3,7.
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
#   APP_NAME             Test app name [default: upgrade-smoke]
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
#   SKIP_UPGRADE=1       Skip the local tarball --all-nodes update passes
#   SKIP_CLI=1           Skip live flynn / flynn-host CLI function steps
#   SMOKE_TOPOLOGIES     Comma-separated cluster sizes to run, each getting
#                        the full install/bootstrap/deploy/verify/upgrade/CLI
#                        path. 1 = singleton (--min-hosts 1, node1 only);
#                        N>=3 = HA (--min-hosts N, node1..nodeN). 2 is
#                        invalid (Flynn). [default: 1,3]
#                        CLUSTER_SIZE=N is a shortcut for one topology.
#                        Example: SMOKE_TOPOLOGIES=1,3,5
#   SMOKE_MAX_NODES      Optional ceiling on N (host-only /24 already caps
#                        node IPs at 192.168.56.254). Unset = no extra cap.
#   UPGRADE_PASSES=N     How many --force tarball updates to run [default: 2]
#   SMOKE_SEED_ROWS=N    Dummy rows/keys seeded per datastore [default: 200]
#   SMOKE_BLOB_COUNT=N   Extra slug files embedded in the test app [default: 100]
#   SMOKE_DETAIL=1       Stream command output live (default: hide it; STEP/
#                        STEP OK/WARN stay visible; Ctrl+R expands the log
#                        without echoing ^R)
#   SKIP_TEARDOWN=1      Alias for KEEP_VMS=1
#

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${ROOT}"

source "${ROOT}/script/lib/ui.sh"

BUILD_VERSION="${BUILD_VERSION:-}"
BUILD_PHASE="${BUILD_PHASE:-auto}"
CLUSTER_DOMAIN="${CLUSTER_DOMAIN:-upgrade-smoke.localflynn.com}"
APP_NAME="${APP_NAME:-upgrade-smoke}"
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
NODES=(node1)
NODE_IPS=("${NODE1_IP}")
PEER_IPS="${NODE1_IP}"
MIN_HOSTS=1
TOPOLOGY_SIZE=1
TOPOLOGY_LABEL="1-node"
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
# Host-side packages that compile without Linux netlink/ZFS. Run before Vagrant
# so a broken CLI/datastore change cannot burn a 3-node cluster boot.
SMOKE_UNIT_PACKAGES=(
  ./cli/
  ./controller/types/
  ./pkg/updaterdeploy/
  ./pkg/sirenia/state/
  ./pkg/iptables/
  ./appliance/clickhouse/
  ./appliance/redis/
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

record() {
  local name=$1 status=$2 seconds=$3 detail=${4:-}
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
  detail="${detail//$'\t'/ }"
  detail="${detail//$'\n'/ }"
  printf '%s\t%s\t%s\t%s\n' "${phase}" "${name}" "${status}" "${detail}" >> "${CHECK_FILE}"
  if [[ "${status}" != "PASS" && "${status}" != "SKIP" ]]; then
    OVERALL_FAILED=1
  fi
}

# Host unit-test gate. Same file-backed pattern as record_check (run_step is a
# subshell). Failures here abort before Vagrant up.
record_unit_check() {
  local kind=$1 name=$2 status=$3 detail=${4:-}
  detail="${detail//$'\t'/ }"
  detail="${detail//$'\n'/ }"
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
  awk 'NF { p=$0 } END { print p }' "${LAST_STEP_LOG}" | tr '\n' ' ' | cut -c1-160
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
    echo "${err}" | cut -c1-200
    return
  fi
  echo "${detail}" | tr '\n' ' ' | cut -c1-160
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
  printf "| %-8s | %-48s | %-6s | %s\n" "Kind" "Check" "Status" "Detail"
  printf "|----------|--------------------------------------------------|--------|%s\n" "----------------------------------------"
  local kind name status detail failed=0 total=0
  while IFS=$'\t' read -r kind name status detail; do
    [[ -z "${kind}" ]] && continue
    total=$((total + 1))
    if [[ "${status}" != "PASS" && "${status}" != "SKIP" ]]; then
      failed=$((failed + 1))
      OVERALL_FAILED=1
    fi
    printf "| %-8s | %-48s | %s | %s\n" "${kind}" "${name}" "$(ui_status_text "${status}")" "${detail}"
  done < "${UNIT_CHECK_FILE}"
  local tot_status="PASS" tot_detail="all host unit tests passed"
  if [[ ${failed} -ne 0 ]]; then
    tot_status="FAIL"
    tot_detail="${failed} failed"
  fi
  printf "| %-8s | %-48s | %s | %s\n" "TOTAL" "${total} checks" "$(ui_status_text "${tot_status}")" "${tot_detail}"
  ui_banner "================================================================================"
}

print_datastore_report() {
  echo
  ui_banner "================================================================================"
  ui_banner " App, CLI & datastore persistence"
  echo " app=${APP_NAME}  seed_rows=${SMOKE_SEED_ROWS}  blobs=${SMOKE_BLOB_COUNT}  passes=${UPGRADE_PASSES}"
  echo " topologies=${SMOKE_TOPOLOGIES}  providers=${DATASTORE_PROVIDERS[*]}"
  ui_banner "================================================================================"
  if [[ ! -s "${CHECK_FILE}" ]]; then
    echo " (no app/datastore checks recorded — verify steps did not run)"
    echo "================================================================================"
    return
  fi
  printf "| %-24s | %-16s | %-6s | %s\n" "Phase" "Check" "Status" "Detail"
  printf "|--------------------------|------------------|--------|%s\n" "----------------------------------------"
  local phase name status detail failed=0 total=0
  while IFS=$'\t' read -r phase name status detail; do
    [[ -z "${phase}" ]] && continue
    total=$((total + 1))
    if [[ "${status}" != "PASS" && "${status}" != "SKIP" ]]; then
      failed=$((failed + 1))
      OVERALL_FAILED=1
    fi
    printf "| %-24s | %-16s | %s | %s\n" "${phase}" "${name}" "$(ui_status_text "${status}")" "${detail}"
  done < "${CHECK_FILE}"
  local tot_status="PASS" tot_detail="all app/CLI/DB checks passed"
  if [[ ${failed} -ne 0 ]]; then
    tot_status="FAIL"
    tot_detail="${failed} failed"
  fi
  printf "| %-24s | %-16s | %s | %s\n" "TOTAL" "${total} checks" "$(ui_status_text "${tot_status}")" "${tot_detail}"
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
  echo " build=${BUILD_VERSION:-n/a}  domain=${CLUSTER_DOMAIN}  app=${APP_NAME}"
  echo " topologies=${SMOKE_TOPOLOGIES}  seed_rows=${SMOKE_SEED_ROWS}  blobs=${SMOKE_BLOB_COUNT}  upgrade_passes=${UPGRADE_PASSES}"
  if [[ -n "${BUILT_TARBALL}" ]]; then
    echo " tarball=${BUILT_TARBALL}"
  fi
  ui_banner "================================================================================"
  printf "| %-48s | %-6s | %8s | %s\n" "Step" "Status" "Duration" "Detail"
  printf "|--------------------------------------------------|--------|----------|%s\n" "----------------------------------------"
  local i
  for i in "${!RESULT_NAMES[@]}"; do
    printf "| %-48s | %s | %7ss | %s\n" \
      "${RESULT_NAMES[$i]}" \
      "$(ui_status_text "${RESULT_STATUS[$i]}")" \
      "${RESULT_SECONDS[$i]}" \
      "${RESULT_DETAIL[$i]}"
  done
  printf "| %-48s | %s | %7ss | %s\n" "OVERALL" "$(ui_status_text "${overall}")" "${total}" ""
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

smoke_restore_tty() {
  if [[ -n "${SMOKE_STTY_SAVED:-}" ]] && [[ -e /dev/tty ]]; then
    stty "${SMOKE_STTY_SAVED}" < /dev/tty 2>/dev/null || true
  fi
  SMOKE_STTY_SAVED=""
}

# Consume Ctrl+R ourselves. The default tty "rprnt" character is also Ctrl+R
# and reprints as a literal ^R; a SIGINFO rebind does not work in Cursor's
# terminal, so we disable rprnt/echo and read the byte from /dev/tty.
smoke_prepare_tty() {
  [[ "${SMOKE_DETAIL}" == "1" ]] && return 0
  [[ -e /dev/tty ]] || return 0
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
  if [[ -e /dev/tty ]] && [[ -f "${LAST_STEP_LOG}" ]]; then
    tail -n 80 -f "${LAST_STEP_LOG}" >/dev/tty 2>/dev/null &
    SMOKE_DETAIL_TAIL_PID=$!
  fi
}

# Non-blocking-enough poll: bash 3.2 read -t is whole seconds. Timeout must
# not trip the ERR trap. Swallow the key so it never echoes as ^R.
smoke_poll_detail_key() {
  local key=""
  [[ -e /dev/tty ]] || return 0
  read -t 1 -n 1 -s key < /dev/tty || true
  if [[ "${key}" == $'\x12' ]]; then
    smoke_toggle_detail
  fi
}

smoke_wait_collapsed_step() {
  local wrapper=$1
  if [[ ! -e /dev/tty ]]; then
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
      hosts_body+="${ip} ${CLUSTER_DOMAIN} controller.${CLUSTER_DOMAIN} git.${CLUSTER_DOMAIN} images.${CLUSTER_DOMAIN} dashboard.${CLUSTER_DOMAIN} status.${CLUSTER_DOMAIN} ${APP_NAME}.${CLUSTER_DOMAIN}"$'\n'
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
  # Allow remote subnet routes / FDB entries to settle, then require reachability.
  sleep 20
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
    esac
  done
}

ensure_flynn_cli_on_node1() {
  # A present-but-wrong-arch binary (e.g. amd64 on an arm64 VM) exists yet
  # fails with "Exec format error", so test execution, not just presence.
  if node_ssh node1 'flynn version >/dev/null 2>&1'; then
    return 0
  fi
  info "installing Flynn CLI on node1"
  # Prefer a CLI binary from the local build (synced folder), matching the
  # node's architecture; fall back to the published installer.
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

step_host_unit_tests() {
  local failed=0 start elapsed rc
  local script pkg name
  local packages=( "${SMOKE_UNIT_PACKAGES[@]}" )

  echo "host unit-test gate: smoke regressions + Darwin-safe Go packages"
  echo "failures abort before Vagrant up / cluster build"

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
    packages+=( ./flannel/backend/vxlan/ )
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
export GOFLAGS=-mod=vendor
export FLYNN_TEST_DOCKER=0
export FLYNN_TEST_SKIP_CHECKS="${FLYNN_TEST_SKIP_CHECKS:-1}"
export PGHOST="\${PGHOST:-/var/run/postgresql}"
export PGSSLMODE="\${PGSSLMODE:-disable}"
cd "${REPO_IN_VM}"

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
if [[ ! -x /usr/local/go/bin/go ]]; then
  echo "Go toolchain missing on builder; run: vagrant provision builder" >&2
  exit 1
fi

transient_build_failure() {
  grep -qE 'Failed to fetch|Hash Sum mismatch|Temporary failure resolving|Connection timed out|Could not resolve|Network is unreachable|502 Bad Gateway|503 Service|download.docker.com|Unable to lock directory|Could not get lock|I/O error|Connection reset|TLS handshake|the remote end hung up|Clearing|Splitting up|503  |504  |522 ' "\$1"
}

attempt=1
max="${FLYNN_BUILD_ATTEMPTS}"
log="/tmp/flynn-build-attempt.log"
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

step_install_flynn() {
  resolve_built_tarball
  local tarball_in_vm
  tarball_in_vm="$(tarball_vm_path)"

  local node
  for node in "${NODES[@]}"; do
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
for link in flannel.1 flynnbr0; do
  if ip link show "\${link}" &>/dev/null; then
    echo "removing stale \${link}"
    ip link del "\${link}" || true
  fi
done
EOF
    configure_node_dns "${node}"
    # Verify install actually produced a working flynn-host before continuing.
    node_root_script "${node}" <<'EOF'
set -euo pipefail
command -v flynn-host >/dev/null
flynn-host version
test -x /usr/bin/flynn-host || test -x /usr/local/bin/flynn-host
EOF
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

step_bootstrap() {
  local overlay_fail="${WORK_DIR}/overlay-fail.txt"
  local watch_pid=""
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
  --job-timeout "${BOOTSTRAP_JOB_TIMEOUT}"
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

  info "waiting for postgres primary read-write"
  # mariadb/mongodb are bootstrapped at scale 0; they have no primary until
  # the first `resource add mysql|mongodb`.
  if ! wait_datastores_ready "after bootstrap" postgres; then
    dump_overlay_diagnostics
    return 1
  fi
  echo "bootstrapped ${CLUSTER_DOMAIN} (${TOPOLOGY_LABEL} min-hosts=${MIN_HOSTS})"
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
  # ON CLUSTER DDL needs distributed_ddl in the server config (added in this
  # branch). The current tarball may not have it, so create the database on
  # the replica we talk to. MergeTree data still lives on /data and is what
  # the upgrade restart has to keep.
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
echo "seed complete"
EOF
  record_seed_counts
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

  count="$(numeric_count "$(flynn1 -a "${APP_NAME}" clickhouse client -- --query "SELECT count() FROM smoke_db.rows")")"
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
  count="$(numeric_count "$(flynn1 -a "${APP_NAME}" clickhouse client -- --query "SELECT count() FROM smoke_db.rows")")"
  if [[ -n "${count}" && "${count}" -ge "${rows}" ]]; then
    record_check "${label}" "clickhouse" "PASS" "rows=${count}"
  else
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

# Live flynn + flynn-host commands against the cluster. Unit tests cover CLI
# packages (./cli on the host gate, ./cli + ./host/cli on the builder); this
# catches controller/scheduler/logaggregator drift after an upgrade. Does not
# scale or env-set (those create releases and restart web).
step_cli_functions() {
  local label=$1
  local failed=0
  local out rc hosts

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

  rc=0
  out="$(node_ssh node1 "sudo -H timeout 90 flynn -a $(printf '%q' "${APP_NAME}") run -- echo smoke-cli" </dev/null)" || rc=$?
  if [[ "${rc}" -eq 0 ]] && echo "${out}" | grep -q 'smoke-cli'; then
    record_check "${label}" "cli-run" "PASS" "$(echo "${out}" | tr '\n' ' ' | cut -c1-80)"
    echo "cli ${label} cli-run: PASS"
  else
    record_check "${label}" "cli-run" "FAIL" "rc=${rc} $(echo "${out}" | tr '\n' ' ' | cut -c1-80)"
    echo "cli ${label} cli-run: FAIL rc=${rc} ${out}" >&2
    failed=1
  fi

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

  if [[ "${failed}" -ne 0 ]]; then
    echo "CLI function checks failed for ${label}" >&2
    return 1
  fi
  echo "cli ${label}: apps/ps/scale/env/resource/route/release/log/run/meta/host PASS"
}

step_verify_before() {
  wait_for "app HTTP pre-upgrade" 180 probe_app_http
  assert_app_http pre-upgrade
  wait_for "app /status pre-upgrade" 120 probe_app_status
  assert_app_status pre-upgrade
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
  assert_databases "${label}"
  node_ssh node1 'sudo flynn-host version' || true
}

teardown_cluster_nodes() {
  info "destroying cluster nodes: ${NODES[*]}"
  vagrant destroy -f "${NODES[@]}"
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
  local need=0 t discovered
  if [[ ${#TOPOLOGIES[@]} -gt 0 ]]; then
    for t in "${TOPOLOGIES[@]}"; do
      if [[ "${t}" -gt "${need}" ]]; then
        need="${t}"
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
    if ! valid_topology_size "${item}"; then
      echo "SMOKE_TOPOLOGIES sizes must be 1 or >=3 (not 2); got '${item}' in '${SMOKE_TOPOLOGIES}'" >&2
      return 1
    fi
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
}

# One full install → bootstrap → deploy → verify → CLI → --force upgrades.
# idx is 0-based; is_last=1 means KEEP_VMS can retain these cluster nodes.
run_one_topology() {
  local size=$1
  local idx=$2
  local is_last=$3
  local pass
  apply_topology "${size}"
  info "topology ${TOPOLOGY_LABEL} ($((idx + 1))/${#TOPOLOGIES[@]}): nodes=${NODES[*]} peer-ips=${PEER_IPS} min-hosts=${MIN_HOSTS}"
  clear_cluster_shared_logs

  if [[ "${SKIP_VAGRANT_UP}" == "1" && "${idx}" -eq 0 ]]; then
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
    register_cli_cluster || fail_shutdown "Bootstrap cluster (${TOPOLOGY_LABEL})" 0 "SKIP_INSTALL=1 but CLI cluster registration failed"
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

  if [[ "${SKIP_DEPLOY}" == "1" ]]; then
    record "Deploy app + DB resources (${TOPOLOGY_LABEL})" "SKIP" 0 "SKIP_DEPLOY=1"
  else
    run_step "Deploy app + DB resources (${TOPOLOGY_LABEL})" step_deploy_app
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

  if [[ "${KEEP_VMS}" == "1" && "${is_last}" -eq 1 ]]; then
    record "Teardown cluster nodes (${TOPOLOGY_LABEL})" "SKIP" 0 "KEEP_VMS=1"
  else
    if [[ "${KEEP_VMS}" == "1" && "${is_last}" -ne 1 ]]; then
      info "KEEP_VMS=1: destroying ${NODES[*]} before next topology; last topology VMs will be kept"
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

  # Fail before touching VMs or wiping flynn-logs if the host-side tree is broken.
  if [[ "${SKIP_UNIT_TESTS}" == "1" ]]; then
    record "Host unit tests (pre-cluster)" "SKIP" 0 "SKIP_UNIT_TESTS=1"
    record_unit_check "gate" "host" "SKIP" "SKIP_UNIT_TESTS=1"
  else
    run_step "Host unit tests (pre-cluster)" step_host_unit_tests
  fi

  clear_shared_logs

  if [[ "${SKIP_VAGRANT_UP}" == "1" ]]; then
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
