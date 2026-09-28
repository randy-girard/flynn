#!/bin/bash
# Run on the builder as root. bootstrap-flynn opens squashfs blobs in
# /var/lib/flynn/layer-cache. A release tarball holds those blobs but does
# not install them there.
#
# Exit 0 if the cache is ready (already populated, or restored from tarball).
# Exit 2 if there is nothing to restore — the laptop loop then builds images
# instead of telling the operator to run build and setup again.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"
cd "${ROOT}"
mkdir -p /var/lib/flynn/layer-cache

cache_ready() {
  if ! find /var/lib/flynn/layer-cache -name '*.squashfs' -print -quit | grep -q .; then
    return 1
  fi
  while read -r blob; do
    id="$(basename "${blob}" .squashfs)"
    if [[ ! -f "/var/lib/flynn/layer-cache/${id}.json" ]]; then
      return 1
    fi
  done < <(find /var/lib/flynn/layer-cache -name '*.squashfs')
}

tarball="$(ls -t build/release/flynn-*.tar.gz 2>/dev/null | head -1 || true)"
if [[ -z "${tarball}" ]]; then
  if cache_ready; then
    echo "layer cache already has images"
    exit 0
  fi
  echo "no cluster images yet" >&2
  exit 2
fi

echo "restoring layers from ${tarball}"
tmp="$(mktemp -d)"
tar -xzf "${tarball}" -C "${tmp}" --wildcards '*.squashfs' '*.json'
# Skip blobs already in the cache. Do not use GNU cp no-clobber (Ubuntu 24.04
# warns that flag is non-portable).
while read -r blob; do
  dest="/var/lib/flynn/layer-cache/$(basename "${blob}")"
  if [[ ! -e "${dest}" ]]; then
    cp "${blob}" "${dest}"
  fi
done < <(find "${tmp}" -name '*.squashfs')
# A .json sidecar pins the blob so host disk cleanup does not delete it.
find /var/lib/flynn/layer-cache -name '*.squashfs' | while read -r blob; do
  id="$(basename "${blob}" .squashfs)"
  found="$(find "${tmp}" -name "${id}.json" -print -quit || true)"
  if [[ -n "${found}" && ! -f "/var/lib/flynn/layer-cache/${id}.json" ]]; then
    cp "${found}" "/var/lib/flynn/layer-cache/${id}.json"
  fi
done
rm -rf "${tmp}"
echo "layer cache restored"
