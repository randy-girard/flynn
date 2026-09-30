#!/bin/bash
# Run on a cluster node as root after install-node.sh.
# PEER_IPS is the comma-separated host-only addresses of every cluster node.
# EXTERNAL_IP is this node's host-only address.
set -euo pipefail
: "${PEER_IPS:?PEER_IPS is required}"
: "${EXTERNAL_IP:?EXTERNAL_IP is required}"

flynn-host init --peer-ips "${PEER_IPS}" --external-ip "${EXTERNAL_IP}"
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
