#!/bin/bash
# Regression: smoke topologies are 1 (singleton) or N>=3 (HA). Flynn rejects
# min-hosts=2. Node count is derived from SMOKE_TOPOLOGIES (Vagrantfile loop
# uses FLYNN_MAX_NODES); 5 is not a special maximum.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
vagrant="${ROOT}/Vagrantfile"

need() {
  local needle=$1 msg=$2
  if ! grep -qE "${needle}" "${smoke}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${smoke}" >&2
    exit 1
  fi
}

need 'SMOKE_TOPOLOGIES' \
  "smoke must take SMOKE_TOPOLOGIES so 1-node and HA sizes can both run"
need 'CLUSTER_SIZE' \
  "CLUSTER_SIZE=N must be a shortcut for a single topology"
need 'SMOKE_TOPOLOGIES="1,3"' \
  "default must run singleton then 3-node HA (1,3)"
need 'cluster_node_ip' \
  "node IPs must be computed (192.168.56.(19+N)), not a hard-coded list"
need 'FLYNN_MAX_NODES' \
  "smoke must export FLYNN_MAX_NODES so Vagrantfile defines node1..max(N)"
need 'expand_cluster_inventory' \
  "fail/teardown must expand node1..N from the run's largest topology"
need 'apply_topology' \
  "cluster size must be applied (NODES/PEER_IPS/MIN_HOSTS) per topology"
need 'run_one_topology' \
  "each topology must get the full install/bootstrap/deploy/upgrade/CLI path"
need 'valid_topology_size' \
  "topology parser must share the 1-or->=3 rule with apply_topology"
need 'TOPOLOGY_LABEL="\$\{size\}-node"' \
  "topology labels must be N-node (1-node, 3-node, 7-node, …)"
if grep -qE 'SMOKE_MAX_NODES=5' "${smoke}"; then
  echo "smoke must not hard-code SMOKE_MAX_NODES=5" >&2
  exit 1
fi
if grep -qE '\(1\.\.5\)' "${vagrant}"; then
  echo "Vagrantfile must not hard-code (1..5); use FLYNN_MAX_NODES" >&2
  exit 1
fi
if ! grep -q 'FLYNN_MAX_NODES' "${vagrant}"; then
  echo "Vagrantfile must loop 1..FLYNN_MAX_NODES" >&2
  exit 1
fi
if ! grep -Fq -- '--min-hosts "${MIN_HOSTS}"' "${smoke}"; then
  echo "bootstrap min-hosts must follow the topology (1 = singleton, N = HA)" >&2
  exit 1
fi
if grep -v '^#' "${smoke}" | grep -qE -- '--min-hosts 3'; then
  echo "bootstrap must not hard-code --min-hosts 3" >&2
  exit 1
fi
if ! grep -Fq 'vagrant up "${NODES[@]}"' "${smoke}"; then
  echo "vagrant up must boot only the current topology's nodes" >&2
  exit 1
fi
if ! grep -Fq 'vagrant destroy -f "${NODES[@]}"' "${smoke}"; then
  echo "teardown must destroy only the current topology's nodes" >&2
  exit 1
fi
need 'CHECK_PHASE_PREFIX' \
  "datastore/CLI report phases must be prefixed N-node/"
need 'RESUME_AT requires a single SMOKE_TOPOLOGIES' \
  "resume/skip-install flags must not mix with multiple topologies"
need 'KEEP_VMS=1: destroying' \
  "KEEP_VMS must still destroy nodes between topologies so the next can boot clean"
if ! grep -F 'bash 3.2 + set -u' "${smoke}" >/dev/null; then
  echo "duplicate-topology check must guard empty TOPOLOGIES for bash 3.2 set -u" >&2
  exit 1
fi
if ! grep -F '${#TOPOLOGIES[@]} -gt 0' "${smoke}" >/dev/null; then
  echo "must not expand \${TOPOLOGIES[*]} while the array is empty (macOS bash 3.2)" >&2
  exit 1
fi

CLUSTER_IP_OFFSET=19
valid_topology_size() {
  local size=$1 octet
  [[ "${size}" =~ ^[0-9]+$ ]] || return 1
  if [[ "${size}" -lt 1 || "${size}" -eq 2 ]]; then
    return 1
  fi
  octet=$((CLUSTER_IP_OFFSET + size))
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

eval_topologies() {
  local SMOKE_TOPOLOGIES="${1:-}"
  local CLUSTER_SIZE="${2:-}"
  if [[ -n "${SMOKE_TOPOLOGIES}" ]]; then
    :
  elif [[ -n "${CLUSTER_SIZE}" ]]; then
    SMOKE_TOPOLOGIES="${CLUSTER_SIZE}"
  else
    SMOKE_TOPOLOGIES="1,3"
  fi
  local raw="${SMOKE_TOPOLOGIES// /}"
  if [[ "${raw}" == "both" ]]; then
    raw="1,3"
  fi
  local item parts
  local TOPOLOGIES=()
  IFS=',' read -r -a parts <<< "${raw}"
  for item in "${parts[@]}"; do
    [[ -z "${item}" ]] && continue
    if ! valid_topology_size "${item}"; then
      echo "reject:${item}"
      return 1
    fi
    if [[ " ${TOPOLOGIES[*]} " == *" ${item} "* ]]; then
      continue
    fi
    TOPOLOGIES+=("${item}")
  done
  echo "${TOPOLOGIES[*]}"
}

got="$(eval_topologies "" "")"
if [[ "${got}" != "1 3" ]]; then
  echo "default topologies must be 1 then 3, got '${got}'" >&2
  exit 1
fi
got="$(eval_topologies "" "1")"
if [[ "${got}" != "1" ]]; then
  echo "CLUSTER_SIZE=1 must select only singleton, got '${got}'" >&2
  exit 1
fi
got="$(eval_topologies "3" "")"
if [[ "${got}" != "3" ]]; then
  echo "SMOKE_TOPOLOGIES=3 must select only 3-node HA, got '${got}'" >&2
  exit 1
fi
got="$(eval_topologies "1,3,5" "")"
if [[ "${got}" != "1 3 5" ]]; then
  echo "SMOKE_TOPOLOGIES=1,3,5 must run singleton then 3 then 5, got '${got}'" >&2
  exit 1
fi
got="$(eval_topologies "1,3,7" "")"
if [[ "${got}" != "1 3 7" ]]; then
  echo "SMOKE_TOPOLOGIES=1,3,7 must be accepted (7 is not a special max), got '${got}'" >&2
  exit 1
fi
got="$(eval_topologies "both" "")"
if [[ "${got}" != "1 3" ]]; then
  echo "SMOKE_TOPOLOGIES=both must expand to 1 3, got '${got}'" >&2
  exit 1
fi
if eval_topologies "2" "" >/dev/null 2>&1; then
  echo "topology size 2 must be rejected (Flynn HA minimum is 3)" >&2
  exit 1
fi
if eval_topologies "236" "" >/dev/null 2>&1; then
  echo "topology size 236 must be rejected (192.168.56.(19+N) last octet > 254)" >&2
  exit 1
fi
if SMOKE_MAX_NODES=4 eval_topologies "5" "" >/dev/null 2>&1; then
  echo "SMOKE_MAX_NODES=4 must reject topology 5" >&2
  exit 1
fi

# macOS /bin/bash is 3.2: set -u + empty ${arr[*]} is "unbound variable".
empty_dup_check() {
  set -euo pipefail
  local TOPOLOGIES=() item=1
  if [[ ${#TOPOLOGIES[@]} -gt 0 && " ${TOPOLOGIES[*]} " == *" ${item} "* ]]; then
    echo "empty TOPOLOGIES must not match ${item}" >&2
    return 1
  fi
  TOPOLOGIES+=(1)
  if [[ ${#TOPOLOGIES[@]} -gt 0 && " ${TOPOLOGIES[*]} " == *" ${item} "* ]]; then
    return 0
  fi
  echo "TOPOLOGIES=(1) must match item=1" >&2
  return 1
}
empty_dup_check

echo "ok smoke topologies are 1 or >=3 (SMOKE_TOPOLOGIES=1,3,5 and 1,3,7)"
