#!/bin/bash

set -eo pipefail

protoc_version="3.11.0"

case "$(dpkg --print-architecture)" in
  amd64)
    PROTOC_ARCH="x86_64"
    ;;
  arm64)
    PROTOC_ARCH="aarch_64"
    ;;
  *)
    echo "Unsupported architecture: $(dpkg --print-architecture)"
    exit 1
    ;;
esac

protoc_url="https://github.com/google/protobuf/releases/download/v${protoc_version}/protoc-${protoc_version}-linux-${PROTOC_ARCH}.zip"


apt-get update
apt-get install --yes unzip

# install protobuf compiler
curl -sL "${protoc_url}" > /tmp/protoc.zip
unzip -d /usr/local /tmp/protoc.zip
rm /tmp/protoc.zip

# protoc-gen-go: do not build from ./vendor — `go mod vendor` only keeps imported
# packages, so cmd/protoc-gen-go has no main package in vendor ("no Go files").
# Install pinned versions (same as repo stubs / gRPC-Go toolchain).
cd /mnt/src
mkdir -p /bin
export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
export GOSUMDB="${GOSUMDB:-sum.golang.org}"
# Install pinned tools. The Flynn Go image wraps `go` with go-wrapper.sh, which sets
# GOFLAGS=-mod=vendor when GOFLAGS is empty; that blocks `go install` from fetching
# toolchain modules. Force module mode for these two installs only.
GOFLAGS=-mod=mod GOBIN=/bin go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6
GOFLAGS=-mod=mod GOBIN=/bin go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.3.0

if ! mountpoint -q /var/cache/apt/archives 2>/dev/null; then
  rm -rf /var/cache/apt/archives/* "/var/cache/apt/archives/partial"/*
fi
if ! mountpoint -q /var/lib/apt/lists 2>/dev/null; then
  rm -rf /var/lib/apt/lists/*
fi
