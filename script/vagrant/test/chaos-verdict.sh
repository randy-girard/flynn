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
verdict bad-budget 2 "bad CHAOS_BUDGET entry" "CHAOS_BUDGET=system-job:ingress-longest=x"
verdict bad-budget 0 "PASSED: 1 recovered, 0 skipped" "CHAOS_BUDGET=system-job:ingress-longest=3 system-job:ingress=0"

# Per-second ingress probes (ingress.tsv: epoch, node, target, HTTP status,
# latency ms). A second fails only when no probed node answered the target
# as expected (router: 404 for an unknown host, controller: 200 on /ping,
# app:<name>: 200-499), so a node that is down while the others serve is
# reported per node but does not fail the run. A probe belongs to a round
# when its epoch is in [inject, recovered] from rounds.tsv.
# wround <n> <kind> <start> <end> <target>: a recovered round with a window.
wround() { printf '%s\t%s\t%s\t%s\t%s\trecovered\t30s\t5s\n' "$1" "$2" "$5" "$3" "$4"; }
# probes <from> <to> <target> <code1> <code2> <code3>: one probe per second
# per node (dev-node1..3) answering the given codes.
probes() {
  local e
  for ((e = $1; e <= $2; e++)); do
    printf '%s\tdev-node1\t%s\t%s\t12\n%s\tdev-node2\t%s\t%s\t12\n%s\tdev-node3\t%s\t%s\t1500\n' \
      "${e}" "$3" "$4" "${e}" "$3" "$5" "${e}" "$3" "$6"
  done
}
# ingress_case <name> <rounds...>: rounds from args, ingress.tsv from stdin.
ingress_case() {
  local d="${tmp}/$1"
  shift
  mkdir -p "${d}"
  printf '%s' "$*" | tr '|' '\n' > "${d}/rounds.tsv"
  : > "${d}/samples.tsv"
  cat > "${d}/ingress.tsv"
}
web2="kind=tenant-job app=foo type=web host=h job=j running=2"
web1="kind=tenant-job app=foo type=web host=h job=j running=1"

# One second where no node served the tenant app during tenant-job: fails the
# default budget.
{ probes 100 104 app:foo 200 200 200; probes 105 105 app:foo 503 503 000; probes 106 150 app:foo 200 200 200; probes 100 150 router 404 404 404; } |
  ingress_case ingress-lost "$(wround 1 tenant-job 100 150 "${web2}")"
verdict ingress-lost 1 "FAILED: round 1 tenant-job had 1 failed ingress probe seconds (app:foo=1), budget 0"
verdict ingress-lost 0 "PASSED: 1 recovered, 0 skipped" "CHAOS_BUDGET=tenant-job:ingress=1"
verdict ingress-lost 0 "PASSED: 1 recovered, 0 skipped" CHAOS_BUDGET=

# The same second lost on one node only: another node served it.
{ probes 100 104 app:foo 200 200 200; probes 105 105 app:foo 200 200 000; probes 106 150 app:foo 200 200 200; } |
  ingress_case ingress-one-node "$(wround 1 tenant-job 100 150 "${web2}")"
verdict ingress-one-node 0 "PASSED: 1 recovered, 0 skipped"

# A node down for a whole node-reboot round: per-node failures only.
{ probes 100 220 controller 200 200 000; probes 100 220 router 404 404 000; } |
  ingress_case ingress-reboot "$(wround 1 node-reboot 100 220 "node=dev-node3 power off 60s")"
verdict ingress-reboot 0 "PASSED: 1 recovered, 0 skipped"
verdict ingress-reboot 0 "PASSED: 1 recovered, 0 skipped" "CHAOS_BUDGET=node-reboot:ingress=0 node-reboot:ingress-longest=0"

# A powered-off node does not reset its connections: a router holding a
# pooled connection to a backend there waits up to its 5 s dead-peer bound
# before it retries another backend, so every node can miss a probe for a
# few seconds. The default node-reboot budget allows that (measured: 0 and 1
# failed seconds), not more.
{ probes 100 119 controller 200 200 000; probes 120 124 controller 000 000 000; probes 125 220 controller 200 200 000; } |
  ingress_case ingress-reboot-blip "$(wround 1 node-reboot 100 220 "node=dev-node3 power off 60s")"
verdict ingress-reboot-blip 0 "PASSED: 1 recovered, 0 skipped"
{ probes 100 119 controller 200 200 000; probes 120 125 controller 000 000 000; probes 126 220 controller 200 200 000; } |
  ingress_case ingress-reboot-long "$(wround 1 node-reboot 100 220 "node=dev-node3 power off 60s")"
verdict ingress-reboot-long 1 "FAILED: round 1 node-reboot had a 6s ingress outage (controller), budget 5s"
{ probes 100 104 controller 000 000 000; probes 105 109 controller 200 200 200; probes 110 113 controller 000 000 000
  probes 114 120 controller 200 200 200; probes 121 123 controller 000 000 000; probes 124 220 controller 200 200 000; } |
  ingress_case ingress-reboot-many "$(wround 1 node-reboot 100 220 "node=dev-node3 power off 60s")"
verdict ingress-reboot-many 1 "FAILED: round 1 node-reboot had 12 failed ingress probe seconds (controller=12), budget 10"
# Any other fault: not one failed second by default.
{ probes 100 104 controller 200 200 200; probes 105 105 controller 000 000 000; probes 106 150 controller 200 200 200; } |
  ingress_case ingress-host-restart "$(wround 1 host-restart 100 150 "node=dev-node2 systemctl restart flynn-host")"
verdict ingress-host-restart 1 "FAILED: round 1 host-restart had 1 failed ingress probe seconds (controller=1), budget 0"

# Outages before the first fault, between rounds and in the settle wait are
# not charged to a round.
{ probes 50 60 controller 503 503 503; probes 100 150 controller 200 200 200; probes 151 170 controller 000 000 000; } |
  ingress_case ingress-outside "$(wround 1 system-job 100 150 "kind=system-job app=router")"
verdict ingress-outside 0 "PASSED: 1 recovered, 0 skipped" "CHAOS_BUDGET=system-job:ingress=0"

# Every target counts with its own expectation: the router must answer 404
# for an unknown host, the controller 200; a controller 401 is a failure.
{ probes 100 119 controller 200 200 200; probes 120 122 controller 503 000 401; probes 123 129 controller 200 200 200
  probes 130 130 controller 000 000 000; probes 131 150 controller 200 200 200; probes 100 150 router 404 404 404; } |
  ingress_case ingress-longest "$(wround 1 system-job 100 150 "kind=system-job app=controller")"
verdict ingress-longest 1 "FAILED: round 1 system-job had a 3s ingress outage (controller), budget 2s" "CHAOS_BUDGET=system-job:ingress-longest=2"
verdict ingress-longest 0 "PASSED: 1 recovered, 0 skipped" "CHAOS_BUDGET=system-job:ingress-longest=3"
verdict ingress-longest 1 "round 1 system-job had 4 failed ingress probe seconds (controller=4), budget 3" "CHAOS_BUDGET=system-job:ingress=3"
verdict ingress-longest 0 "PASSED: 1 recovered, 0 skipped" "CHAOS_BUDGET=system-job:ingress=4"
{ probes 100 150 router 200 200 200; } | ingress_case ingress-router "$(wround 1 system-job 100 150 "kind=system-job app=router")"
verdict ingress-router 1 "had 51 failed ingress probe seconds (router=51)" "CHAOS_BUDGET=system-job:ingress=0"

# A tenant app with a single web job cannot serve while that job is stopped:
# the failure must say so instead of only breaking the budget.
hint="tenant app foo runs a single web job; scale web=2 or set CHAOS_BUDGET"
{ probes 100 110 app:foo 503 503 503; probes 111 150 app:foo 200 200 200; } |
  ingress_case ingress-web1 "$(wround 1 tenant-job 100 150 "${web1}")"
verdict ingress-web1 1 "round 1 tenant-job had 11 failed ingress probe seconds (app:foo=11), budget 0: ${hint}"
{ printf '1\ttenant-job\t%s\t100\t150\trecovered\t30s\t5s\n' "${web1}"; echo --; sample "round 1 tenant-job" UNHEALTHY tenants; } | case_dir tenants-web1
verdict tenants-web1 1 "round 1 tenant-job had 1 failed tenants samples, budget 0: ${hint}"
{ printf '1\ttenant-job\t%s\t100\t150\trecovered\t30s\t5s\n' "${web2}"; echo --; sample "round 1 tenant-job" UNHEALTHY tenants; } | case_dir tenants-web2
verdict tenants-web2 1 "round 1 tenant-job had 1 failed tenants samples, budget 0"
out="$(bash -c 'source "$1"; chaos_verdict "$2" 900' _ "${lib}" "${tmp}/tenants-web2" 2>&1 || true)"
if grep -Fq "single web job" <<<"${out}"; then
  echo "chaos-verdict: a tenant app with two web jobs must not get the single-web-job hint: ${out}" >&2
  fail=1
fi

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
wait_cluster_stable() { echo "cluster is stable after $1"; }
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
