# Cluster policy buildpack checks, sourced by build.sh.
#
# FLYNN_ALLOWED_BUILDPACKS is the enterprise cluster policy allowlist, set by
# gitreceive (space or comma separated). Empty allows every buildpack.
# Entries are bundled buildpack names (nodejs, go, multi, ...), "inline" for a
# bin/detect + bin/compile in the app, or a buildpack git URL.

buildpack_policy_lower() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
}

# buildpack_policy_active succeeds when an allowlist is set.
buildpack_policy_active() {
  local stripped="${FLYNN_ALLOWED_BUILDPACKS//[[:space:],]/}"
  [[ -n "${stripped}" ]]
}

# buildpack_policy_name maps a bundled buildpack directory
# (/builder/buildpacks/000004_heroku-buildpack-nodejs) to its policy name
# (nodejs).
buildpack_policy_name() {
  local name
  name="$(basename "$1")"
  if [[ "${name}" =~ ^[0-9]+_(.*)$ ]]; then
    name="${BASH_REMATCH[1]}"
  fi
  name="${name#heroku-buildpack-}"
  name="${name%-buildpack}"
  if [[ "${name}" == "staticfile" ]]; then
    name="static"
  fi
  buildpack_policy_lower "${name}"
}

# buildpack_allowed succeeds when any argument is on the allowlist, or when
# no allowlist is set.
buildpack_allowed() {
  buildpack_policy_active || return 0
  local -a entries
  local entry candidate
  IFS=$', \t\n' read -r -a entries <<< "${FLYNN_ALLOWED_BUILDPACKS}"
  for entry in "${entries[@]}"; do
    [[ -n "${entry}" ]] || continue
    entry="$(buildpack_policy_lower "${entry}")"
    for candidate in "$@"; do
      if [[ "$(buildpack_policy_lower "${candidate}")" == "${entry}" ]]; then
        return 0
      fi
    done
  done
  return 1
}

# buildpack_url_allowed checks a custom buildpack URL. An entry matches the
# URL as given or without its #ref.
buildpack_url_allowed() {
  buildpack_allowed "$1" "${1%%#*}"
}

# buildpack_policy_filter prints the bundled buildpack directories the
# allowlist permits, one per line, keeping their order.
buildpack_policy_filter() {
  local dir
  for dir in "$@"; do
    if buildpack_allowed "$(buildpack_policy_name "${dir}")"; then
      printf '%s\n' "${dir}"
    fi
  done
}
