#!/bin/bash
# Build the Flynn CLI for this machine and install it to /usr/local/bin.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"
cd "${ROOT}"
export GOFLAGS="${GOFLAGS:--mod=vendor}"
mkdir -p build-dev/bin
echo "building the flynn CLI"
go build -o build-dev/bin/flynn ./cli
sudo_cmd=(sudo)
if [[ "${FLYNN_VAGRANT_YES:-}" == "1" ]]; then
  sudo_cmd=(sudo -n)
fi
echo "sudo is needed to install flynn to /usr/local/bin"
if ! "${sudo_cmd[@]}" install -m 755 "${ROOT}/build-dev/bin/flynn" /usr/local/bin/flynn; then
  if [[ "${FLYNN_VAGRANT_YES:-}" == "1" ]]; then
    echo "sudo -n could not install /usr/local/bin/flynn; using ${ROOT}/build-dev/bin/flynn" >&2
    echo "installed ${ROOT}/build-dev/bin/flynn"
    exit 0
  fi
  exit 1
fi
echo "installed $(command -v flynn)"
