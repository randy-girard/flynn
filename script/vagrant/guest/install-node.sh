#!/bin/bash
# Run on a cluster node as root. Install Flynn from the newest synced
# release tarball (the builder writes build/release/; this VM mounts it).
# Set CLEAN=1 (or AUTO_CLEAN=1 when Flynn is already present) to pass --clean.
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
for _ in $(seq 1 60); do
  if [[ -f "${tarball}" ]]; then
    break
  fi
  sleep 2
done
if [[ ! -f "${tarball}" ]]; then
  echo "tarball not visible on this node: ${tarball}" >&2
  exit 1
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT
tar xzf "${tarball}" -C "${tmpdir}"
install_script="$(find "${tmpdir}" -maxdepth 2 -type f -name install-flynn | head -1)"
if [[ -z "${install_script}" ]]; then
  echo "install-flynn missing from ${tarball}" >&2
  exit 1
fi

extra_args=()
if [[ "${CLEAN:-}" == "1" ]] || { [[ "${AUTO_CLEAN:-1}" == "1" ]] && [[ -e /usr/local/bin/flynn-host || -d /var/lib/flynn ]]; }; then
  echo "existing Flynn install detected; reinstalling with --clean"
  extra_args+=(--clean)
  pkill -KILL -f 'flynn-host libcontainer-init' || true
  pkill -KILL -f '/.containerinit' || true
  pkill -KILL -f '^/bin/discoverd' || true
  pkill -KILL -f '^/usr/bin/flanneld' || true
  if [[ -r /proc/mounts ]]; then
    while read -r mp; do
      [[ -z "${mp}" ]] && continue
      umount -l "${mp}" 2>/dev/null || umount "${mp}" 2>/dev/null || true
    done < <(awk '$2 ~ /^\/var\/lib\/flynn(\/|$)/ { print $2 }' /proc/mounts | sort -r)
  fi
fi

bash "${install_script}" --yes --no-ntp "${extra_args[@]}" --tarball "${tarball}"
if ! command -v ipset >/dev/null 2>&1; then
  echo "installing ipset (required for flynn-host job isolation)"
  export DEBIAN_FRONTEND=noninteractive
  apt-get install -y ipset
fi
command -v flynn-host >/dev/null
command -v ipset >/dev/null
echo "installed Flynn from ${tarball}"
