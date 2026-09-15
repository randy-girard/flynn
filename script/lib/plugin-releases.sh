# Dispatch plugin "Build and Release" workflows after a Flynn GitHub Release
# is published. Repo list comes from PLUGIN_RELEASE_REPOS (Actions variable),
# never a hardcoded appliance name in Flynn.

plugin_release_version_ok() {
  [[ "${1:-}" =~ ^v[0-9]{8}\.[0-9]+$ ]]
}

# Normalize owner/repo from a URL or short name. Empty input is invalid.
plugin_release_normalize_repo() {
  local tok=$1
  tok="${tok#"${tok%%[![:space:]]*}"}"
  tok="${tok%"${tok##*[![:space:]]}"}"
  [[ -z "${tok}" || "${tok}" == \#* ]] && return 1
  tok="${tok#https://github.com/}"
  tok="${tok#http://github.com/}"
  tok="${tok#git@github.com:}"
  tok="${tok%.git}"
  tok="${tok%/}"
  if [[ ! "${tok}" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]]; then
    return 1
  fi
  printf '%s\n' "${tok}"
}

# Print unique owner/repo lines from a variable blob (newlines, commas, comments).
plugin_release_repos_from_text() {
  local raw=$1
  local line tok seen=$'\n'
  raw=$(printf '%s\n' "${raw}" | tr ',;' '\n\n' | tr -d '\r')
  while IFS= read -r line || [[ -n "${line}" ]]; do
    tok="$(plugin_release_normalize_repo "${line}" 2>/dev/null)" || {
      line="${line#"${line%%[![:space:]]*}"}"
      line="${line%"${line##*[![:space:]]}"}"
      if [[ -n "${line}" && "${line}" != \#* ]]; then
        echo "invalid plugin repo '${line}'" >&2
        return 1
      fi
      continue
    }
    case "${seen}" in
      *$'\n'"${tok}"$'\n'*) continue ;;
    esac
    seen="${seen}${tok}"$'\n'
    printf '%s\n' "${tok}"
  done <<EOF
${raw}
EOF
}

plugin_release_exists() {
  local repo=$1 version=$2
  local gh=${GH:-gh}
  "${gh}" release view "${version}" --repo "${repo}" >/dev/null 2>&1
}

# Queue each plugin's release.yml with the Flynn tag as version and flynn_version.
plugin_dispatch_releases() {
  local version=$1
  local blob=$2
  local prerelease=${3:-false}
  local dry_run=${4:-false}
  local repo failed=0
  local gh=${GH:-gh}

  if ! plugin_release_version_ok "${version}"; then
    echo "version must look like vYYYYMMDD.N (got '${version}')" >&2
    return 1
  fi

  local repos
  repos="$(plugin_release_repos_from_text "${blob}")" || return 1
  if [[ -z "${repos}" ]]; then
    echo "PLUGIN_RELEASE_REPOS is empty; skipping plugin release dispatch"
    return 0
  fi

  while IFS= read -r repo || [[ -n "${repo}" ]]; do
    [[ -z "${repo}" ]] && continue
    if [[ "${dry_run}" != "true" ]] && plugin_release_exists "${repo}" "${version}"; then
      echo "skip ${repo}: ${version} already exists"
      continue
    fi
    echo "dispatch ${repo} Build and Release version=${version} flynn_version=${version} prerelease=${prerelease}"
    if [[ "${dry_run}" == "true" ]]; then
      continue
    fi
    if ! "${gh}" workflow run release.yml \
      --repo "${repo}" \
      -f "version=${version}" \
      -f "flynn_version=${version}" \
      -f "draft=false" \
      -f "prerelease=${prerelease}"; then
      echo "failed to dispatch ${repo}" >&2
      failed=1
    fi
  done <<EOF
${repos}
EOF
  return "${failed}"
}
