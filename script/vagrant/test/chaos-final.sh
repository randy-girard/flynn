#!/bin/bash
# Final state of script/vagrant.sh chaos (lib/chaos.sh chaos_cluster) with
# stubbed faults, health samples and the full-strength gate. Does not boot a
# VM.
#
# A round counts as recovered once every health section passes, but a
# postgres peer can still be resyncing then. chaos must run the same
# full-strength gate as reload and update (wait_cluster_stable <label>
# <tries> 1: postgres peers healthy, every service at its formation count,
# hosts) after each fault round and once more at the end, and fail the run
# when the cluster is not back at full strength, so the next command never
# starts on a degraded cluster. CHAOS_SETTLE_TRIES (default 120) bounds it.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
lib="${ROOT}/script/vagrant/lib/chaos.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
fail=0

# run_cluster <name> [VAR=value ...]: runs chaos_cluster with three rounds.
# STUB_GATE_FAIL is the gate label that fails ("chaos" for the final gate,
# "round 1 system-job" for the first round); STUB_SKIP=1 skips every round.
run_cluster() {
  local name="$1"
  shift
  mkdir -p "${tmp}/${name}"
  set +e
  env "$@" bash -c '
set -euo pipefail
source "$1"
ROOT="$2"; CHAOS_REPORT_DIR="$3"
CHAOS_ROUNDS="${CHAOS_ROUNDS:-3}"; CHAOS_FAULTS="${CHAOS_FAULTS:-system-job node-reboot datastore-primary}"; CHAOS_SETTLE=0; CHAOS_SEED=1
# CHAOS_SEED=1 shuffles the three faults to: system-job datastore-primary node-reboot.
require_cluster_nodes() { :; }
list_tenant_apps() { :; }
flynn_vagrant_get_nodes() { echo 3; }
sleep() { :; }
chaos_wait_healthy() { CHAOS_HEALTH_OUT="ok cluster: fine"; }
chaos_monitor_loop() { :; }
chaos_inject() {
  echo "inject $1" >> "${CHAOS_REPORT_DIR}/calls"
  CHAOS_TARGET="kind=$1 none running"
  [[ "${STUB_SKIP:-0}" == 1 ]] && return 3
  CHAOS_TARGET="kind=$1 app=x"
}
# Same output shape as vagrant.sh wait_cluster_stable over probe_live_cluster:
# the probe failure goes to stderr, then the give-up line.
wait_cluster_stable() {
  echo "gate $1|$2|$3|phase=$(cat "${CHAOS_REPORT_DIR}/phase" 2>/dev/null || echo none)" >> "${CHAOS_REPORT_DIR}/calls"
  echo "probing live cluster on dev-node1 (192.0.2.1, min-hosts=3)"
  if [[ "$1" == "${STUB_GATE_FAIL:-}" ]]; then
    echo "controller HTTP 200 hosts=3 domain=test.local"
    echo "postgres peer 10.0.0.2 is not healthy (HTTP 503)" >&2
    echo "waiting for Flynn to settle after $1 (1/$2)"
    echo "cluster did not return to a stable state after $1" >&2
    return 1
  fi
  echo "cluster probe ok (quick)"
  echo "cluster is stable after $1"
}
chaos_cluster
' _ "${lib}" "${tmp}" "${tmp}/${name}" > "${tmp}/${name}.out" 2>&1
  echo $? > "${tmp}/${name}.st"
  set -e
}

check() {
  local name="$1" what="$2"
  shift 2
  if ! "$@"; then
    echo "chaos-final ${name}: ${what}" >&2
    echo "--- output" >&2
    cat "${tmp}/${name}.out" >&2
    echo "--- calls" >&2
    cat "${tmp}/${name}/calls" >&2 2>/dev/null || true
    fail=1
  fi
}
st() { cat "${tmp}/$1.st"; }
has() { grep -Fq -- "$2" "$1"; }
calls() { cat "${tmp}/$1/calls" 2>/dev/null || true; }

# Healthy end: exit 0, the gate runs after rounds 1 and 2 and at the end,
# always with the full-strength flag and the default budget, and the report
# shows the settle wait.
run_cluster ok
check ok "a run that ends at full strength must pass" [ "$(st ok)" -eq 0 ]
check ok "the verdict must stay PASSED" has "${tmp}/ok/report.txt" "result    PASSED: 3 recovered, 0 skipped"
check ok "the final gate must run with the full-strength flag and CHAOS_SETTLE_TRIES=120 by default" \
  grep -q '^gate chaos|120|1|' "${tmp}/ok/calls"
check ok "every round but the last must be followed by the full-strength gate" \
  grep -q '^gate round 1 system-job|120|1|' "${tmp}/ok/calls"
check ok "every round but the last must be followed by the full-strength gate" \
  grep -Eq '^gate round 2 [a-z-]+\|120\|1\|' "${tmp}/ok/calls"
check ok "the gate after a round must run before the next fault" \
  [ "$(calls ok | grep -n '^gate round 1 ' | cut -d: -f1)" -lt "$(calls ok | grep -n '^inject ' | sed -n 2p | cut -d: -f1)" ]
check ok "the final gate must run after the last fault" \
  [ "$(calls ok | tail -1 | cut -d'|' -f1)" == "gate chaos" ]
check ok "the gate must not be charged to a round's budget (phase must not be 'round N ...')" \
  bash -c '! grep -q "phase=round " "$1"' _ "${tmp}/ok/calls"
check ok "report.txt must show the final settle wait" \
  grep -Eq '^  settle    full strength after [0-9]+s' "${tmp}/ok/report.txt"
check ok "report.html must show the final settle wait" has "${tmp}/ok/report.html" "full strength after"
check ok "rounds.tsv must record the settle wait of each round" \
  awk -F'\t' 'NR == 1 && $8 ~ /^[0-9]+s$/ { ok = 1 } END { exit !ok }' "${tmp}/ok/rounds.tsv"

# The budget is configurable.
run_cluster tries CHAOS_SETTLE_TRIES=7
check tries "CHAOS_SETTLE_TRIES must set the gate's tries" grep -q '^gate chaos|7|1|' "${tmp}/tries/calls"

# A typo fails before the first fault.
run_cluster badtries CHAOS_SETTLE_TRIES=x
check badtries "a bad CHAOS_SETTLE_TRIES must fail" [ "$(st badtries)" -ne 0 ]
check badtries "a bad CHAOS_SETTLE_TRIES must fail before injecting" bash -c '! grep -q "^inject" "$1" 2>/dev/null' _ "${tmp}/badtries/calls"
check badtries "a bad CHAOS_SETTLE_TRIES must say so" has "${tmp}/badtries.out" "bad CHAOS_SETTLE_TRIES x"

# Not at full strength at the end: exit 1 with the reason, in the report and
# on the terminal, even though every round recovered.
want="FAILED: cluster did not return to full strength after chaos: postgres peer 10.0.0.2 is not healthy (HTTP 503)"
run_cluster final STUB_GATE_FAIL=chaos
check final "a run that ends below full strength must exit 1" [ "$(st final)" -eq 1 ]
check final "report.txt must give the verdict with the probe's reason" has "${tmp}/final/report.txt" "result    ${want}"
check final "the terminal output must say why" has "${tmp}/final.out" "${want}"
check final "report.txt must show the failed settle wait" \
  grep -Eq '^  settle    not at full strength after [0-9]+s \(CHAOS_SETTLE_TRIES=120\): postgres peer 10.0.0.2' "${tmp}/final/report.txt"
check final "report.html must show the failed verdict" has "${tmp}/final/report.html" "did not return to full strength after chaos"

# A round that leaves the cluster below full strength (node-reboot or
# datastore-primary leaving a peer resyncing) stops the run before the next
# fault.
run_cluster round STUB_GATE_FAIL="round 1 system-job"
check round "a round that does not return to full strength must exit 1" [ "$(st round)" -eq 1 ]
check round "the next fault must not be injected on a degraded cluster" [ "$(calls round | grep -c '^inject ')" -eq 1 ]
check round "the verdict must name the round and the reason" has "${tmp}/round/report.txt" \
  "result    FAILED: cluster did not return to full strength after round 1 system-job: postgres peer 10.0.0.2 is not healthy (HTTP 503)"

# A last round that was skipped still gets the final gate, and its failure
# is reported next to the verdict's own reasons.
run_cluster skipped STUB_SKIP=1 STUB_GATE_FAIL=chaos
check skipped "an all-skipped run must still fail" [ "$(st skipped)" -eq 1 ]
check skipped "the final gate must run after skipped rounds" grep -q '^gate chaos|' "${tmp}/skipped/calls"
check skipped "both failures must be reported" has "${tmp}/skipped/report.txt" \
  "result    FAILED: cluster did not return to full strength after chaos: postgres peer 10.0.0.2 is not healthy (HTTP 503); no fault was injected (3 skipped)"

# A single round: only the final gate.
run_cluster one CHAOS_ROUNDS=1
check one "a one-round run must pass" [ "$(st one)" -eq 0 ]
check one "a one-round run must gate once, at the end" [ "$(calls one | grep -c '^gate ')" -eq 1 ]

if [[ "${fail}" -ne 0 ]]; then
  exit 1
fi
echo "chaos-final: ok"
