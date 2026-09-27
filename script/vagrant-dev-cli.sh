#!/bin/bash
# Build the Flynn CLI for this machine and install it to /usr/local/bin.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${ROOT}"
export GOFLAGS="${GOFLAGS:--mod=vendor}"
mkdir -p build/bin
echo "building the flynn CLI"
go build -o build/bin/flynn ./cli
echo "sudo is needed to install flynn to /usr/local/bin"
sudo install -m 755 "${ROOT}/build/bin/flynn" /usr/local/bin/flynn
echo "installed $(command -v flynn)"
