#!/bin/bash
# Per-second ingress prober of script/vagrant.sh chaos (host/ingress-probe.sh
# started and stopped by lib/chaos.sh) with a stubbed curl. Does not boot a
# VM; takes a few seconds of wall time.
#
# The health samples run every 15 s plus their own run time, so a short
# ingress outage between two samples was invisible. During the whole run
# chaos probes every target (the router itself, the controller, each tenant
# app that answered at baseline) on every node IP once a second with a short
# timeout and appends "epoch node target status latency_ms" to ingress.tsv.
# The prober must stop with the run, including when chaos is interrupted,
# and leave no process behind. The report shows an Ingress section.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
lib="${ROOT}/script/vagrant/lib/chaos.sh"
tmp="$(mktemp -d)"
cleanup() {
  pkill -f "ingress-probe.sh ${tmp}" 2>/dev/null || true
  rm -rf "${tmp}"
}
trap cleanup EXIT
fail=0

mkdir -p "${tmp}/bin"
# Stub curl: answers by the IP in --resolve host:port:ip. STUB_HANG_IP sleeps
# for --max-time and then fails like a timeout (000, exit 28). The router
# answers 404 for its unknown probe host, everything else 200.
cat > "${tmp}/bin/curl" <<'SH'
#!/bin/bash
max=10 resolve="" url="${!#}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --max-time) max="$2"; shift ;;
    --resolve) resolve="$2"; shift ;;
  esac
  shift
done
ip="${resolve##*:}"
if [[ "${ip}" == "${STUB_HANG_IP:-none}" ]]; then
  sleep "${max}"
  printf '000 %s' "${max}"
  exit 28
fi
case "${url}" in
  http://chaos-ingress-probe.*) printf '404 0.001200' ;;
  *) printf '200 0.012345' ;;
esac
SH
chmod +x "${tmp}/bin/curl"

check() {
  local what="$1"
  shift
  if ! "$@"; then
    echo "chaos-ingress: ${what}" >&2
    fail=1
  fi
}
# running <pattern>: is a process whose command line contains it alive?
# pgrep where it exists, /proc otherwise (minimal containers).
running() {
  local f
  if command -v pgrep >/dev/null 2>&1; then
    pgrep -f "$1" >/dev/null
    return
  fi
  for f in /proc/[0-9]*/cmdline; do
    tr '\0' ' ' < "${f}" 2>/dev/null | grep -Fq -- "$1" && return 0
  done
  return 1
}
no_prober() { ! running "ingress-probe.sh $1"; }
no_curl() { ! running "${tmp}/bin/curl"; }

stubs='set -euo pipefail
source "$1"
flynn_vagrant_cluster_nodes() { printf "dev-node1\ndev-node2\n"; }
flynn_vagrant_node_ip() { case "$1" in dev-node1) echo 10.9.0.1 ;; dev-node2) echo 10.9.0.2 ;; esac; }
flynn_vagrant_cluster_domain() { echo test.local; }
'

# Start, let it run about three seconds, stop.
d="${tmp}/probe"
mkdir -p "${d}"
PATH="${tmp}/bin:${PATH}" STUB_HANG_IP=10.9.0.2 CHAOS_RUN_DIR="${d}" CHAOS_INGRESS_TIMEOUT=1.5 bash -c "${stubs}"'
CHAOS_INGRESS_APPS="foo"
chaos_start_ingress
echo "${CHAOS_INGRESS_PID}" > "${CHAOS_RUN_DIR}/pid"
/bin/sleep 3.2
chaos_stop_ingress
' _ "${lib}" > "${tmp}/probe.out" 2>&1 || { echo "chaos-ingress: start/stop failed" >&2; cat "${tmp}/probe.out" >&2; exit 1; }
check "the prober must be gone after chaos_stop_ingress" no_prober "${d}"
check "no curl may outlive the prober" no_curl
check "ingress.tsv must exist" test -s "${d}/ingress.tsv"
check "every line must be epoch, node, target, status, latency_ms" \
  awk -F'\t' 'NF != 5 || $1 !~ /^[0-9]+$/ || $4 !~ /^[0-9][0-9][0-9]$/ || $5 !~ /^[0-9]+$/ { bad = 1 } END { exit bad }' "${d}/ingress.tsv"
check "it must probe at least three different seconds in ~3 s" \
  [ "$(cut -f1 "${d}/ingress.tsv" | sort -u | wc -l)" -ge 3 ]
for t in router controller app:foo; do
  for n in dev-node1 dev-node2; do
    check "it must probe ${t} on ${n}" grep -q "	${n}	${t}	" "${d}/ingress.tsv"
  done
done
check "the router target must get the router's 404 for an unknown host" grep -q '	dev-node1	router	404	1$' "${d}/ingress.tsv"
check "a hanging node must be recorded as 000 at about the timeout" grep -Eq '	dev-node2	controller	000	1500$' "${d}/ingress.tsv"
check "a hanging node must not hold back the other node's probes" \
  [ "$(awk -F'\t' '$2 == "dev-node1" && $3 == "controller"' "${d}/ingress.tsv" | wc -l)" -ge 3 ]

# CHAOS_INGRESS_NODES limits the probe to some nodes (the single-node view).
d="${tmp}/one"
mkdir -p "${d}"
PATH="${tmp}/bin:${PATH}" CHAOS_RUN_DIR="${d}" CHAOS_INGRESS_NODES=dev-node2 bash -c "${stubs}"'
chaos_start_ingress; /bin/sleep 1.5; chaos_stop_ingress' _ "${lib}" > "${tmp}/one.out" 2>&1 || true
check "CHAOS_INGRESS_NODES=dev-node2 must probe only dev-node2" \
  [ "$(cut -f2 "${d}/ingress.tsv" | sort -u | tr '\n' ' ')" == "dev-node2 " ]
check "the prober must be gone (CHAOS_INGRESS_NODES)" no_prober "${d}"

# CHAOS_INGRESS=0 turns it off.
d="${tmp}/off"
mkdir -p "${d}"
PATH="${tmp}/bin:${PATH}" CHAOS_RUN_DIR="${d}" CHAOS_INGRESS=0 bash -c "${stubs}"'
chaos_start_ingress; /bin/sleep 1.2; chaos_stop_ingress' _ "${lib}" > "${tmp}/off.out" 2>&1 || true
check "CHAOS_INGRESS=0 must not probe" test ! -s "${d}/ingress.tsv"

# run_cluster <name> <mode>: chaos_cluster with stubbed faults and health
# and the real prober. mode=term sends SIGTERM to chaos in the first round.
run_cluster() {
  local name="$1" mode="$2"
  mkdir -p "${tmp}/${name}"
  set +e
  PATH="${tmp}/bin:${PATH}" MODE="${mode}" bash -c "${stubs}"'
ROOT="$2"; CHAOS_REPORT_DIR="$3"; CHAOS_ROUNDS=2; CHAOS_FAULTS="system-job tenant-job"; CHAOS_SETTLE=0; CHAOS_SEED=1
require_cluster_nodes() { :; }
list_tenant_apps() { echo foo; }
flynn_vagrant_get_nodes() { echo 2; }
sleep() { :; }
chaos_wait_healthy() { CHAOS_HEALTH_OUT="ok tenants: tenant HTTP ok: foo=200"; /bin/sleep 1.3; }
chaos_monitor_loop() { :; }
wait_cluster_stable() { echo "cluster is stable after $1"; }
chaos_inject() {
  CHAOS_TARGET="kind=$1 app=foo type=web host=h job=j running=2"
  if [[ "${MODE}" == term ]]; then kill -TERM $$; fi
}
chaos_cluster
' _ "${lib}" "${tmp}" "${tmp}/${name}" > "${tmp}/${name}.out" 2>&1
  echo $? > "${tmp}/${name}.st"
  set -e
}
run_cluster run ok
check "chaos_cluster must pass with healthy ingress (got $(cat "${tmp}/run.st"))" [ "$(cat "${tmp}/run.st")" -eq 0 ]
check "the prober must be gone after chaos_cluster" no_prober "${tmp}/run"
check "no curl may outlive chaos_cluster" no_curl
check "chaos_cluster must probe the tenant app that answered at baseline" grep -q '	app:foo	200	' "${tmp}/run/ingress.tsv"
r="${tmp}/run/report.txt"
check "report.txt must have an Ingress section" grep -Eq '^Ingress: [0-9]+ probes' "${r}"
check "report.txt must give the cluster-level failures" grep -Fq "cluster level (no node answered): 0 failed probe seconds, longest outage 0s" "${r}"
check "report.txt must list per-round ingress counts" grep -Eq '^  [12] +system-job +[0-9]+ +0 +0s' "${r}"
check "report.txt must give per-node failures" grep -Eq 'per node \(failed probes, not part of the verdict\): dev-node1 0/[0-9]+, dev-node2 0/[0-9]+' "${r}"
check "report.txt must name ingress.tsv" grep -Fq "${tmp}/run/ingress.tsv" "${r}"
check "report.html must have an Ingress section" grep -Fq "<h2>Ingress</h2>" "${tmp}/run/report.html"
check "report.html must link ingress.tsv" grep -Fq '<a href="ingress.tsv">ingress.tsv</a>' "${tmp}/run/report.html"
if [[ "${fail}" -ne 0 ]]; then cat "${tmp}/run.out" >&2; fi

run_cluster term term
check "an interrupted chaos must exit 130 (got $(cat "${tmp}/term.st"))" [ "$(cat "${tmp}/term.st")" -eq 130 ]
check "the prober must be gone after an interrupted chaos" no_prober "${tmp}/term"
check "no curl may outlive an interrupted chaos" no_curl

if [[ "${fail}" -ne 0 ]]; then
  exit 1
fi
echo "chaos-ingress: ok"
