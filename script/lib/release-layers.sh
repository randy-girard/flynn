# Verify images.json layer IDs against squashfs blobs in a directory.
#
# The v20260919.1 GitHub Release listed a slugrunner overlay in images.json
# whose squashfs was never packaged (builder reused {id}.json without the blob).
# Hosts then 404'd that layer on flynn-host update.
#
# shellcheck shell=bash

# Print unique layer IDs from images.json or images.json.gz, one per line.
flynn_images_json_layer_ids() {
  local json=$1
  if [[ -z "${json}" || ! -f "${json}" ]]; then
    echo "ERROR: images.json not found: ${json:-<empty>}" >&2
    return 1
  fi
  python3 - "${json}" <<'PY'
import gzip, json, sys

path = sys.argv[1]
opener = gzip.open if path.endswith(".gz") else open
with opener(path, "rt") as f:
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

ids = []
seen = set()
for art in artifacts.values() if isinstance(artifacts, dict) else []:
    if not isinstance(art, dict):
        continue
    manifest = manifest_of(art)
    for rf in manifest.get("rootfs") or []:
        for layer in rf.get("layers") or []:
            lid = (layer or {}).get("id") or ""
            if lid and lid not in seen:
                seen.add(lid)
                ids.append(lid)

if not ids:
    print("ERROR: no layer ids in", path, file=sys.stderr)
    sys.exit(1)
for lid in ids:
    print(lid)
PY
}

# Fail unless every images.json layer has <id>.squashfs in dir.
flynn_verify_images_json_layers() {
  local json=$1
  local dir=$2
  local ids id missing=0
  ids="$(flynn_images_json_layer_ids "${json}")" || return 1
  while IFS= read -r id; do
    [[ -z "${id}" ]] && continue
    if [[ ! -f "${dir}/${id}.squashfs" ]]; then
      echo "ERROR: images.json layer ${id} has no ${id}.squashfs in ${dir}" >&2
      missing=1
    fi
  done <<< "${ids}"
  if [[ "${missing}" -ne 0 ]]; then
    echo "ERROR: images.json lists layers that are not packaged as squashfs assets" >&2
    return 1
  fi
  echo "Verified $(printf '%s\n' "${ids}" | grep -c . || true) images.json layers have squashfs in ${dir}"
}
