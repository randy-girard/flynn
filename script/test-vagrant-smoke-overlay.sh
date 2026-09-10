#!/bin/bash
# Regression: overlay NAT/MAC/promisc and install-time udev pin.
# 2026-09-09: udev MACAddressPolicy=persistent rewrote flannel.1 after the
# lease was published; MASQUERADE without ! -d overlay SNAT'd VXLAN; Vagrant
# NIC2 needed promiscuous mode; --clean left stale flannel.1.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need "${ROOT}/script/install-flynn" '10-flynn-flannel.link' \
  "install-flynn must write /etc/systemd/network/10-flynn-flannel.link"
need "${ROOT}/script/install-flynn" 'MACAddressPolicy=none' \
  "install-flynn must pin udev MACAddressPolicy=none for flannel.*"
need "${ROOT}/script/install-flynn-release" '10-flynn-flannel.link' \
  "install-flynn-release must write /etc/systemd/network/10-flynn-flannel.link"
need "${ROOT}/script/install-flynn" 'OriginalName=flannel\.\*' \
  "install-flynn udev .link must match flannel.*"
need "${ROOT}/script/install-flynn-release" 'MACAddressPolicy=none' \
  "install-flynn-release must pin udev MACAddressPolicy=none for flannel.*"
need "${ROOT}/script/install-flynn-release" 'OriginalName=flannel\.\*' \
  "install-flynn-release udev .link must match flannel.*"

vagrant="${ROOT}/Vagrantfile"
need "${vagrant}" 'FLYNN_MAX_NODES' \
  "Vagrantfile must generate node1..N from FLYNN_MAX_NODES (not a hard-coded count)"
need "${vagrant}" 'nicpromisc2", "allow-all"' \
  "Vagrantfile must set --nicpromisc2 allow-all on cluster node NICs (flannel VXLAN)"
need "${vagrant}" '192\.168\.56\.\#\{19 \+ i\}' \
  "cluster node N must be 192.168.56.(19+N) (node1=.20)"

smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
need "${smoke}" 'VTEP MAC' "smoke script must diagnose device vs lease VTEP MAC"
need "${smoke}" 'extra_args\+=\(--clean\)' "smoke reinstall on existing VMs must pass --clean"
need "${smoke}" 'for link in flannel.1 flynnbr0' "smoke --clean must delete stale overlay devices"
need "${smoke}" 'cli_arch=arm64' "smoke must install flynn-linux-arm64 on aarch64 nodes"
need "${smoke}" 'flynn version' "smoke must execute the CLI (wrong-arch binaries exist but fail with Exec format error)"
if ! grep -F 'ssh -F "${cfg}"' "${smoke}" >/dev/null; then
  echo "smoke must ssh via cached ssh-config (not vagrant ssh)" >&2
  exit 1
fi
need "${smoke}" 'postgres_is_read_write' "smoke must verify postgres on a cluster node (overlay :5433 is not reachable from the host)"

build="${ROOT}/build.sh"
need "${build}" '12GiB' \
  "build.sh must set GOMEMLIMIT so concurrent mksquashfs does not OOM the builder"
if ! grep -F 'GOMEMLIMIT="${GOMEMLIMIT:-12GiB}"' "${build}" >/dev/null; then
  echo "build.sh must default GOMEMLIMIT to 12GiB" >&2
  exit 1
fi
need "${build}" '^exit 0$' \
  "build.sh must exit 0 after success (a later phase_start not-found used to fail a completed build)"

echo "ok overlay/install/smoke cluster-setup regressions"
