#!/bin/bash
# Per-second ingress prober for script/vagrant.sh chaos (runs on the laptop).
#
#   ingress-probe.sh <run-dir> <domain> <node>=<ip>...
#
# While <run-dir>/ingress.on exists, once a second (aligned to the second) it
# sends one request per target to every node IP, each with
# INGRESS_TIMEOUT seconds (default 1.5), and appends
#   epoch <TAB> node <TAB> target <TAB> HTTP status (000: none) <TAB> latency_ms
# to <run-dir>/ingress.tsv. INGRESS_TARGETS is a space list of:
#   router       http://chaos-ingress-probe.<domain>/ (the router itself; it
#                answers 404 for a host it has no route for)
#   controller   https://controller.<domain>/ping (200)
#   app:<name>   https://<name>.<domain>/ (a tenant app; 200-499)
# Requests of one second run in parallel and are not waited for by the next
# second, so a node that hangs does not hold back the others. When the flag
# file is removed it waits for the requests in flight and exits; SIGTERM
# stops them too. lib/chaos.sh chaos_verdict decides what counts as failed.
set -uo pipefail
dir="${1:?run dir}"
domain="${2:?domain}"
shift 2
nodes=("$@")
timeout="${INGRESS_TIMEOUT:-1.5}"
read -r -a targets <<< "${INGRESS_TARGETS:-router controller}"
out="${dir}/ingress.tsv"
batches=()

stop() {
  local pid
  for pid in "${batches[@]:-}"; do
    [[ -n "${pid}" ]] || continue
    pkill -TERM -P "${pid}" 2>/dev/null || true
    kill -TERM "${pid}" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  exit 0
}
trap stop TERM INT

# probe <epoch> <node> <ip> <target>: one request, one line.
probe() {
  local epoch="$1" node="$2" ip="$3" target="$4" host port url res code secs whole frac ms
  case "${target}" in
    router) host="chaos-ingress-probe.${domain}"; port=80; url="http://${host}/" ;;
    controller) host="controller.${domain}"; port=443; url="https://${host}/ping" ;;
    app:*) host="${target#app:}.${domain}"; port=443; url="https://${host}/" ;;
    *) return 0 ;;
  esac
  res="$(curl -sk -o /dev/null -w '%{http_code} %{time_total}' --max-time "${timeout}" \
    --resolve "${host}:${port}:${ip}" "${url}" 2>/dev/null)" || true
  code="${res%% *}"
  secs="${res#* }"
  [[ "${code}" =~ ^[0-9][0-9][0-9]$ ]] || code=000
  [[ "${secs}" =~ ^[0-9]+(\.[0-9]+)?$ ]] || secs="${timeout}"
  whole="${secs%%.*}"
  frac="000"
  [[ "${secs}" == *.* ]] && frac="${secs#*.}000"
  ms=$((10#${whole} * 1000 + 10#${frac:0:3}))
  # One short write per line with O_APPEND: lines from parallel probes do
  # not interleave.
  printf '%s\t%s\t%s\t%s\t%s\n' "${epoch}" "${node}" "${target}" "${code}" "${ms}" >> "${out}"
}

# next_second sleeps to the start of the next wall-clock second.
next_second() {
  if command -v perl >/dev/null 2>&1; then
    perl -MTime::HiRes=time,sleep -e 'my $t = time; sleep(1 - ($t - int($t)))'
  else
    sleep 1
  fi
}

while [[ -f "${dir}/ingress.on" ]]; do
  epoch="$(date +%s)"
  (
    trap 'kill -TERM $(jobs -p) 2>/dev/null; exit 0' TERM
    for pair in "${nodes[@]}"; do
      for target in "${targets[@]}"; do
        probe "${epoch}" "${pair%%=*}" "${pair#*=}" "${target}" &
      done
    done
    wait
  ) &
  batches+=("$!")
  # Keep only the batches that may still run (a batch ends within the
  # timeout); bash reaps the rest.
  if [[ "${#batches[@]}" -gt 5 ]]; then
    batches=("${batches[@]:1}")
  fi
  next_second
done
wait
