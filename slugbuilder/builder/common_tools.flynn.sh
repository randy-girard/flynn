#!/bin/bash
#
# Flynn replacement for Heroku buildpack lib/common_tools.sh when the upstream
# bootstraps jq via "jq-linux64" from Heroku's object store (amd64 only). On
# arm64 the cached binary fails with "cannot execute binary file: Exec format error".
#
# The heroku-24 slugbuilder base image installs jq via apt (correct architecture).
# We seed the buildpack cache directory with that binary so the rest of the
# buildpack (which expects ${cache}/.jq/bin/jq) keeps working without SHA checks
# against files.json for jq-linux64.
#
# Sourced after lib/common.sh (see heroku-buildpack-go bin/compile), so addToPATH
# and ${cache} are available.

ensure_jq_for_buildpack() {
  local d="${cache}/.jq/bin"
  local j="${d}/jq"
  if [ -x "${j}" ] && "${j}" --version >/dev/null 2>&1; then
    :
  else
    if ! command -v jq >/dev/null 2>&1; then
      echo "!! error: jq is not installed in the slugbuilder image; install jq in the base image" >&2
      exit 1
    fi
    mkdir -p "${d}"
    cp -f "$(command -v jq)" "${j}"
    chmod a+x "${j}"
  fi
  addToPATH "${d}"
}

ensure_jq_for_buildpack

# Ensure we have a copy of the stdlib
STDLIB_DIR=$(mktemp -d -t stdlib.XXXXX)
BPLOG_PREFIX="buildpack.go"
ensureFile "stdlib.sh.v8" "${STDLIB_DIR}" "chmod a+x"

source_stdlib() {
  source "${STDLIB_DIR}/stdlib.sh.v8"
}
