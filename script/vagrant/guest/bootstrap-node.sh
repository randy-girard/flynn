#!/bin/bash
# Run on cluster node1 as root after every node has flynn-host init.
# This is flynn-host bootstrap, the same Layer-1 path smoke tests.
set -euo pipefail
: "${PEER_IPS:?PEER_IPS is required}"
: "${MIN_HOSTS:?MIN_HOSTS is required}"
DOMAIN="${CLUSTER_DOMAIN:-1.localflynn.com}"
export CLUSTER_DOMAIN="${DOMAIN}"

manifest="/etc/flynn/bootstrap-manifest.json"
if [[ ! -f "${manifest}" ]]; then
  echo "missing ${manifest}; install Flynn on this node first" >&2
  exit 1
fi

flynn-host bootstrap --min-hosts "${MIN_HOSTS}" --peer-ips "${PEER_IPS}" \
  --admin-email "${FLYNN_ADMIN_EMAIL:-admin@${DOMAIN}}" \
  --admin-password "${FLYNN_ADMIN_PASSWORD:-flynn-dev}" \
  "${manifest}"
umask 077
cat > /etc/flynn/admin.env <<EOF
FLYNN_ADMIN_EMAIL=${FLYNN_ADMIN_EMAIL:-admin@${DOMAIN}}
FLYNN_ADMIN_PASSWORD=${FLYNN_ADMIN_PASSWORD:-flynn-dev}
EOF
echo "bootstrapped ${DOMAIN} min-hosts=${MIN_HOSTS} peer-ips=${PEER_IPS}"
