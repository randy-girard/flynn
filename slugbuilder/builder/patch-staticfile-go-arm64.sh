#!/bin/bash
# Replace staticfile-buildpack scripts/install_go.sh with the Flynn toolchain
# selector. Idempotent: a script that already contains the marker is left as is.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repl="${here}/staticfile-install-go.flynn.sh"
root="${1:-/builder/buildpacks}"

if [[ ! -f "${repl}" ]]; then
  echo "patch-staticfile-go-arm64: missing ${repl}" >&2
  exit 1
fi
if [[ ! -d "${root}" ]]; then
  echo "patch-staticfile-go-arm64: missing buildpack root ${root}" >&2
  exit 1
fi

found=0
while IFS= read -r -d '' bp; do
  found=1
  dest="${bp}/scripts/install_go.sh"
  if [[ ! -f "${dest}" ]]; then
    echo "patch-staticfile-go-arm64: ${bp} has no scripts/install_go.sh" >&2
    exit 1
  fi
  if grep -q 'flynn_staticfile_go_arm64' "${dest}" 2>/dev/null; then
    continue
  fi
  cp "${repl}" "${dest}"
  chmod +x "${dest}"
  echo "patch-staticfile-go-arm64: updated ${dest}" >&2
done < <(find "${root}" -maxdepth 1 -type d -name '*staticfile*' -print0)

if [[ "${found}" -eq 0 ]]; then
  echo "patch-staticfile-go-arm64: no staticfile buildpack under ${root}" >&2
  exit 1
fi
