#!/bin/bash
# Replace heroku-buildpack-python's sqlite3 vendor step with a multiarch-aware
# copy that works on Ubuntu 24.04 / aarch64 Flynn slugbuilders.
# Run at image build and again at compile time (custom BUILDPACK_URL / .buildpacks
# clone an unpatched tree).
set -euo pipefail
REPL="${1:-/builder/flynn-python-sqlite3.sh}"
ROOT="${2:-/builder/buildpacks}"
if [[ ! -f "${REPL}" ]]; then
  echo "patch-heroku-python-sqlite: missing ${REPL}" >&2
  exit 1
fi
[[ -d "${ROOT}" ]] || exit 0
patched=0
while IFS= read -r -d '' step; do
  if grep -q 'Flynn replacement for heroku-buildpack-python' "${step}" 2>/dev/null; then
    continue
  fi
  cp "${REPL}" "${step}"
  chmod +x "${step}"
  echo "patch-heroku-python-sqlite: replaced ${step}" >&2
  patched=1
done < <(find "${ROOT}" -type f -path '*/bin/steps/sqlite3' -print0 2>/dev/null)
# Classic compile sources bin/steps/sqlite3. If the selected pack has no such
# file, leave it; modern heroku-buildpack-python vendors sqlite in the runtime.
if [[ "${patched}" -eq 0 ]]; then
  :
fi
