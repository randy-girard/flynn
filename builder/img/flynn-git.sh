#!/bin/bash
# Flynn-builder git mirror cache: bare mirrors live under ${FLYNN_GIT_CACHE_ROOT}
# (_git_mirrors/ on host). For `git clone <url> [<dir>]` we refresh a host-side mirror
# and then perform a local clone, eliminating the upstream fetch on cache hits.
# Anything we cannot safely intercept is delegated to the real git.
set -euo pipefail

REAL_GIT="${REAL_GIT:-/usr/bin/git}"
exec_real() { exec "${REAL_GIT}" "$@"; }

[[ "${FLYNN_NO_GIT_CACHE:-}" == "1" ]] && exec_real "$@"

ROOT="${FLYNN_GIT_CACHE_ROOT:-}"
[[ -z "${ROOT}" || ! -d "${ROOT}" ]] && exec_real "$@"

# Only intercept the top-level "git clone" verb. Any other subcommand passes through.
sub=""
for arg in "$@"; do
  case "${arg}" in
  -*) continue ;;
  *) sub="${arg}"; break ;;
  esac
done
[[ "${sub}" == "clone" ]] || exec_real "$@"

# Walk the argv collecting safe clone flags. Anything we do not recognise as a
# clone-time option that can be replayed against a local mirror means we bail to
# the real git (e.g. --reference, --separate-git-dir, --template, --shallow-since).
# Pre-"clone" git-level flags (e.g. -C, --git-dir) also force passthrough.
declare -a clone_flags=()
declare -a post_url=()
url=""
target=""
saw_clone=false
skip_next=false

safe_flag() {
  case "$1" in
  -q | --quiet | -v | --verbose | --progress | --no-progress | \
    --recurse-submodules | --recursive | --no-recurse-submodules | \
    --no-checkout | -n | --no-tags | --tags | \
    --single-branch | --no-single-branch | \
    --shallow-submodules | --no-shallow-submodules | \
    --no-remote-submodules | --remote-submodules | \
    --filter | --filter=* | --depth | --depth=* | \
    --branch | --branch=* | -b | \
    --origin | --origin=* | -o | \
    --jobs | --jobs=* | -j) return 0 ;;
  esac
  return 1
}

flag_takes_value() {
  case "$1" in
  --filter | --depth | --branch | -b | --origin | -o | --jobs | -j) return 0 ;;
  esac
  return 1
}

for arg in "$@"; do
  if [[ "${skip_next}" == true ]]; then
    clone_flags+=("${arg}")
    skip_next=false
    continue
  fi
  if [[ "${saw_clone}" == false ]]; then
    # Any pre-"clone" token that is not the literal "clone" verb forces passthrough.
    [[ "${arg}" == "clone" ]] || exec_real "$@"
    saw_clone=true
    continue
  fi
  if [[ -z "${url}" ]]; then
    if [[ "${arg}" == -* ]]; then
      safe_flag "${arg}" || exec_real "$@"
      clone_flags+=("${arg}")
      flag_takes_value "${arg}" && skip_next=true
      continue
    fi
    url="${arg}"
    continue
  fi
  if [[ -z "${target}" && "${arg}" != -* ]]; then
    target="${arg}"
    continue
  fi
  post_url+=("${arg}")
done

# Refuse to handle anything we did not fully parse (extra positionals, dangling flags).
[[ "${skip_next}" == true ]] && exec_real "$@"
[[ "${#post_url[@]}" -eq 0 ]] || exec_real "$@"
[[ -n "${url}" ]] || exec_real "$@"

# Only cache schemes that make sense for a public mirror. file:// is included so
# local-bare-repo test rigs can exercise the cache path.
case "${url}" in
http://* | https://* | git://* | ssh://git@* | git@*:* | file://*) ;;
*) exec_real "$@" ;;
esac

key="$(printf '%s' "${url}" | sha256sum | awk '{print $1}')"
mirror="${ROOT}/${key}.git"
stamp="${mirror}/.flynn-last-refresh"
ttl="${FLYNN_GIT_CACHE_TTL:-3600}"

needs_fetch=true
if [[ -d "${mirror}" && -f "${stamp}" ]]; then
  age=$(( $(date +%s) - $(stat -c %Y "${stamp}" 2>/dev/null || echo 0) ))
  [[ "${age}" -lt "${ttl}" ]] && needs_fetch=false
fi

if [[ ! -d "${mirror}" ]]; then
  # First-time mirror: full clone --mirror. Failure falls back to direct clone.
  tmp_mirror="${mirror}.partial.$$"
  rm -rf "${tmp_mirror}"
  if ! "${REAL_GIT}" clone --mirror -- "${url}" "${tmp_mirror}" >&2; then
    rm -rf "${tmp_mirror}"
    exec_real "$@"
  fi
  mv "${tmp_mirror}" "${mirror}"
  date +%s > "${stamp}" || true
elif [[ "${needs_fetch}" == true ]]; then
  # Best-effort refresh; stale-mirror clones are still preferable to a network round-trip.
  if "${REAL_GIT}" --git-dir="${mirror}" remote update --prune >&2; then
    date +%s > "${stamp}" || true
  fi
fi

# Replay the original clone against the local mirror; preserve the user's flags so
# --branch / --depth / --filter / etc. behave the same.
declare -a final=("clone")
[[ "${#clone_flags[@]}" -gt 0 ]] && final+=("${clone_flags[@]}")
final+=("${mirror}")
[[ -n "${target}" ]] && final+=("${target}")

exec "${REAL_GIT}" "${final[@]}"
