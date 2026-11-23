#!/bin/bash
# Verdict of script/vagrant.sh chaos (lib/chaos.sh) from canned rounds.tsv and
# samples.tsv. Does not boot a VM.
#
# A run where no fault was injected (every round skipped) must fail. A round
# that comes back after CHAOS_TIMEOUT, or whose background samples break a
# per-fault budget (CHAOS_BUDGET, default: no failed tenant ingress sample
# during tenant-job or datastore-replica), must fail even though the round
# itself recovered. CHAOS_MAX_SKIPPED caps the skipped rounds.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
lib="${ROOT}/script/vagrant/lib/chaos.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
fail=0

# round <n> <kind> <result> <recovery>: one rounds.tsv line.
round() { printf '%s\t%s\ttarget\t100\t200\t%s\t%s\n' "$1" "$2" "$3" "$4"; }
# sample <phase> <healthy|UNHEALTHY> <sections>: one samples.tsv line.
sample() {
  local fails="-"
  [[ "$3" != "-" ]] && fails="$3: down"
  printf '100\t00:00:00\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "${fails}"
}

# verdict <name> <want-status> <want-substring> [VAR=value ...]: runs
# chaos_verdict on ${tmp}/<name>/{rounds,samples}.tsv with the given env.
verdict() {
  local name="$1" want="$2" substr="$3" out st
  shift 3
  set +e
  out="$(env "$@" bash -c 'set -euo pipefail; source "$1"; chaos_verdict "$2" "${CHAOS_TIMEOUT:-900}"' _ "${lib}" "${tmp}/${name}" 2>&1)"
  st=$?
  set -e
  if [[ "${st}" -ne "${want}" ]] || ! grep -Fq -- "${substr}" <<<"${out}"; then
    echo "chaos-verdict ${name}: want status ${want} and \"${substr}\", got status ${st}: ${out}" >&2
    fail=1
  fi
}

# case <name>: reads rounds then samples from stdin, separated by a line "--".
case_dir() {
  local d="${tmp}/$1" line target=rounds
  mkdir -p "${d}"
  : > "${d}/rounds.tsv"
  : > "${d}/samples.tsv"
  while IFS= read -r line; do
    if [[ "${line}" == "--" ]]; then target=samples; continue; fi
    printf '%s\n' "${line}" >> "${d}/${target}.tsv"
  done
}

if ! grep -q '^chaos_verdict() {' "${lib}"; then
  echo "chaos-verdict: ${lib} must define chaos_verdict()" >&2
  exit 1
fi

{ round 1 tenant-job skipped -; round 2 plugin-job skipped -; echo --; sample baseline healthy -; } | case_dir all-skipped
verdict all-skipped 1 "FAILED: no fault was injected (2 skipped)"

{ echo --; sample baseline healthy -; } | case_dir no-rounds
verdict no-rounds 1 "FAILED: no fault was injected (0 skipped)"

{
  round 1 system-job recovered 40s; round 2 tenant-job skipped -; round 3 datastore-replica recovered 35s
  echo --
  sample baseline healthy -; sample "round 1 system-job" UNHEALTHY cluster; sample "round 3 datastore-replica" UNHEALTHY cluster,resources
} | case_dir mixed
verdict mixed 0 "PASSED: 2 recovered, 1 skipped"
verdict mixed 1 "FAILED: 1 skipped rounds, more than CHAOS_MAX_SKIPPED=0" CHAOS_MAX_SKIPPED=0
verdict mixed 0 "PASSED: 2 recovered, 1 skipped" CHAOS_MAX_SKIPPED=1
verdict mixed 1 "FAILED: round 1 system-job had 1 UNHEALTHY samples, budget 0" "CHAOS_BUDGET=system-job:unhealthy=0"

{
  round 1 tenant-job recovered 30s; round 2 datastore-replica recovered 30s
  echo --
  sample "round 1 tenant-job" UNHEALTHY tenants; sample "round 1 tenant-job" healthy -
  sample "round 2 datastore-replica" UNHEALTHY cluster,tenants; sample "round 2 datastore-replica" UNHEALTHY tenants
} | case_dir ingress
verdict ingress 1 "FAILED: round 1 tenant-job had 1 failed tenants samples, budget 0"
verdict ingress 1 "round 2 datastore-replica had 2 failed tenants samples, budget 0"
verdict ingress 0 "PASSED: 2 recovered, 0 skipped" CHAOS_BUDGET=
verdict ingress 1 "round 2 datastore-replica had 2 failed tenants samples, budget 1" "CHAOS_BUDGET=tenant-job:tenants=1 datastore-replica:tenants=1"

# Failed ingress outside a tenant-job round (baseline, between rounds, other
# faults) is not charged to the budget, and "round 1" is not "round 10".
{
  round 1 tenant-job recovered 30s; round 10 node-reboot recovered 90s
  echo --
  sample baseline UNHEALTHY tenants; sample "between rounds" UNHEALTHY tenants; sample "round 10 node-reboot" UNHEALTHY tenants
} | case_dir other-phases
verdict other-phases 0 "PASSED: 2 recovered, 0 skipped"
verdict other-phases 1 "FAILED: round 10 node-reboot recovered in 90s, budget 60s" "CHAOS_BUDGET=node-reboot:recovery=60"

# A recovery that took longer than CHAOS_TIMEOUT (the last sample can run
# past it) is not a pass.
{ round 1 system-job recovered 1028s; round 2 plugin-job recovered 30s; echo --; } | case_dir slow
verdict slow 1 "FAILED: round 1 system-job recovered in 1028s, more than CHAOS_TIMEOUT=900s"
verdict slow 0 "PASSED: 2 recovered, 0 skipped" CHAOS_TIMEOUT=1100

{ round 1 system-job recovered 30s; echo --; } | case_dir bad-budget
verdict bad-budget 2 "bad CHAOS_BUDGET entry" "CHAOS_BUDGET=system-job:tenants"
verdict bad-budget 2 "bad CHAOS_BUDGET entry" "CHAOS_BUDGET=system-job:tenants=x"

# chaos_cluster must use the verdict: an all-skipped run exits non-zero and
# says so in report.txt.
run_cluster() {
  local name="$1" inject="$2"
  mkdir -p "${tmp}/${name}"
  set +e
  bash -c '
set -euo pipefail
source "$1"
ROOT="$2"; CHAOS_REPORT_DIR="$3"; CHAOS_ROUNDS=2; CHAOS_FAULTS="tenant-job system-job"; CHAOS_SETTLE=0
INJECT="$4"
require_cluster_nodes() { :; }
list_tenant_apps() { :; }
flynn_vagrant_get_nodes() { echo 3; }
sleep() { :; }
chaos_wait_healthy() { CHAOS_HEALTH_OUT="ok cluster: fine"; }
chaos_monitor_loop() { :; }
chaos_inject() { echo "inject $1" >> "${CHAOS_REPORT_DIR}/injected"; CHAOS_TARGET="kind=$1 none running"; [[ "${INJECT}" == skip ]] && return 3; CHAOS_TARGET="kind=$1 app=x"; return 0; }
chaos_cluster
' _ "${lib}" "${tmp}" "${tmp}/${name}" "${inject}" > "${tmp}/${name}.out" 2>&1
  echo $?
  set -e
}
st="$(run_cluster run-skip skip)"
if [[ "${st}" -eq 0 ]] || ! grep -Fq "result    FAILED: no fault was injected (2 skipped)" "${tmp}/run-skip/report.txt"; then
  echo "chaos-verdict: chaos_cluster with every round skipped must exit non-zero (got ${st})" >&2
  cat "${tmp}/run-skip.out" >&2
  fail=1
fi
st="$(run_cluster run-ok ok)"
if [[ "${st}" -ne 0 ]] || ! grep -Fq "result    PASSED: 2 recovered, 0 skipped" "${tmp}/run-ok/report.txt"; then
  echo "chaos-verdict: chaos_cluster with every round recovered must pass (got ${st})" >&2
  cat "${tmp}/run-ok.out" >&2
  fail=1
fi

# A budget typo must fail before the first fault, not after the whole run.
st="$(CHAOS_BUDGET="tenant-job:tenants" run_cluster run-typo ok)"
if [[ "${st}" -eq 0 || -e "${tmp}/run-typo/injected" ]] || ! grep -Fq "bad CHAOS_BUDGET entry tenant-job:tenants" "${tmp}/run-typo.out"; then
  echo "chaos-verdict: chaos_cluster with a bad CHAOS_BUDGET must fail before injecting (got ${st})" >&2
  cat "${tmp}/run-typo.out" >&2
  fail=1
fi

if [[ "${fail}" -ne 0 ]]; then
  exit 1
fi
echo "chaos-verdict: ok"
