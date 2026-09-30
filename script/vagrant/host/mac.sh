#!/bin/bash
# Run on the laptop. Writes /etc/hosts, installs the flynn CLI, and adds the
# cluster. Requires sudo once. CLUSTER_PIN and CLUSTER_KEY come from node1.
# The live cluster is on cluster nodes (default dev-node1 at 192.168.57.20),
# not the builder.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"
: "${CLUSTER_PIN:?}"
: "${CLUSTER_KEY:?}"

DOMAIN="${FLYNN_DEV_DOMAIN:-1.localflynn.com}"
IP="${FLYNN_DEV_CLUSTER_IP:-192.168.57.20}"
names="controller.${DOMAIN} status.${DOMAIN} git.${DOMAIN} dashboard.${DOMAIN} images.${DOMAIN} ${DOMAIN}"
mark="# flynn-vagrant-dev"
line="${IP} ${names} ${mark}"

export GOFLAGS="${GOFLAGS:--mod=vendor}"
"${FLYNN_VAGRANT_HOST}/cli.sh"

sudo_cmd=(sudo)
if [[ "${FLYNN_VAGRANT_YES:-}" == "1" ]]; then
  sudo_cmd=(sudo -n)
fi

hosts_tmp="$(mktemp)"
if [[ -f /etc/hosts ]]; then
  grep -v 'flynn-vagrant-dev' /etc/hosts > "${hosts_tmp}" || true
else
  : > "${hosts_tmp}"
fi
printf '%s\n' "${line}" >> "${hosts_tmp}"

echo "sudo is needed once to point ${DOMAIN} at ${IP}"
if ! "${sudo_cmd[@]}" install -m 644 "${hosts_tmp}" /etc/hosts; then
  rm -f "${hosts_tmp}"
  if [[ "${FLYNN_VAGRANT_YES:-}" == "1" ]]; then
    echo "sudo -n could not write /etc/hosts; skip laptop cluster:add and probe from the node" >&2
    exit 0
  fi
  exit 1
fi
rm -f "${hosts_tmp}"

export FLYNN_SKIP_UPDATE_CHECK=1
PATH="${ROOT}/build-dev/bin:${PATH}"
flynn cluster:add --force --default -p "${CLUSTER_PIN}" local "${DOMAIN}" "${CLUSTER_KEY}"
if ! flynn -c local apps >/dev/null; then
  echo "controller rejected the cluster key on GET /apps (cluster:add only checks TLS/CA)." >&2
  echo "Re-run setup after pulling the latest script/vagrant/guest/creds.sh, or from the VM: sudo flynn-host cli-add-command" >&2
  exit 1
fi

ca="${HOME}/.flynn/ca-certs/local.pem"
if [[ -f "${ca}" ]]; then
  echo "trusting the cluster CA for this Mac user"
  security add-trusted-cert -r trustRoot -p ssl -k "${HOME}/Library/Keychains/login.keychain-db" "${ca}" || true
  "${sudo_cmd[@]}" security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain "${ca}" || true
fi

echo "ready: flynn -c local apps"
echo "ready: open https://status.${DOMAIN}"
echo "inside a git repo that already has a flynn remote, pass -c local"
