#!/bin/bash
# Run on the builder as root. Apply the newest release tarball with flynn-host.
# The builder PATH often has no flynn-host; the binary is in build/bin.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"
cd "${ROOT}"

tarball="$(bash "${FLYNN_VAGRANT_GUEST}/pack-release.sh" | tail -1)"
if [[ -z "${tarball}" ]]; then
  echo "no build/release tarball; run script/vagrant.sh build" >&2
  exit 1
fi

host_bin=""
for candidate in "${ROOT}/build/bin/flynn-host" /usr/local/libexec/flynn-host; do
  if [[ -x "${candidate}" ]] && ! head -1 "${candidate}" | grep -q '^#!'; then
    host_bin="${candidate}"
    break
  fi
done
if [[ -z "${host_bin}" ]]; then
  echo "flynn-host was not found in build/bin or /usr/local/libexec" >&2
  exit 1
fi

echo "updating from ${tarball} using ${host_bin}"
install -m 0755 "${host_bin}" /usr/local/libexec/flynn-host
export FLYNN_ROOT="${ROOT}"
export PATH="/usr/local/bin:${ROOT}/build/bin:${PATH}"
# Install the tarball into build/bin so start-flynn-host (laptop loop) execs
# the new binary. Default --bin-dir is /usr/local/bin and would leave the
# start-stop-daemon --exec path on the previous inode.
flynn-host update --tarball "${ROOT}/${tarball}" --force --bin-dir="${ROOT}/build/bin"
bash "${FLYNN_VAGRANT_GUEST}/guest-cli.sh"
if ! curl -s --max-time 2 -o /dev/null "http://192.0.2.200:1111/services"; then
  echo "discoverd is not on 192.0.2.200:1111; starting the existing cluster if it was bootstrapped"
  if ! bash "${FLYNN_VAGRANT_GUEST}/ensure-cluster.sh"; then
    echo "cluster did not come back. script/vagrant.sh build used to tear it down; run script/vagrant.sh bootstrap" >&2
    exit 1
  fi
fi
