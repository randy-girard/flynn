#!/bin/bash
# Last community MinIO binary. dl.min.io now returns 410 for all community
# releases (project archived). GitHub still hosts this tag's assets.
set -euo pipefail

apt-get update
apt-get install -y --no-install-recommends ca-certificates curl

# Last tagged build that still published binaries (no assets after this).
MINIO_VERSION="RELEASE.2025-09-07T16-13-09Z"

ARCH="$(dpkg --print-architecture)"
case "${ARCH}" in
  amd64)
    MINIO_ARCH="amd64"
    MINIO_SHA256="7c5bd8512c6e966455b1d198209358b2d191c77a83ab377c4073281065fb855f"
    ;;
  arm64)
    MINIO_ARCH="arm64"
    MINIO_SHA256="5c83cd2cf151717ba0243f73e1c7802ff36e272b67144bdd7f1f7d684fd6f03d"
    ;;
  ppc64el)
    MINIO_ARCH="ppc64le"
    MINIO_SHA256="bf2b20db16d1b9e598ffcaa8c775908b558512346f81d359437125a1f1048d13"
    ;;
  *)
    echo "Unsupported architecture: ${ARCH}" >&2
    exit 1
    ;;
esac

URL="https://github.com/minio/minio/releases/download/${MINIO_VERSION}/minio.linux-${MINIO_ARCH}.${MINIO_VERSION}"
tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT

curl -fsSL --retry 5 --retry-delay 3 --retry-connrefused \
  -o "${tmp}" \
  "${URL}"

echo "${MINIO_SHA256}  ${tmp}" | sha256sum -c -

install -m 0755 "${tmp}" /usr/local/bin/minio
install -m 0755 "${tmp}" /bin/minio
