#!/bin/bash
# Run on the builder as root. bootstrap-flynn opens squashfs blobs in
# /var/lib/flynn/layer-cache. A release tarball holds those blobs but does
# not install them there.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${ROOT}"
mkdir -p /var/lib/flynn/layer-cache
if find /var/lib/flynn/layer-cache -name '*.squashfs' -print -quit | grep -q .; then
  missing_json=0
  while read -r blob; do
    id="$(basename "${blob}" .squashfs)"
    if [[ ! -f "/var/lib/flynn/layer-cache/${id}.json" ]]; then
      missing_json=1
      break
    fi
  done < <(find /var/lib/flynn/layer-cache -name '*.squashfs')
  if [[ "${missing_json}" -eq 0 ]]; then
    echo "layer cache already has images"
    exit 0
  fi
fi
tarball="$(ls -t build/release/flynn-*.tar.gz 2>/dev/null | head -1 || true)"
if [[ -z "${tarball}" ]]; then
  echo "no build/release tarball; run script/vagrant-dev.sh build first" >&2
  exit 1
fi
echo "restoring layers from ${tarball}"
tmp="$(mktemp -d)"
tar -xzf "${tarball}" -C "${tmp}" --wildcards '*.squashfs' '*.json'
find "${tmp}" -name '*.squashfs' -exec cp -n {} /var/lib/flynn/layer-cache/ \;
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
