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
  local kind="$1" seed="$2" nodes=() name node out st=0
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
      set +e
      out="$(run_as_root_on "${nodes[0]}" "cd ${SRC} && FAULT=${kind} SEED=${seed} script/vagrant/guest/chaos-fault.sh" 2>&1)"
      st=$?
      set -e
      CHAOS_TARGET="$(printf '%s\n' "${out}" | sed -n 's/^target //p' | tail -1)"
      if [[ "${st}" -ne 0 ]]; then
        [[ -n "${CHAOS_TARGET}" ]] || CHAOS_TARGET="$(printf '%s\n' "${out}" | tail -1)"
        return "${st}"
      fi
      ;;
  esac
}

# chaos_cluster runs CHAOS_ROUNDS faults (default 10) cycling through a
# shuffled CHAOS_FAULTS list. After each fault it waits CHAOS_SETTLE seconds,
# then up to CHAOS_TIMEOUT seconds for every health section to pass. Stops at
# the first round that does not recover. CHAOS_SEED repeats a run.
chaos_cluster() {
  require_cluster_nodes
  local rounds="${CHAOS_ROUNDS:-10}" timeout="${CHAOS_TIMEOUT:-900}" settle="${CHAOS_SETTLE:-20}"
  local seed="${CHAOS_SEED:-$(date +%s)}" kinds=() results=() i j tmp kind start st
  read -r -a kinds <<< "${CHAOS_FAULTS:-${CHAOS_ALL_FAULTS}}"
  RANDOM="${seed}"
  for ((i = ${#kinds[@]} - 1; i > 0; i--)); do
    j=$((RANDOM % (i + 1)))
    tmp="${kinds[i]}"; kinds[i]="${kinds[j]}"; kinds[j]="${tmp}"
  done
  CHAOS_TENANT_APPS="$(list_tenant_apps 2>/dev/null | tr '\n' ' ' | sed 's/[[:space:]]*$//' || true)"
  echo "chaos: rounds=${rounds} seed=${seed} timeout=${timeout}s order=${kinds[*]}"
  echo "tenant apps: ${CHAOS_TENANT_APPS:-none}"
  if ! chaos_wait_healthy "baseline" 300; then
    echo "cluster is not healthy before chaos; fix that first" >&2
    return 1
  fi
  echo "$(chaos_now) baseline healthy"
  for ((i = 1; i <= rounds; i++)); do
    kind="${kinds[$(((i - 1) % ${#kinds[@]}))]}"
    CHAOS_TARGET=""
    st=0
    chaos_inject "${kind}" "${seed}-${i}" || st=$?
    if [[ "${st}" -eq 3 ]]; then
      echo "$(chaos_now) round ${i}/${rounds} ${kind}: skipped (${CHAOS_TARGET})"
      results+=("${i} ${kind} skipped -")
      continue
    elif [[ "${st}" -ne 0 ]]; then
      echo "$(chaos_now) round ${i}/${rounds} ${kind}: fault failed: ${CHAOS_TARGET}" >&2
      results+=("${i} ${kind} inject-failed -")
      break
    fi
    echo "$(chaos_now) round ${i}/${rounds} ${kind}: ${CHAOS_TARGET}"
    start=${SECONDS}
    sleep "${settle}"
    if chaos_wait_healthy "round ${i}" "${timeout}"; then
      echo "$(chaos_now) round ${i}/${rounds} ${kind}: recovered in $((SECONDS - start))s"
      results+=("${i} ${kind} recovered $((SECONDS - start))s")
    else
      results+=("${i} ${kind} FAILED >${timeout}s")
      break
    fi
  done
  echo
  echo "chaos summary (seed ${seed}):"
  printf '  %-6s %-18s %-14s %s\n' ROUND FAULT RESULT TIME
  st=0
  # bash 3.2 + set -u: an empty "${results[@]}" is unbound (CHAOS_ROUNDS=0).
  if [[ ${#results[@]} -gt 0 ]]; then
    for tmp in "${results[@]}"; do
      # shellcheck disable=SC2086
      printf '  %-6s %-18s %-14s %s\n' ${tmp}
      case "${tmp}" in *FAILED*|*inject-failed*) st=1 ;; esac
    done
  fi
  return "${st}"
}
