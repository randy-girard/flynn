#!/bin/bash
# Run on a running cluster node (usually node1) as root.
# Applies the newest builder tarball with flynn-host update --all-nodes.
# The builder is not a cluster member and is not updated here.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"
cd "${ROOT}"

tarball="${TARBALL:-}"
if [[ -z "${tarball}" ]]; then
  tarball="$(ls -t build/release/flynn-*.tar.gz 2>/dev/null | head -1 || true)"
fi
if [[ -z "${tarball}" ]]; then
  echo "no build/release tarball; run script/vagrant.sh build" >&2
  exit 1
fi
if [[ "${tarball}" != /* ]]; then
  tarball="${ROOT}/${tarball}"
fi
if [[ ! -f "${tarball}" ]]; then
  echo "tarball not visible on this node: ${tarball}" >&2
  exit 1
fi
if ! command -v flynn-host >/dev/null 2>&1; then
  echo "flynn-host is not installed on this node; run script/vagrant.sh bootstrap" >&2
  exit 1
fi

echo "updating cluster from ${tarball} using $(command -v flynn-host)"
flynn-host update --all-nodes --tarball "${tarball}" --force
bash "${FLYNN_VAGRANT_GUEST}/ensure-flynn-root.sh"
# Older node-dns.sh omitted auth.<domain>. flynn login then waits on
# systemd-resolved for auth.1.localflynn.com instead of /etc/hosts.
if [[ -z "${CLUSTER_IP:-}" ]]; then
  CLUSTER_IP="$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -E '^192\.168\.57\.' | head -1 || true)"
fi
if [[ -n "${CLUSTER_IP:-}" ]]; then
  CLUSTER_IP="${CLUSTER_IP}" CLUSTER_DOMAIN="${CLUSTER_DOMAIN:-1.localflynn.com}" \
    bash "${FLYNN_VAGRANT_GUEST}/node-dns.sh"
fi
# This script runs on node1; vagrant.sh also runs ensure-qemu-binfmt on every
# cluster node. Keep it here so a direct guest invoke still registers binfmt.
bash "${FLYNN_VAGRANT_GUEST}/ensure-qemu-binfmt.sh"
echo "cluster update complete"
