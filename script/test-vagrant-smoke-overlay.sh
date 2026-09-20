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
need "${vagrant}" 'private_network' \
  "cluster nodes must use a host-only private_network (not NAT forwards) for service ports"
if grep -vE '^\s*#' "${vagrant}" | grep -q 'forwarded_port'; then
  echo "Vagrantfile must not NAT-forward cluster ports (that bypasses guest UFW)" >&2
  exit 1
fi

smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
need "${smoke}" 'vbox_id_for_node' \
  "NIC promisc check must wait for the Vagrant id file (parallel vagrant up can lag)"
need "${smoke}" 'VBoxManage list vms' \
  "NIC promisc check must fall back to VBoxManage list vms if the id file is missing"
need "${smoke}" 'VTEP MAC' "smoke script must diagnose device vs lease VTEP MAC"
need "${smoke}" 'extra_args\+=\(--clean\)' "smoke reinstall on existing VMs must pass --clean"
need "${smoke}" 'for link in flannel.1 flynnbr0' "smoke --clean must delete stale overlay devices"
need "${smoke}" 'cli_arch=arm64' "smoke must install flynn-linux-arm64 on aarch64 nodes"
need "${smoke}" 'flynn version' "smoke must execute the CLI (wrong-arch binaries exist but fail with Exec format error)"
if grep -q 'cli_arch=386' "${smoke}"; then
  echo "smoke must not install 32-bit CLI binaries" >&2
  exit 1
fi
if ! grep -F 'ssh -F "${cfg}"' "${smoke}" >/dev/null; then
  echo "smoke must ssh via cached ssh-config (not vagrant ssh)" >&2
  exit 1
fi
need "${smoke}" 'postgres_is_read_write' "smoke must verify postgres on a cluster node (overlay :5433 is not reachable from the host)"
if ! grep -Fq 'http://${NODE1_IP}/' "${smoke}"; then
  echo "smoke HTTP/app probes must use host-only node IPs, not localhost NAT forwards" >&2
  exit 1
fi
if grep -qE '9079|9442' "${smoke}"; then
  echo "smoke must not use Vagrant NAT-forwarded HTTP/HTTPS host ports" >&2
  exit 1
fi

build="${ROOT}/build.sh"
need "${build}" 'default_gomemlimit' \
  "build.sh must set GOMEMLIMIT so concurrent mksquashfs does not OOM the builder"
need "${build}" '12GiB' \
  "build.sh must default GOMEMLIMIT to 12GiB locally"
need "${build}" '4GiB' \
  "build.sh must default GOMEMLIMIT to 4GiB on GitHub Actions"
need "${build}" 'GITHUB_ACTIONS' \
  "build.sh must detect GitHub Actions for lower memory/concurrency defaults"
need "${build}" '^exit 0$' \
  "build.sh must exit 0 after success (a later phase_start not-found used to fail a completed build)"
need "${ROOT}/builder/build.go" 'Location:  "/mnt"' \
  "layer jobs must bind-mount the host temp dir at /mnt so mksquashfs writes local disk"
if grep -qE 'Device:[[:space:]]+"9p"' "${ROOT}/builder/build.go"; then
  echo "layer jobs must not squash over 9p (host image mksquashfs stalls on netfs_begin_write)" >&2
  exit 1
fi
if [[ -f "${ROOT}/builder/fileserver.go" ]]; then
  echo "builder/fileserver.go served /mnt over 9p; layer out must be a host bind, not 9p" >&2
  exit 1
fi

echo "ok overlay/install/smoke cluster-setup regressions"
