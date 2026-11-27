#!/bin/bash
# Behaviour of rolling reload and update in script/vagrant/vagrant.sh with
# stubbed VM and probe helpers. Does not boot a VM.
#
# Rolling reload must not reboot the first cluster node unless the cluster is
# already healthy (postgres sync caught up, every service at its formation
# count) and the tenant app list could be read: rebooting a node that holds
# the only good copy causes the outage the reload is meant to test for, and an
# empty list taken after the crash misses the apps whose jobs died with it.
# `update` must wait for full strength before the next command starts.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
script="${ROOT}/script/vagrant/vagrant.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
log="${tmp}/log"

fns=""
for f in wait_cluster_stable list_tenant_apps reload_preflight reload_cluster_rolling update_dev_cluster; do
  body="$(sed -n "/^${f}() {/,/^}/p" "${script}")"
  if [[ -z "${body}" ]]; then
    echo "reload-preflight: ${script} must define ${f}()" >&2
    exit 1
  fi
  fns+="${body}"$'\n'
done

# run <name> <fn> [args...]: runs fn in a fresh bash with the stubs below.
# STUB_PROBE: exit code of probe_live_cluster. STUB_LIST: exit code of the
# guest tenant-app listing (its output is STUB_APPS).
run() {
  local name="$1"
  shift
  : > "${log}"
  set +e
  bash -c '
set -euo pipefail
log="$1"; shift
FLYNN_VAGRANT_BUILDER=dev-builder
FLYNN_VAGRANT_NODE_PREFIX=dev-node
FLYNN_VAGRANT_FORCE_BUILD=0
SRC=/src
FLYNN_VAGRANT_TARGETS=()
sleep() { :; }
flynn_vagrant_collect_existing() { FLYNN_VAGRANT_TARGETS=(${STUB_TARGETS:-dev-builder dev-node1 dev-node2 dev-node3}); }
flynn_vagrant_cluster_domain() { echo test.local; }
flynn_vagrant_cluster_ip() { echo 192.0.2.1; }
flynn_vagrant_reload_one() { echo "reload $1" >> "${log}"; }
start_one_cluster_node() { echo "start $1" >> "${log}"; return "${STUB_START:-0}"; }
probe_live_cluster() {
  echo "probe sirenia=${PROBE_SIRENIA:-} full=${PROBE_FULL_STRENGTH:-}" >> "${log}"
  if [[ -n "${STUB_PROBE_SEQ:-}" ]]; then
    local n seq
    n="$(grep -c "^probe " "${log}")"
    read -r -a seq <<< "${STUB_PROBE_SEQ}"
    return "${seq[$((n - 1))]:-0}"
  fi
  return "${STUB_PROBE:-0}"
}
probe_live_cluster_retry() { echo "probe-retry $1" >> "${log}"; }
flynn_vagrant_running_cluster_nodes() { printf "%s\n" ${STUB_RUNNING-dev-node1 dev-node2 dev-node3}; }
run_as_root_on() {
  if [[ "$2" == *"/etc/flynn/host.json"* ]]; then
    echo "bootcheck $1" >> "${log}"
    [[ "${STUB_BOOT:-}" == "fail" ]] && return 255
    echo "${STUB_BOOT:-bootstrapped}"
    return 0
  fi
  if [[ "$2" == *LIST_ONLY=1* ]]; then
    echo "list" >> "${log}"
    printf "%s\n" ${STUB_APPS:-}
    return "${STUB_LIST:-0}"
  fi
  echo "ssh $1" >> "${log}"
}
run_as_root() { echo "builder $1" >> "${log}"; }
halt_builder() { echo "halt-builder" >> "${log}"; }
update_running_cluster() { echo "update-nodes" >> "${log}"; }
wait_tenant_http() { echo "tenant $1 [$2]" >> "${log}"; }
'"${fns}"'
"$@"
' _ "${log}" "$@" > "${tmp}/out" 2>&1
  local st=$?
  set -e
  echo "${st}" > "${tmp}/st"
  cp "${log}" "${tmp}/${name}.log"
}

fail() {
  echo "reload-preflight: $1" >&2
  echo "--- output (tail)" >&2
  tail -n 20 "${tmp}/out" >&2
  echo "--- calls (tail)" >&2
  tail -n 20 "${log}" >&2
  exit 1
}
st() { cat "${tmp}/st"; }

# Unhealthy cluster before the first node: no cluster node is rebooted and the
# reload fails.
STUB_PROBE=1 RELOAD_PREFLIGHT_TRIES=2 run unhealthy reload_cluster_rolling
[[ "$(st)" -ne 0 ]] || fail "reload on an unhealthy cluster must exit non-zero"
if grep -q '^reload dev-node' "${log}"; then
  fail "reload must not reboot a cluster node when the preflight probe fails"
fi
grep -q '^reload ' "${log}" \
  && fail "a failed preflight must not reboot the builder either (it only delays the check)"
grep -Fq 'probe sirenia=1 full=1' "${log}" \
  || fail "the preflight must check postgres peers and formation counts"
[[ "$(grep -c '^probe ' "${log}")" -eq 2 ]] \
  || fail "the preflight must retry RELOAD_PREFLIGHT_TRIES times"

# NEW-40: one passing probe is not stable. A sync that is about to find its
# WAL recycled and reseed answers healthy once, so the gate needs
# STABLE_HOLD_PASSES (default 3) passes in a row; a failure starts over.
STUB_PROBE_SEQ="0 1 0 0 1 0 0 0" RELOAD_PREFLIGHT_TRIES=20 run flapping wait_cluster_stable preflight 20 1
[[ "$(st)" -eq 0 ]] || fail "a cluster that holds healthy must pass after the flaps"
[[ "$(grep -c '^probe ' "${log}")" -eq 8 ]] \
  || fail "wait_cluster_stable must need 3 passes in a row (8 probes for 0 1 0 0 1 0 0 0), got $(grep -c '^probe ' "${log}")"
STUB_PROBE_SEQ="0 1 0 1 0 1 0 1 0 1" run neverholds wait_cluster_stable reboot 10 1
[[ "$(st)" -ne 0 ]] || fail "a cluster that never holds healthy for 3 probes must not count as stable"
STUB_PROBE_SEQ="0 1 0 0" STABLE_HOLD_PASSES=2 run hold2 wait_cluster_stable reboot 10 1
[[ "$(st)" -eq 0 && "$(grep -c '^probe ' "${log}")" -eq 4 ]] \
  || fail "STABLE_HOLD_PASSES must set the passes in a row"
# The gate before the first reboot holds too.
STUB_PROBE_SEQ="0 1 1" RELOAD_PREFLIGHT_TRIES=3 run preflightflap reload_cluster_rolling
[[ "$(st)" -ne 0 ]] || fail "a preflight that passed once and then failed must not reboot"
grep -q '^reload dev-node' "${log}" && fail "no node may be rebooted on a single passing preflight probe"

# The tenant app list cannot be read: no cluster node is rebooted.
STUB_LIST=1 run nolist reload_cluster_rolling
[[ "$(st)" -ne 0 ]] || fail "reload must exit non-zero when the tenant app list cannot be read"
if grep -q '^reload dev-node' "${log}"; then
  fail "reload must not reboot a cluster node without a tenant app list"
fi

# Healthy cluster: preflight runs before the first node, each node is
# rebooted and waited for, and the pre-crash app list is pinned.
STUB_APPS="app-one app-two" run healthy reload_cluster_rolling
[[ "$(st)" -eq 0 ]] || fail "reload on a healthy cluster must pass"
first_probe="$(grep -n '^probe ' "${log}" | head -1 | cut -d: -f1)"
first_node="$(grep -n '^reload ' "${log}" | head -1 | cut -d: -f1)"
[[ -n "${first_probe}" && -n "${first_node}" && "${first_probe}" -lt "${first_node}" ]] \
  || fail "the preflight probe must run before any VM (builder included) is rebooted"
grep -Fxq "reload dev-builder" "${log}" || fail "the builder must still be rebooted"
for n in 1 2 3; do
  grep -Fxq "reload dev-node${n}" "${log}" || fail "dev-node${n} must be rebooted"
  grep -Fxq "tenant dev-node${n} [app-one app-two]" "${log}" \
    || fail "tenant wait after dev-node${n} must use the pre-crash list"
done

# No tenant apps (empty list, listing succeeded) is not an error.
run empty reload_cluster_rolling
[[ "$(st)" -eq 0 ]] || fail "an empty tenant app list must not fail the reload"
grep -Fxq "tenant dev-node1 []" "${log}" || fail "empty list must still wait (re-list) after each node"

# Not bootstrapped yet: there is nothing to protect, so no gate; the
# tenant list is best effort (it cannot be read without host.json).
STUB_BOOT=none STUB_START=2 STUB_LIST=1 STUB_PROBE=1 run noboot reload_cluster_rolling
[[ "$(st)" -eq 0 ]] || fail "reload of a cluster that is not bootstrapped must not need the preflight"
grep -q '^probe ' "${log}" && fail "an unbootstrapped cluster must not be probed before the reload"
grep -Fxq "reload dev-node1" "${log}" || fail "an unbootstrapped dev-node1 must still be rebooted"

# Nothing running (halted cluster): reboot without the gate.
STUB_RUNNING="" STUB_LIST=1 run halted reload_cluster_rolling
[[ "$(st)" -eq 0 ]] || fail "reload of a halted cluster must not need the preflight"
[[ "$(grep -v '^reload dev-builder' "${log}" | sed -n 2p)" == "reload dev-node1" ]] \
  || fail "a halted cluster must be rebooted without a preflight probe"

# dev-node1 down but other nodes serving: refuse (the probe runs on dev-node1).
STUB_RUNNING="dev-node2 dev-node3" run node1down reload_cluster_rolling
[[ "$(st)" -ne 0 ]] || fail "reload must refuse when dev-node1 is down and other nodes serve"
grep -q '^reload dev-node' "${log}" && fail "no cluster node may be rebooted when dev-node1 is down"

# Bootstrap check cannot be answered: refuse.
STUB_BOOT=fail run bootunknown reload_cluster_rolling
[[ "$(st)" -ne 0 ]] || fail "reload must refuse when the bootstrap check fails"
grep -q '^reload dev-node' "${log}" && fail "no cluster node may be rebooted when the bootstrap check fails"

# Explicit override for a cluster that is already broken.
FLYNN_VAGRANT_RELOAD_SKIP_PREFLIGHT=1 STUB_LIST=1 run override reload_cluster_rolling
[[ "$(st)" -eq 0 ]] || fail "FLYNN_VAGRANT_RELOAD_SKIP_PREFLIGHT=1 must reboot without the gate"
[[ "$(grep -v '^reload dev-builder' "${log}" | sed -n 2p)" == "reload dev-node1" ]] \
  || fail "the override must skip the preflight probe"

# Builder-only reload (VM=dev-builder) needs no live cluster.
STUB_TARGETS=dev-builder STUB_PROBE=1 run builderonly reload_cluster_rolling
[[ "$(st)" -eq 0 ]] || fail "a builder-only reload must not need the cluster preflight"
[[ "$(cat "${log}")" == "reload dev-builder" ]] || fail "a builder-only reload must only reboot the builder"

# update waits for postgres peers and formation counts, not just status.
run update update_dev_cluster
[[ "$(st)" -eq 0 ]] || fail "update on a healthy cluster must pass"
grep -Fq 'probe sirenia=1 full=1' "${log}" \
  || fail "update must wait for postgres peers and every service at its formation count"
grep -Fq 'probe-retry update' "${log}" || fail "update must still run the full probe"
upd="$(grep -n '^update-nodes' "${log}" | cut -d: -f1)"
pr="$(grep -n '^probe ' "${log}" | head -1 | cut -d: -f1)"
[[ -n "${upd}" && -n "${pr}" && "${upd}" -lt "${pr}" ]] || fail "update must probe after updating the nodes"

STUB_PROBE=1 UPDATE_STABLE_TRIES=2 run update-bad update_dev_cluster
[[ "$(st)" -ne 0 ]] || fail "update must fail when the cluster never reaches full strength"

echo "reload-preflight ok"
