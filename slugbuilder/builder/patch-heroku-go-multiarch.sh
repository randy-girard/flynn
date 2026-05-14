#!/bin/bash
# Append Flynn multi-arch overrides to heroku-buildpack-go lib/common.sh (idempotent).
set -euo pipefail
OVER="${1:-/builder/flynn-heroku-go-common-overrides.sh}"
if [[ ! -f "${OVER}" ]]; then
  echo "patch-heroku-go-multiarch: missing overrides file: ${OVER}" >&2
  exit 1
fi
while IFS= read -r -d '' bp; do
  if [[ ! -f "${bp}/bin/compile" ]] || [[ ! -f "${bp}/lib/common.sh" ]]; then
    continue
  fi
  if grep -q 'flynn_host_is_arm64' "${bp}/lib/common.sh" 2>/dev/null; then
    continue
  fi
  cat "${OVER}" >>"${bp}/lib/common.sh"
  echo "patch-heroku-go-multiarch: updated ${bp}/lib/common.sh" >&2
done < <(find /builder/buildpacks -maxdepth 1 -type d -name '*heroku-buildpack-go*' -print0)
