#!/usr/bin/env bash
# The Flynn sqlite3 vendor replacement must not emit Heroku's empty-glob mv/sed.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
src="${ROOT}/slugbuilder/builder/flynn-python-sqlite3.sh"
if grep -E 'mv[[:space:]]+".*/(include|lib)/"\*' "${src}"; then
  echo "flynn-python-sqlite3.sh still mvs include/lib globs" >&2
  exit 1
fi
if ! grep -q 'sqlite3_copy_if' "${src}"; then
  echo "expected sqlite3_copy_if" >&2
  exit 1
fi
dir="$(mktemp -d)"
trap 'rm -rf "${dir}"' EXIT
mkdir -p "${dir}/pack/bin/steps"
printf '%s\n' '#!/bin/bash' 'echo original sqlite3' > "${dir}/pack/bin/steps/sqlite3"
"${ROOT}/slugbuilder/builder/patch-heroku-python-sqlite.sh" "${src}" "${dir}/pack"
if ! grep -q 'Flynn replacement for heroku-buildpack-python' "${dir}/pack/bin/steps/sqlite3"; then
  echo "compile-time patch did not replace sqlite3" >&2
  exit 1
fi
echo "ok python sqlite3 compile-time patch"
