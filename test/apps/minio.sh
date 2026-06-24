#!/bin/bash

set -e

apt-get update
apt-get install -y curl

ARCH="$(dpkg --print-architecture)"

case "$ARCH" in
  amd64)  MINIO_ARCH="amd64" ;;
  arm64)  MINIO_ARCH="arm64" ;;
  armhf)  MINIO_ARCH="arm" ;;
  ppc64el) MINIO_ARCH="ppc64le" ;;
  s390x) MINIO_ARCH="s390x" ;;
  *)
    echo "Unsupported architecture: $ARCH"
    exit 1
    ;;
esac

URL="https://dl.min.io/server/minio/release/linux-${MINIO_ARCH}/minio"

curl -fsSL --retry 5 --retry-delay 3 \
  -o /usr/local/bin/minio \
  "${URL}"

chmod +x /usr/local/bin/minio