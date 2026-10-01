#!/bin/bash
# Run on a cluster node as root. Install the same pinned Go as the builder
# (builder/img/go.sh) plus squashfs-tools so flynn-host plugin:install can
# compile a local checkout. The builder already has this from setup.sh;
# cluster nodes do not.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"
cd "${ROOT}"

go_sh="${ROOT}/builder/img/go.sh"
if [[ ! -f "${go_sh}" ]]; then
  echo "missing ${go_sh}" >&2
  exit 1
fi
go_version="$(sed -n 's/^go_version="\(.*\)"/\1/p' "${go_sh}" | head -1)"
go_sha256_amd64="$(sed -n 's/^go_sha256_amd64="\(.*\)"/\1/p' "${go_sh}" | head -1)"
go_sha256_arm64="$(sed -n 's/^go_sha256_arm64="\(.*\)"/\1/p' "${go_sh}" | head -1)"
if [[ -z "${go_version}" ]]; then
  echo "could not read go_version from ${go_sh}" >&2
  exit 1
fi

arch="$(dpkg --print-architecture 2>/dev/null || true)"
case "${arch}" in
  amd64) go_sha256="${go_sha256_amd64}" ;;
  arm64) go_sha256="${go_sha256_arm64}" ;;
  *)
    echo "unsupported architecture for pinned Go checksum: ${arch:-unknown}" >&2
    exit 1
    ;;
esac

need_go=1
if [[ -x /usr/local/go/bin/go ]]; then
  have="$(/usr/local/go/bin/go version 2>/dev/null || true)"
  if [[ "${have}" == *"go${go_version} "* ]]; then
    need_go=0
  fi
fi

if [[ "${need_go}" -eq 1 ]]; then
  cache_dir="${ROOT}/ubuntu_ports_cache/go"
  mkdir -p "${cache_dir}"
  tarball="${cache_dir}/go${go_version}.linux-${arch}.tar.gz"
  url="https://go.dev/dl/go${go_version}.linux-${arch}.tar.gz"
  if [[ ! -f "${tarball}" ]]; then
    echo "downloading Go ${go_version} (${arch}) for plugin-build"
    curl --retry 5 --retry-delay 3 -fsSLo "${tarball}.partial" "${url}"
    mv "${tarball}.partial" "${tarball}"
  fi
  echo "${go_sha256}  ${tarball}" | sha256sum -c -
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "${tarball}"
  echo "installed $(/usr/local/go/bin/go version)"
else
  echo "Go ${go_version} already at /usr/local/go/bin/go"
fi

export DEBIAN_FRONTEND=noninteractive
if ! command -v mksquashfs >/dev/null 2>&1 || ! command -v unsquashfs >/dev/null 2>&1; then
  echo "installing squashfs-tools (plugin-build overlays Flynn layers)"
  apt-get update -o Acquire::Retries=5
  apt-get install -y --no-install-recommends squashfs-tools
fi

mkdir -p /etc/profile.d
printf '%s\n' 'export PATH=/usr/local/go/bin:$PATH' > /etc/profile.d/flynn-go.sh
chmod 644 /etc/profile.d/flynn-go.sh

bash "${FLYNN_VAGRANT_GUEST}/ensure-flynn-root.sh"

# sudo flynn-host plugin:install uses secure_path, which omits /usr/local/go/bin.
mkdir -p /etc/sudoers.d
sudoers="/etc/sudoers.d/flynn-go"
cat > "${sudoers}" <<'EOF'
Defaults env_keep += "FLYNN_ROOT"
Defaults secure_path="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/snap/bin:/usr/local/go/bin"
EOF
chmod 440 "${sudoers}"
if command -v visudo >/dev/null 2>&1 && ! visudo -c >/dev/null 2>&1; then
  rm -f "${sudoers}"
  echo "sudoers fragment rejected; relying on PATH in plugin-build env" >&2
fi

export PATH="/usr/local/go/bin:${PATH}"
command -v go >/dev/null
command -v mksquashfs >/dev/null
echo "plugin-build toolchain ready: $(command -v go) $(go version | awk '{print $3}')"
