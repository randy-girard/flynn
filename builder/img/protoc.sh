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
apt-get clean

# install protobuf compiler
curl -sL "${protoc_url}" > /tmp/protoc.zip
unzip -d /usr/local /tmp/protoc.zip
rm /tmp/protoc.zip

# Build protoc-gen-go from the vendored source
cd /mnt/src
mkdir -p /bin
go build -o /bin/protoc-gen-go ./vendor/github.com/golang/protobuf/protoc-gen-go
