#!/bin/bash
# Run on a cluster node as root after vagrant reload (a node crash). Starting
# flynn-host.service restores discoverd/flannel and ConnectPeer rejoins raft.
# install-flynn ships the unit; nested bootstrap-flynn on the builder does not.
set -euo pipefail
DOMAIN="${CLUSTER_DOMAIN:-1.localflynn.com}"
IP="${CLUSTER_IP:-}"

if [[ ! -f /etc/flynn/host.json ]]; then
  echo "cluster not bootstrapped on this node"
  exit 2
fi
if ! systemctl list-unit-files flynn-host.service >/dev/null 2>&1; then
  echo "flynn-host.service is missing; run script/vagrant.sh bootstrap" >&2
  exit 2
fi

# plugin:update compiles against the mounted Flynn tree (SEC-003 Auth-Key).
bash "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ensure-flynn-root.sh"
# aarch64: keep qemu-user binfmt registered after reload (no Flynn reinstall).
bash "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ensure-qemu-binfmt.sh"

systemctl enable flynn-host.service >/dev/null 2>&1 || true
systemctl start flynn-host.service

if [[ -z "${IP}" ]]; then
  IP="$(python3 -c 'import json
e=(json.load(open("/etc/flynn/host.json")) or {}).get("env") or {}
print((e.get("EXTERNAL_IP") or "").strip())' 2>/dev/null || true)"
fi

controller_code() {
  local code
  if [[ -n "${IP}" ]]; then
    code="$(curl -sk --max-time 3 -o /dev/null -w '%{http_code}' \
      --resolve "controller.${DOMAIN}:443:${IP}" \
      "https://controller.${DOMAIN}/" || true)"
    if [[ "${code}" == "200" || "${code}" == "401" ]]; then
      printf '%s' "${code}"
      return
    fi
  fi
  curl -sk --max-time 3 -o /dev/null -w '%{http_code}' \
    "https://controller.${DOMAIN}/" || true
}

for _ in $(seq 1 60); do
  code="$(controller_code)"
  if [[ "${code}" == "200" || "${code}" == "401" ]]; then
    echo "cluster is up"
    exit 0
  fi
  sleep 2
done
echo "flynn-host started but controller never answered" >&2
exit 1
