#!/bin/bash
# Host-gate: install scripts must lock down public ingress to 22/80/443 and
# install logrotate for /var/log/flynn. Keep in sync with lib/host-hardening.sh.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=lib/host-hardening.sh
source "${ROOT}/script/lib/host-hardening.sh"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE -- "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

installers=(
  "${ROOT}/script/install-flynn"
  "${ROOT}/script/install-flynn.tmpl"
  "${ROOT}/script/install-flynn-release"
)

for f in "${installers[@]}"; do
  need "${f}" 'configure_flynn_firewall' \
    "${f} must call configure_flynn_firewall"
  need "${f}" 'configure_flynn_logrotate' \
    "${f} must call configure_flynn_logrotate"
  need "${f}" '--no-firewall' \
    "${f} must offer --no-firewall"
  need "${f}" '--no-logrotate' \
    "${f} must offer --no-logrotate"
  need "${f}" 'ufw allow "\$\{port\}/tcp"' \
    "${f} must allow public TCP ports via ufw"
  need "${f}" 'ufw default deny incoming' \
    "${f} must default-deny public ingress"
  need "${f}" '/etc/logrotate.d' \
    "${f} must write /etc/logrotate.d/flynn"
  need "${f}" '/var/log/flynn' \
    "${f} must rotate /var/log/flynn"
  need "${f}" 'copytruncate' \
    "${f} must use copytruncate so open Flynn log writers are not interrupted"
  need "${f}" '"ufw"' \
    "${f} must install the ufw package"
  need "${f}" '"logrotate"' \
    "${f} must install the logrotate package"
done

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

ports="$(flynn_firewall_public_tcp_ports)"
[[ "${ports}" == "22 80 443" ]] || {
  echo "public TCP ports must be exactly 22 80 443, got: ${ports}" >&2
  exit 1
}

plan="$(flynn_firewall_plan)"
echo "${plan}" | grep -qx 'default deny incoming'
echo "${plan}" | grep -qx 'allow 22/tcp'
echo "${plan}" | grep -qx 'allow 80/tcp'
echo "${plan}" | grep -qx 'allow 443/tcp'
echo "${plan}" | grep -qx 'allow from 10.0.0.0/8'
echo "${plan}" | grep -qx 'allow from 172.16.0.0/12'
echo "${plan}" | grep -qx 'allow from 192.168.0.0/16'
echo "${plan}" | grep -qx 'allow from 100.64.0.0/10'
if echo "${plan}" | grep -E 'allow [0-9]+/tcp' | grep -vE 'allow (22|80|443)/tcp'; then
  echo "firewall plan must not allow public TCP ports other than 22/80/443" >&2
  exit 1
fi

export FLYNN_LOGROTATE_DIR="${tmp}/logrotate.d"
export FLYNN_LOG_DIR="${tmp}/flynn-logs"
configure_flynn_logrotate
[[ -f "${FLYNN_LOGROTATE_DIR}/flynn" ]] || {
  echo "configure_flynn_logrotate did not write ${FLYNN_LOGROTATE_DIR}/flynn" >&2
  exit 1
}
grep -q '/var/log/flynn/\*\.log' "${FLYNN_LOGROTATE_DIR}/flynn"
grep -q 'copytruncate' "${FLYNN_LOGROTATE_DIR}/flynn"
grep -q 'daily' "${FLYNN_LOGROTATE_DIR}/flynn"
[[ -d "${FLYNN_LOG_DIR}" ]] || {
  echo "configure_flynn_logrotate did not create ${FLYNN_LOG_DIR}" >&2
  exit 1
}

export FLYNN_FIREWALL_DRY_RUN=1
dry="$(configure_flynn_firewall)"
echo "${dry}" | grep -qx 'allow 22/tcp'
echo "${dry}" | grep -qx 'allow 443/tcp'

need "${ROOT}/host/cli/firewall.go" 'firewall:peer-add' \
  "flynn-host must add peer IPs to the host firewall"
need "${ROOT}/host/cli/firewall.go" 'firewall:peer-remove' \
  "flynn-host must drop peer IPs from the host firewall"
need "${ROOT}/host/cli/firewall.go" 'firewall:expose' \
  "flynn-host must open TCP ports for exposed services"
need "${ROOT}/host/cli/firewall.go" 'firewall:unexpose' \
  "flynn-host must close TCP ports when services are unexposed"
need "${ROOT}/host/host.go" 'startHostFirewall' \
  "flynn-host daemon must reconcile the firewall as peers join and leave"
need "${ROOT}/pkg/hostfw/plan.go" 'KindPeer' \
  "host firewall planner must model per-node peer allows"
need "${ROOT}/pkg/hostfw/plan.go" 'KindExpose' \
  "host firewall planner must model exposed TCP ports"

echo "ok install firewall (22/80/443) and logrotate for /var/log/flynn"
