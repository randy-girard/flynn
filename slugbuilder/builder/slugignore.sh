#!/bin/bash
# Removes files matched by the app's .slugignore (EDGE-23).
#
# .slugignore is tenant input, so build.sh runs this as the unprivileged build
# user (run_unprivileged), and the matching itself never leaves BUILD_DIR:
#   - each line is one glob, relative to BUILD_DIR (leading slashes dropped);
#     spaces, quotes, `$()` and leading dashes are literal characters
#   - patterns with a `..` component are skipped and reported
#   - a match is removed only if its parent directory resolves (symlinks
#     followed) to BUILD_DIR or below; a matched symlink is removed itself,
#     never what it points at
#   - a .slugignore that is itself a symlink is ignored
#
# Usage: slugignore.sh BUILD_DIR   (or source it and call slugignore_prune)

slugignore_say() {
  echo $'\e[1G----->' "$*"
}

# slugignore_trim prints $1 without leading and trailing whitespace.
slugignore_trim() {
  local var="$1"
  var="${var#"${var%%[![:space:]]*}"}"
  var="${var%"${var##*[![:space:]]}"}"
  printf '%s' "${var}"
}

# slugignore_inside succeeds if path $2 stays inside directory $1 (already
# resolved): its parent must resolve to $1 or below, and its last component
# must not be "." or "..".
slugignore_inside() {
  local root="$1" path="$2" parent base
  # drop trailing slashes so "link/" is the symlink, not its target
  while [[ "${path}" == */ && "${path}" != / ]]; do
    path="${path%/}"
  done
  base="${path##*/}"
  if [[ "${base}" == "." || "${base}" == ".." || -z "${base}" ]]; then
    return 1
  fi
  parent="$(cd -P -- "${path%/*}" 2>/dev/null && pwd)" || return 1
  [[ "${parent}" == "${root}" || "${parent}" == "${root}/"* ]]
}

slugignore_prune() {
  local build_dir="$1"
  local file="${build_dir}/.slugignore"
  if [[ -L "${file}" ]]; then
    slugignore_say "Ignoring .slugignore: it is a symlink."
    return 0
  fi
  if [[ ! -f "${file}" ]]; then
    return 0
  fi

  local root
  root="$(cd -P -- "${build_dir}" && pwd)" || return 1

  local restore_nullglob restore_dotglob restore_globstar
  restore_nullglob="$(shopt -p nullglob)" || true
  restore_dotglob="$(shopt -p dotglob)" || true
  restore_globstar="$(shopt -p globstar)" || true
  shopt -s nullglob
  shopt -u dotglob globstar

  local lines=() line glob match deleted=0
  local IFS=$'\n'
  mapfile -t lines < "${file}"
  for line in "${lines[@]}"; do
    glob="$(slugignore_trim "${line}")"
    if [[ -z "${glob}" || "${glob:0:1}" == "#" ]]; then
      continue
    fi
    # remove leading slash(es): patterns are relative to the build directory
    glob="${glob#"${glob%%[!/]*}"}"
    if [[ -z "${glob}" ]]; then
      continue
    fi
    if [[ "/${glob}/" == */../* ]]; then
      slugignore_say "Skipping .slugignore pattern '${glob}': it leaves the build directory."
      continue
    fi
    # IFS is a newline (never in a line), so this expands the glob without
    # splitting it on spaces; the build directory part is quoted.
    for match in "${root}"/${glob}; do
      if ! slugignore_inside "${root}" "${match}"; then
        continue
      fi
      while [[ "${match}" == */ ]]; do
        match="${match%/}"
      done
      if [[ -e "${match}" || -L "${match}" ]]; then
        rm -rf -- "${match}"
        deleted=$((deleted + 1))
      fi
    done
  done

  eval "${restore_nullglob}"
  eval "${restore_dotglob}"
  eval "${restore_globstar}"
  slugignore_say "Deleting ${deleted} files matching .slugignore patterns."
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -eo pipefail
  slugignore_prune "$1"
fi
