#!/bin/bash
# Build the Flynn CLI for this machine and install it to /usr/local/bin.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"
cd "${ROOT}"
export GOFLAGS="${GOFLAGS:--mod=vendor}"
mkdir -p build-dev/bin
os="$(go env GOOS)"
arch="$(go env GOARCH)"
native="build-dev/bin/flynn-${os}-${arch}"
echo "building the flynn CLI (${os}/${arch})"
go build -o "${native}" ./cli
# Vagrant image builds replace build-dev/bin/flynn with a Linux symlink.
# Git credential helpers must use this native install, not that path.
sudo_cmd=(sudo)
if [[ "${FLYNN_VAGRANT_YES:-}" == "1" ]]; then
  sudo_cmd=(sudo -n)
fi
echo "sudo is needed to install flynn to /usr/local/bin"
if ! "${sudo_cmd[@]}" install -m 755 "${ROOT}/${native}" /usr/local/bin/flynn; then
  if [[ "${FLYNN_VAGRANT_YES:-}" == "1" ]]; then
    echo "sudo -n could not install /usr/local/bin/flynn; using ${ROOT}/${native}" >&2
    echo "installed ${ROOT}/${native}"
    exit 0
  fi
  exit 1
fi
echo "installed $(command -v flynn)"
