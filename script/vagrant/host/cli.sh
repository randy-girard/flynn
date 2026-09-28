#!/bin/bash
# Build the Flynn CLI for this machine and install it to /usr/local/bin.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"
cd "${ROOT}"
export GOFLAGS="${GOFLAGS:--mod=vendor}"
# Laptop-loop artifacts stay in ./build-dev so they do not overwrite smoke's ./build.
mkdir -p build-dev/bin
echo "building the flynn CLI"
go build -o build-dev/bin/flynn ./cli
echo "sudo is needed to install flynn to /usr/local/bin"
sudo install -m 755 "${ROOT}/build-dev/bin/flynn" /usr/local/bin/flynn
echo "installed $(command -v flynn)"
