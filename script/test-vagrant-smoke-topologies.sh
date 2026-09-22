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
need 'apply_topology_spec' \
  "named topologies (add/remove/discovery) must share apply_topology_spec with numeric sizes"
need 'normalize_topology_spec' \
  "add-node/3+1, remove-node/3-1, and discovery-join/1+2 must normalize"
need 'topology_inventory_size' \
  "add must reserve Vagrant node4 (inventory 4) so FLYNN_MAX_NODES covers it"
need 'discovery\) echo 3' \
  "discovery topology must reserve node2 and node3 (inventory 3)"
need 'step_add_cluster_node' \
  "smoke must join a node after the cluster is already running"
need 'step_remove_cluster_node' \
  "smoke must drain a node from a running cluster"
need 'step_verify_membership' \
  "membership changes must re-check HTTP/DBs/deploys"
need 'step_deploy_docker_push_app' \
  "every topology must flynn docker push a pre-built image, not only git-push"
need 'wait_and_assert_docker_apps' \
  "every topology verify/membership phase must probe both Dockerfile paths"
need 'wait_and_assert_buildpack_app' \
  "every topology verify/membership phase must probe the custom .buildpacks app"
need 'slug app git dir missing' \
  "membership must git-push the regular slug app after add/remove"
need 'TEARDOWN_NODES' \
  "teardown must destroy a drained host VM even after it leaves NODES"
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
if ! grep -Fq 'vagrant destroy -f "${NODES[@]}"' "${smoke}"; then
  echo "vagrant up must destroy leftover cluster nodes before boot (KEEP_VMS_ON_FAIL remounts)" >&2
  exit 1
fi
if ! grep -Fq 'vagrant destroy -f "${ALL_CLUSTER_NODES[@]}"' "${smoke}"; then
  echo "vagrant up must destroy leftover nodeN VMs from a larger topology" >&2
  exit 1
fi
if ! grep -Fq 'vagrant up "${NODES[@]}"' "${smoke}"; then
  echo "vagrant up must boot only the current topology's nodes" >&2
  exit 1
fi
if ! grep -Fq 'vagrant destroy -f "${victims[@]}"' "${smoke}"; then
  echo "teardown must destroy TEARDOWN_NODES (drained hosts still need vagrant destroy)" >&2
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

normalize_topology_spec() {
  case "$1" in
    add|add-node|3+1) echo add ;;
    remove|remove-node|3-1) echo remove ;;
    discovery|discovery-join|1+2) echo discovery ;;
    *) echo "$1" ;;
  esac
}

valid_topology_spec() {
  local spec
  spec="$(normalize_topology_spec "$1")"
  case "${spec}" in
    add|remove|discovery) return 0 ;;
    *) valid_topology_size "${spec}" ;;
  esac
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
    if ! valid_topology_spec "${item}"; then
      echo "reject:${item}"
      return 1
    fi
    item="$(normalize_topology_spec "${item}")"
    if [[ ${#TOPOLOGIES[@]} -gt 0 && " ${TOPOLOGIES[*]} " == *" ${item} "* ]]; then
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
got="$(eval_topologies "add,remove" "")"
if [[ "${got}" != "add remove" ]]; then
  echo "SMOKE_TOPOLOGIES=add,remove must run add then remove, got '${got}'" >&2
  exit 1
fi
got="$(eval_topologies "3+1,3-1" "")"
if [[ "${got}" != "add remove" ]]; then
  echo "3+1/3-1 aliases must normalize to add remove, got '${got}'" >&2
  exit 1
fi
got="$(eval_topologies "1,3,add" "")"
if [[ "${got}" != "1 3 add" ]]; then
  echo "numeric and add topologies must compose, got '${got}'" >&2
  exit 1
fi
got="$(eval_topologies "discovery" "")"
if [[ "${got}" != "discovery" ]]; then
  echo "SMOKE_TOPOLOGIES=discovery must select the local-discovery join topology, got '${got}'" >&2
  exit 1
fi
got="$(eval_topologies "1+2" "")"
if [[ "${got}" != "discovery" ]]; then
  echo "1+2 alias must normalize to discovery, got '${got}'" >&2
  exit 1
fi
got="$(eval_topologies "1,3,discovery" "")"
if [[ "${got}" != "1 3 discovery" ]]; then
  echo "numeric and discovery topologies must compose, got '${got}'" >&2
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

echo "ok smoke topologies are 1 or >=3 (SMOKE_TOPOLOGIES=1,3,5 and 1,3,7) plus add/remove/discovery"
