#!/bin/bash
# Summarize unique squashfs layer sizes from flynn-builder build/images.json.
# Shared layers are counted once.
#
# Usage:
#   script/report-image-sizes.sh [build/images.json]
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
JSON="${1:-${ROOT}/build/images.json}"

if [[ ! -f "${JSON}" ]]; then
  echo "no images.json at ${JSON}" >&2
  exit 1
fi

python3 - "${JSON}" <<'PY'
import json, sys

path = sys.argv[1]
with open(path) as f:
    artifacts = json.load(f)

def manifest_of(art):
    m = art.get("manifest")
    if m is None:
        return {}
    if isinstance(m, str):
        try:
            return json.loads(m)
        except json.JSONDecodeError:
            return {}
    if isinstance(m, dict):
        return m
    return {}

def layers_of(manifest):
    out = []
    for rf in manifest.get("rootfs") or []:
        for layer in rf.get("layers") or []:
            out.append(layer)
    return out

def fmt(n):
    n = float(n)
    for unit in ("B", "KiB", "MiB", "GiB"):
        if n < 1024.0 or unit == "GiB":
            if unit == "B":
                return f"{int(n)} B"
            return f"{n:.1f} {unit}"
        n /= 1024.0
    return f"{n:.1f} GiB"

unique = {}
rows = []
for name, art in sorted(artifacts.items()):
    layers = layers_of(manifest_of(art))
    total = 0
    for layer in layers:
        lid = layer.get("id") or ""
        length = int(layer.get("length") or 0)
        total += length
        if lid:
            unique[lid] = max(unique.get(lid, 0), length)
    rows.append((name, len(layers), total, int(art.get("size") or 0)))

if not unique and not rows:
    print("no artifacts in", path, file=sys.stderr)
    sys.exit(1)

unique_bytes = sum(unique.values())
print(f"file: {path}")
print(f"images: {len(artifacts)}")
print(f"unique squashfs layers: {len(unique)}")
print(f"unique layer bytes: {unique_bytes} ({fmt(unique_bytes)})")
print("")
print(f"{'image':<22} {'layers':>6} {'layer_sum':>12} {'artifact':>12}")
for name, n, total, size in rows:
    art_s = fmt(size) if size else "-"
    print(f"{name:<22} {n:>6} {fmt(total):>12} {art_s:>12}")
PY
