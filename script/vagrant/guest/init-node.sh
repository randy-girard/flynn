#!/bin/bash
# Run on a cluster node as root after install-node.sh.
# PEER_IPS is the comma-separated host-only addresses of every cluster node.
# EXTERNAL_IP is this node's host-only address.
set -euo pipefail
: "${PEER_IPS:?PEER_IPS is required}"
: "${EXTERNAL_IP:?EXTERNAL_IP is required}"

init_flags=()
if [[ -n "${FLYNN_HOST_TAGS:-}" ]]; then
  IFS=',' read -r -a _tags <<< "${FLYNN_HOST_TAGS}"
  for tag in "${_tags[@]}"; do
    [[ -n "${tag}" ]] && init_flags+=(--tag "${tag}")
  done
fi
if [[ -n "${FLYNN_MAX_PROCESSES:-}" ]]; then
  init_flags+=(--max-processes "${FLYNN_MAX_PROCESSES}")
fi
flynn-host init --peer-ips "${PEER_IPS}" --external-ip "${EXTERNAL_IP}" "${init_flags[@]}"
systemctl enable flynn-host.service
systemctl restart flynn-host.service

for _ in $(seq 1 90); do
  if curl -sf --max-time 2 "http://${EXTERNAL_IP}:1113/host/status" >/dev/null \
    || curl -sf --max-time 2 "http://127.0.0.1:1113/host/status" >/dev/null; then
    echo "flynn-host HTTP API up on ${EXTERNAL_IP}:1113"
    exit 0
  fi
  sleep 2
done
echo "flynn-host HTTP API did not answer on ${EXTERNAL_IP}:1113" >&2
exit 1
