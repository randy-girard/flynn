#!/bin/bash
# Run on the laptop. Writes /etc/hosts, installs the flynn CLI, and adds the
# cluster. Requires sudo once. CLUSTER_PIN and CLUSTER_KEY come from the builder.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
: "${CLUSTER_PIN:?}"
: "${CLUSTER_KEY:?}"

DOMAIN="${FLYNN_DEV_DOMAIN:-1.localflynn.com}"
IP="${FLYNN_DEV_IP:-192.168.57.10}"
names="controller.${DOMAIN} status.${DOMAIN} git.${DOMAIN} dashboard.${DOMAIN} images.${DOMAIN} ${DOMAIN}"
mark="# flynn-vagrant-dev"
line="${IP} ${names} ${mark}"

export GOFLAGS="${GOFLAGS:--mod=vendor}"
"${ROOT}/script/vagrant-dev-cli.sh"

hosts_tmp="$(mktemp)"
if [[ -f /etc/hosts ]]; then
  grep -v 'flynn-vagrant-dev' /etc/hosts > "${hosts_tmp}" || true
else
  : > "${hosts_tmp}"
fi
printf '%s\n' "${line}" >> "${hosts_tmp}"

echo "sudo is needed once to point ${DOMAIN} at ${IP}"
sudo install -m 644 "${hosts_tmp}" /etc/hosts
rm -f "${hosts_tmp}"

flynn cluster:add --force --default -p "${CLUSTER_PIN}" local "${DOMAIN}" "${CLUSTER_KEY}"

ca="${HOME}/.flynn/ca-certs/local.pem"
if [[ -f "${ca}" ]]; then
  echo "trusting the cluster CA for this Mac user"
  security add-trusted-cert -r trustRoot -p ssl -k "${HOME}/Library/Keychains/login.keychain-db" "${ca}" || true
  sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain "${ca}" || true
fi

echo "ready: flynn -c local apps"
echo "ready: open https://status.${DOMAIN}"
echo "inside a git repo that already has a flynn remote, pass -c local"
