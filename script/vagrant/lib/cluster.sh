#!/bin/bash
# Laptop live cluster helpers. The builder compiles Flynn; cluster nodes
# (dev-node1..) run the operator cluster. Requires flynn_vagrant_use.

flynn_vagrant_cluster_nodes() {
  flynn_vagrant_pin_nodes
  local i
  local n
  n="$(flynn_vagrant_get_nodes)"
  if [[ -z "${n}" || "${n}" -lt 1 ]]; then
    return 0
  fi
  for i in $(seq 1 "${n}"); do
    echo "${FLYNN_VAGRANT_NODE_PREFIX}${i}"
  done
}

flynn_vagrant_running_cluster_nodes() {
  local name state
  while IFS= read -r name; do
    [[ -n "${name}" ]] || continue
    state="$(machine_state "${name}")"
    if [[ "${state}" == "running" ]]; then
      echo "${name}"
    fi
  done < <(flynn_vagrant_cluster_nodes)
}

# flynn_vagrant_node_ip is the host-only address for a cluster node VM.
# Dev: 192.168.57.(19+N). Smoke: 192.168.56.(19+N).
flynn_vagrant_node_ip() {
  local name="$1"
  local n="${name#${FLYNN_VAGRANT_NODE_PREFIX}}"
  if [[ ! "${n}" =~ ^[0-9]+$ ]]; then
    echo "not a cluster node: ${name}" >&2
    return 1
  fi
  local octet=$((19 + n))
  case "${FLYNN_VAGRANT_ENV:-}" in
    dev) echo "192.168.57.${octet}" ;;
    smoke) echo "192.168.56.${octet}" ;;
    *)
      echo "FLYNN_VAGRANT_ENV must be smoke or dev" >&2
      return 1
      ;;
  esac
}

flynn_vagrant_peer_ips() {
  local ips=() name
  while IFS= read -r name; do
    [[ -n "${name}" ]] || continue
    ips+=("$(flynn_vagrant_node_ip "${name}")")
  done < <(flynn_vagrant_cluster_nodes)
  if [[ ${#ips[@]} -eq 0 ]]; then
    return 1
  fi
  local IFS=,
  echo "${ips[*]}"
}

# flynn_vagrant_cluster_ip is node1, the bootstrap and laptop CLI host.
flynn_vagrant_cluster_ip() {
  flynn_vagrant_node_ip "${FLYNN_VAGRANT_NODE_PREFIX}1"
}

flynn_vagrant_cluster_domain() {
  printf '%s' "${FLYNN_DEV_DOMAIN:-1.localflynn.com}"
}
