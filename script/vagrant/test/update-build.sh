#!/bin/bash
# Stamp vs dirty/docs for vagrant update rebuild detection. No VM.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
helper="${ROOT}/script/vagrant/guest/build-needed.sh"
# shellcheck source=../guest/build-needed.sh
source "${helper}"

if ! bash -n "${helper}"; then
  echo "build-needed.sh failed bash -n" >&2
  exit 1
fi

TMP="$(mktemp -d "${TMPDIR:-/tmp}/vagrant-update-build.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT

git -C "${TMP}" init -q
git -C "${TMP}" config user.email test@example.com
git -C "${TMP}" config user.name test
mkdir -p "${TMP}/host" "${TMP}/docs" "${TMP}/build/release"
echo 'package host' > "${TMP}/host/main.go"
echo '# docs' > "${TMP}/docs/index.md"
git -C "${TMP}" add host/main.go docs/index.md
git -C "${TMP}" commit -q -m initial

ROOT="${TMP}"
export ROOT

if ! flynn_vagrant_cluster_build_needed; then
  echo "empty build/release must need a cluster build" >&2
  exit 1
fi

touch "${TMP}/build/release/flynn-v20000101.0.tar.gz"
flynn_vagrant_write_build_stamp
if flynn_vagrant_cluster_build_needed; then
  echo "matching stamp must skip rebuild" >&2
  exit 1
fi

echo 'package host // dirty' > "${TMP}/host/main.go"
if ! flynn_vagrant_cluster_build_needed; then
  echo "dirty Go source must need a rebuild" >&2
  exit 1
fi
git -C "${TMP}" checkout -q -- host/main.go

echo 'more docs' >> "${TMP}/docs/index.md"
if flynn_vagrant_cluster_build_needed; then
  echo "docs-only dirty must not force a rebuild" >&2
  exit 1
fi

echo "ok update builds first when Flynn source changed"
