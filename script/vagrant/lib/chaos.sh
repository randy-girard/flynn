# shellcheck shell=bash
# monitor and chaos for script/vagrant.sh (laptop loop). Health samples run
# guest/check-health.sh on a running cluster node; faults run
# guest/chaos-fault.sh (job kills) or act on a node (flynn-host restart, hard
# power-off and boot). Needs run_as_root_on and start_one_cluster_node from
# vagrant.sh.

CHAOS_ALL_FAULTS="system-job plugin-job datastore-primary datastore-replica tenant-job host-restart node-reboot"
CHAOS_HEALTH_OUT=""

# chaos_check_node prints the first running cluster node other than $1.
chaos_check_node() {
  local avoid="${1:-}" name
  while IFS= read -r name; do
    if [[ -n "${name}" && "${name}" != "${avoid}" ]]; then
      echo "${name}"
      return 0
    fi
  done < <(flynn_vagrant_running_cluster_nodes)
  return 1
}

# chaos_health takes one health sample into CHAOS_HEALTH_OUT.
chaos_health() {
  local node ip st=0
  if ! node="$(chaos_check_node "${1:-}")"; then
    CHAOS_HEALTH_OUT="FAIL nodes: no running cluster node to check from"
    return 1
  fi
  ip="$(flynn_vagrant_node_ip "${node}")"
  set +e
  CHAOS_HEALTH_OUT="$(run_as_root_on "${node}" "cd ${SRC} && CLUSTER_DOMAIN=$(flynn_vagrant_cluster_domain) CLUSTER_IP=${ip} MIN_HOSTS=$(flynn_vagrant_get_nodes) BUILDER_IP=${FLYNN_DEV_IP:-192.168.57.10} TENANT_APPS=$(printf '%q' "${CHAOS_TENANT_APPS:-}") script/vagrant/guest/check-health.sh" 2>&1 | grep -v '^Connection to .* closed')"
  st=$?
  set -e
  if [[ "${st}" -ne 0 ]]; then
    return 1
  fi
}

chaos_now() { date -u +%H:%M:%S; }

# monitor_cluster prints one health sample every MONITOR_INTERVAL seconds
# (default 30) until Ctrl-C, or MONITOR_COUNT samples. Exit status is the
# last sample's.
monitor_cluster() {
  require_cluster_nodes
  local interval="${MONITOR_INTERVAL:-30}" count="${MONITOR_COUNT:-0}" i=0 st=0
  CHAOS_TENANT_APPS="$(list_tenant_apps 2>/dev/null | tr '\n' ' ' | sed 's/[[:space:]]*$//' || true)"
  echo "monitoring every ${interval}s (tenant apps: ${CHAOS_TENANT_APPS:-none}); Ctrl-C to stop"
  while :; do
    i=$((i + 1))
    st=0
    chaos_health || st=$?
    if [[ "${st}" -eq 0 ]]; then
      echo "$(chaos_now) healthy | $(printf '%s\n' "${CHAOS_HEALTH_OUT}" | grep '^ok ' | sed 's/^ok //' | paste -sd '|' -)"
    else
      echo "$(chaos_now) UNHEALTHY"
      printf '%s\n' "${CHAOS_HEALTH_OUT}" | grep -E '^(FAIL|ok) ' | sed 's/^/  /'
    fi
    if [[ "${count}" -gt 0 && "${i}" -ge "${count}" ]]; then
      return "${st}"
    fi
    sleep "${interval}"
  done
}

# chaos_wait_healthy samples every 10s until healthy or $2 seconds pass.
chaos_wait_healthy() {
  local label="$1" timeout="$2" avoid="${3:-}" start=${SECONDS}
  while :; do
    if chaos_health "${avoid}"; then
      return 0
    fi
    if (( SECONDS - start >= timeout )); then
      echo "$(chaos_now) ${label}: not healthy after ${timeout}s" >&2
      printf '%s\n' "${CHAOS_HEALTH_OUT}" | grep -E '^(FAIL|ok) ' | sed 's/^/  /' >&2
      return 1
    fi
    echo "$(chaos_now) ${label}: waiting ($((SECONDS - start))s) $(printf '%s\n' "${CHAOS_HEALTH_OUT}" | grep '^FAIL ' | head -1 | cut -c1-160)"
    sleep 10
  done
}

# chaos_inject applies one fault and sets CHAOS_TARGET. Exit 3 = nothing to hit.
chaos_inject() {
  local kind="$1" seed="$2" nodes=() name node out st=0 attempt
  while IFS= read -r name; do
    [[ -n "${name}" ]] && nodes+=("${name}")
  done < <(flynn_vagrant_running_cluster_nodes)
  node="${nodes[$((RANDOM % ${#nodes[@]}))]}"
  case "${kind}" in
    host-restart)
      CHAOS_TARGET="node=${node} systemctl restart flynn-host"
      run_as_root_on "${node}" "systemctl restart flynn-host.service"
      ;;
    node-reboot)
      CHAOS_TARGET="node=${node} power off ${CHAOS_NODE_DOWN:-60}s, boot, start flynn-host"
      echo "$(chaos_now) powering off ${node}"
      vagrant halt --force "${node}"
      sleep "${CHAOS_NODE_DOWN:-60}"
      flynn_vagrant_up_one "${node}"
      start_one_cluster_node "${node}" || true
      ;;
    *)
      # A vagrant ssh that fails before the guest script prints a target
      # (for example right after a node boots) is retried; the fault itself
      # has not run yet.
      for attempt in 1 2 3; do
        set +e
        out="$(run_as_root_on "${nodes[0]}" "cd ${SRC} && FAULT=${kind} SEED=${seed} script/vagrant/guest/chaos-fault.sh" 2>&1)"
        st=$?
        set -e
        CHAOS_TARGET="$(printf '%s\n' "${out}" | sed -n 's/^target //p' | tail -1)"
        if [[ "${st}" -eq 0 || "${st}" -eq 3 || -n "${CHAOS_TARGET}" ]]; then
          break
        fi
        sleep 10
      done
      if [[ "${st}" -ne 0 ]]; then
        if [[ -n "${CHAOS_RUN_DIR:-}" ]]; then
          printf '%s\n' "${out}" > "${CHAOS_RUN_DIR}/inject-${kind}.log"
        fi
        [[ -n "${CHAOS_TARGET}" ]] || CHAOS_TARGET="$(printf '%s\n' "${out}" | grep -v '^[[:space:]]*$' | tail -3 | paste -sd ';' -)"
        return "${st}"
      fi
      ;;
  esac
}

# chaos_monitor_loop runs in the background for the whole chaos run. Every
# CHAOS_MONITOR_INTERVAL seconds (default 15) it appends one health sample to
# $1/samples.tsv: epoch, UTC time, phase (from $1/phase), healthy|UNHEALTHY,
# failing sections, and the FAIL lines joined by " ;; ".
chaos_monitor_loop() {
  local dir="$1" interval="${CHAOS_MONITOR_INTERVAL:-15}" phase status sections fails
  while [[ -f "${dir}/phase" ]]; do
    phase="$(cat "${dir}/phase" 2>/dev/null || true)"
    if chaos_health; then status=healthy; else status=UNHEALTHY; fi
    sections="$(printf '%s\n' "${CHAOS_HEALTH_OUT}" | sed -n 's/^FAIL \([^:]*\):.*/\1/p' | paste -sd ',' - || true)"
    fails="$(printf '%s\n' "${CHAOS_HEALTH_OUT}" | sed -n 's/^FAIL //p' | tr '\t' ' ' | awk 'NR > 1 { printf " ;; " } { printf "%s", $0 }' || true)"
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$(date -u +%s)" "$(chaos_now)" "${phase}" "${status}" "${sections:--}" "${fails:--}" >> "${dir}/samples.tsv"
    sleep "${interval}"
  done
}

chaos_stop_monitor() {
  if [[ -n "${CHAOS_MONITOR_PID:-}" ]]; then
    rm -f "${CHAOS_RUN_DIR}/phase"
    kill "${CHAOS_MONITOR_PID}" 2>/dev/null || true
    wait "${CHAOS_MONITOR_PID}" 2>/dev/null || true
    CHAOS_MONITOR_PID=""
  fi
}

# chaos_write_report renders $1/report.txt from rounds.tsv and samples.tsv.
chaos_write_report() {
  local dir="$1" seed="$2" rounds="$3" order="$4" started="$5" verdict="$6"
  {
    echo "Flynn chaos report"
    echo "  started   ${started}"
    echo "  finished  $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "  result    ${verdict}"
    echo "  seed      ${seed} (CHAOS_SEED=${seed} repeats this run)"
    echo "  rounds    ${rounds}, order: ${order}"
    echo "  nodes     $(flynn_vagrant_get_nodes), tenant apps: ${CHAOS_TENANT_APPS:-none}"
    echo
    echo "Rounds"
    printf '  %-5s %-18s %-14s %-9s %-24s %s\n' ROUND FAULT RESULT RECOVERY "CHECKS FAILED" TARGET
    awk -F'\t' '
      FILENAME == ARGV[1] { if ($3 ~ /^round /) { split($3, p, " "); n = p[2]; ns = split($5, s, ","); for (k = 1; k <= ns; k++) if (s[k] != "-" && !seen[n, s[k]]++) secs[n] = secs[n] (secs[n] ? "," : "") s[k] }; next }
      { printf "  %-5s %-18s %-14s %-9s %-24s %s\n", $1, $2, $6, $7, (secs[$1] ? secs[$1] : "-"), $3 }
    ' "${dir}/samples.tsv" "${dir}/rounds.tsv"
    echo
    awk -F'\t' -v interval="${CHAOS_MONITOR_INTERVAL:-15}" '
      { total++; if ($4 == "healthy") ok++
        n = split($5, s, ","); delete now
        for (k = 1; k <= n; k++) if (s[k] != "-") { now[s[k]] = 1; if (!(s[k] in fails)) { order[++m] = s[k]; first[s[k]] = $2 }; fails[s[k]]++; if (!run[s[k]]) outages[s[k]]++; run[s[k]]++; if (run[s[k]] > longest[s[k]]) longest[s[k]] = run[s[k]] }
        for (c in run) if (!(c in now)) run[c] = 0
        if ($6 != "-") { c2 = split($6, f, " ;; "); for (k = 1; k <= c2; k++) { msg = substr(f[k], 1, 150); if (!(msg in msgs)) morder[++mm] = msg; msgs[msg]++ } } }
      END {
        printf "Monitor: %d samples every %ss, %d healthy (%d%%)\n", total, interval, ok, (total ? ok * 100 / total : 0)
        if (m == 0) { print "  every check passed in every sample"; exit }
        printf "  %-10s %-14s %-8s %-18s %s\n", "CHECK", "FAILED", "OUTAGES", "LONGEST", "FIRST FAILED"
        for (k = 1; k <= m; k++) { c = order[k]; printf "  %-10s %-14s %-8d ~%-17s %s\n", c, fails[c] "/" total, outages[c], longest[c] * interval "s", first[c] }
        print ""; print "Failure messages (samples seen)"
        for (k = 1; k <= mm; k++) printf "  %4d  %s\n", msgs[morder[k]], morder[k]
      }' "${dir}/samples.tsv"
    echo
    echo "Raw data: ${dir}/samples.tsv, ${dir}/rounds.tsv"
  } > "${dir}/report.txt"
}

# chaos_cluster runs CHAOS_ROUNDS faults (default 10) cycling through a
# shuffled CHAOS_FAULTS list. After each fault it waits CHAOS_SETTLE seconds,
# then up to CHAOS_TIMEOUT seconds for every health section to pass. Stops at
# the first round that does not recover. CHAOS_SEED repeats a run. A
# background monitor samples health for the whole run, and a report is
# written to CHAOS_REPORT_DIR (default ./flynn-logs/chaos/<UTC time>).
chaos_cluster() {
  require_cluster_nodes
  local rounds="${CHAOS_ROUNDS:-10}" timeout="${CHAOS_TIMEOUT:-900}" settle="${CHAOS_SETTLE:-20}"
  local seed="${CHAOS_SEED:-$(date +%s)}" kinds=() i j tmp kind start st inject_at
  local started verdict
  started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  CHAOS_RUN_DIR="${CHAOS_REPORT_DIR:-${ROOT}/flynn-logs/chaos/$(date -u +%Y%m%dT%H%M%SZ)}"
  mkdir -p "${CHAOS_RUN_DIR}"
  : > "${CHAOS_RUN_DIR}/samples.tsv"
  : > "${CHAOS_RUN_DIR}/rounds.tsv"
  read -r -a kinds <<< "${CHAOS_FAULTS:-${CHAOS_ALL_FAULTS}}"
  RANDOM="${seed}"
  for ((i = ${#kinds[@]} - 1; i > 0; i--)); do
    j=$((RANDOM % (i + 1)))
    tmp="${kinds[i]}"; kinds[i]="${kinds[j]}"; kinds[j]="${tmp}"
  done
  CHAOS_TENANT_APPS="$(list_tenant_apps 2>/dev/null | tr '\n' ' ' | sed 's/[[:space:]]*$//' || true)"
  echo "chaos: rounds=${rounds} seed=${seed} timeout=${timeout}s order=${kinds[*]}"
  echo "tenant apps: ${CHAOS_TENANT_APPS:-none}"
  echo "report: ${CHAOS_RUN_DIR}/report.txt"
  if ! chaos_wait_healthy "baseline" 300; then
    echo "cluster is not healthy before chaos; fix that first" >&2
    return 1
  fi
  echo "$(chaos_now) baseline healthy"
  echo "baseline" > "${CHAOS_RUN_DIR}/phase"
  chaos_monitor_loop "${CHAOS_RUN_DIR}" < /dev/null &
  CHAOS_MONITOR_PID=$!
  trap 'chaos_stop_monitor' EXIT
  trap 'chaos_stop_monitor; exit 130' INT TERM
  verdict=""
  for ((i = 1; i <= rounds; i++)); do
    kind="${kinds[$(((i - 1) % ${#kinds[@]}))]}"
    CHAOS_TARGET=""
    st=0
    echo "round ${i} ${kind}" > "${CHAOS_RUN_DIR}/phase"
    inject_at="$(date -u +%s)"
    chaos_inject "${kind}" "${seed}-${i}" || st=$?
    if [[ "${st}" -eq 3 ]]; then
      echo "$(chaos_now) round ${i}/${rounds} ${kind}: skipped (${CHAOS_TARGET})"
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${i}" "${kind}" "${CHAOS_TARGET}" "${inject_at}" "$(date -u +%s)" skipped - >> "${CHAOS_RUN_DIR}/rounds.tsv"
      continue
    elif [[ "${st}" -ne 0 ]]; then
      echo "$(chaos_now) round ${i}/${rounds} ${kind}: fault failed: ${CHAOS_TARGET}" >&2
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${i}" "${kind}" "${CHAOS_TARGET}" "${inject_at}" "$(date -u +%s)" inject-failed - >> "${CHAOS_RUN_DIR}/rounds.tsv"
      verdict="FAILED: round ${i} ${kind} could not inject the fault"
      break
    fi
    echo "$(chaos_now) round ${i}/${rounds} ${kind}: ${CHAOS_TARGET}"
    start=${SECONDS}
    sleep "${settle}"
    if chaos_wait_healthy "round ${i}" "${timeout}"; then
      echo "$(chaos_now) round ${i}/${rounds} ${kind}: recovered in $((SECONDS - start))s"
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${i}" "${kind}" "${CHAOS_TARGET}" "${inject_at}" "$(date -u +%s)" recovered "$((SECONDS - start))s" >> "${CHAOS_RUN_DIR}/rounds.tsv"
    else
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${i}" "${kind}" "${CHAOS_TARGET}" "${inject_at}" "$(date -u +%s)" FAILED ">${timeout}s" >> "${CHAOS_RUN_DIR}/rounds.tsv"
      verdict="FAILED: round ${i} ${kind} did not recover within ${timeout}s"
      break
    fi
    echo "between rounds" > "${CHAOS_RUN_DIR}/phase"
  done
  chaos_stop_monitor
  trap - EXIT INT TERM
  st=0
  if [[ -n "${verdict}" ]]; then
    st=1
  else
    verdict="PASSED: $(awk -F'\t' '$6 == "recovered"' "${CHAOS_RUN_DIR}/rounds.tsv" | wc -l | tr -d ' ') recovered, $(awk -F'\t' '$6 == "skipped"' "${CHAOS_RUN_DIR}/rounds.tsv" | wc -l | tr -d ' ') skipped"
  fi
  chaos_write_report "${CHAOS_RUN_DIR}" "${seed}" "${rounds}" "${kinds[*]}" "${started}" "${verdict}"
  echo
  cat "${CHAOS_RUN_DIR}/report.txt"
  return "${st}"
}
