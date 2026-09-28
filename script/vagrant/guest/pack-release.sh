#!/bin/bash
# Pack build/images.json into build/release/flynn-*.tar.gz when the tarball is
# missing or older than the image manifest. Used after flynn-builder and by
# guest/update.sh so setup/bootstrap can restore layers.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"
cd "${ROOT}"

tarball="$(ls -t build/release/flynn-*.tar.gz 2>/dev/null | head -1 || true)"
images=""
if [[ -f build/manifests/images.json ]]; then
  images="build/manifests/images.json"
elif [[ -f build/images.json ]]; then
  images="build/images.json"
fi
if [[ -z "${images}" ]]; then
  if [[ -n "${tarball}" ]]; then
    echo "${tarball}"
    exit 0
  fi
  echo "no images.json or release tarball" >&2
  exit 1
fi
if [[ -n "${tarball}" && ! "${images}" -nt "${tarball}" ]]; then
  echo "${tarball}"
  exit 0
fi
version=""
if [[ -x build/bin/flynn-host ]]; then
  version="$(build/bin/flynn-host version 2>/dev/null || true)"
fi
if [[ ! "${version}" =~ ^v[0-9]{8}\.[0-9]+$ ]]; then
  # shellcheck source=../../lib/release.sh
  source "${ROOT}/script/lib/release.sh"
  version="$(next_release_version_from_tags)"
fi
echo "packaging ${images} as ${version}" >&2
# Keep stdout to a single relative path; update.sh captures this.
./script/release --target tarball --version "${version}" --output "${ROOT}/build/release" >&2
ls -t build/release/flynn-*.tar.gz | head -1
