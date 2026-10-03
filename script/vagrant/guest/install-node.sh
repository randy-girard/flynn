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

# install-flynn ships flynn-host, not the operator CLI. Put flynn on PATH so
# probe / flynn apps work on the node when laptop sudo cannot cluster:add.
cli_gz=""
case "$(uname -m)" in
  aarch64|arm64) cli_gz="flynn-linux-arm64.gz" ;;
  x86_64|amd64) cli_gz="flynn-linux-amd64.gz" ;;
esac
if [[ -n "${cli_gz}" ]]; then
  cli_src="$(find "${tmpdir}" -maxdepth 2 -type f -name "${cli_gz}" | head -1 || true)"
  if [[ -n "${cli_src}" ]]; then
    gzip -dc "${cli_src}" > /usr/local/bin/flynn
    chmod 0755 /usr/local/bin/flynn
    echo "installed Flynn CLI from ${cli_src}"
  fi
fi

if ! command -v ipset >/dev/null 2>&1; then
  echo "installing ipset (required for flynn-host job isolation)"
  export DEBIAN_FRONTEND=noninteractive
  apt-get install -y ipset
fi
# aarch64: host binfmt so slugbuilder can exec x86_64 Heroku Node binaries.
bash "${FLYNN_VAGRANT_GUEST}/ensure-qemu-binfmt.sh"
# Local plugin:install compiles on this node (sibling checkouts under
# /opt/flynn-plugins). ensure-go.sh also persists FLYNN_ROOT so plugin-build
# compiles against this Flynn (DISCOVERD_AUTH_KEY). The builder has Go from
# setup.sh; cluster nodes do not.
bash "${FLYNN_VAGRANT_GUEST}/ensure-go.sh"
# ensure-go.sh exports PATH in its own process. This login shell started
# before /etc/profile.d/flynn-go.sh existed, so pick Go up here too.
export PATH="/usr/local/go/bin:${PATH}"
command -v flynn-host >/dev/null
command -v ipset >/dev/null
command -v go >/dev/null
echo "installed Flynn from ${tarball}"
