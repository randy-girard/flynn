# Release helpers

# next_release_version takes the previous release version
# and returns the next one (vYYYYMMDD.N, UTC date via `date`).
next_release_version() {
  local previous=$1
  local date
  date=$(date +%Y%m%d)
  local iteration
  if [[ $previous =~ $date ]]; then
    previous_iteration="${previous##*.}"
    iteration=$((previous_iteration + 1))
  else
    iteration=0
  fi
  echo "v${date}.${iteration}"
}

# next_release_version_from_tags inspects local git tags for today and returns
# the next unused vYYYYMMDD.N (vYYYYMMDD.0 when none exist). Ignores suffixes
# like vYYYYMMDD.0-smoke so they cannot poison the integer increment.
next_release_version_from_tags() {
  local date
  date=$(date +%Y%m%d)
  local latest=""
  latest=$(git tag -l "v${date}.*" 2>/dev/null | { grep -E "^v[0-9]{8}\\.[0-9]+$" || true; } | sort -V | tail -n1)
  next_release_version "${latest}"
}

# flynn_github_https_url prints the public https URL for a GitHub remote.
# Non-GitHub URLs fail so callers do not invent a fallback.
flynn_github_https_url() {
  local url="$1"
  local repo
  case "${url}" in
    https://github.com/*)
      printf '%s\n' "${url}"
      return 0
      ;;
    git@github.com:*)
      repo="${url#git@github.com:}"
      ;;
    ssh://git@github.com/*)
      repo="${url#ssh://git@github.com/}"
      ;;
    *)
      return 1
      ;;
  esac
  repo="${repo%/}"
  repo="${repo%.git}"
  if [[ -z "${repo}" ]]; then
    return 1
  fi
  printf 'https://github.com/%s.git\n' "${repo}"
}

# flynn_fetch_release_tags updates local tags from origin.
# A github.com SSH remote gets a host key without prompting, because the
# builder usually has no GitHub key. If that fetch fails, GitHub remotes
# retry over https. Total failure prints a warning and the underlying error.
flynn_fetch_release_tags() {
  local origin_url https_url err_file origin_err https_err
  local git_ssh="ssh -o BatchMode=yes"
  err_file="$(mktemp)"
  if ! origin_url="$(git remote get-url origin 2>"${err_file}")"; then
    echo "Warning: Could not fetch tags from remote" >&2
    cat "${err_file}" >&2 || true
    rm -f "${err_file}"
    return 1
  fi

  case "${origin_url}" in
    git@github.com:*|ssh://git@github.com/*)
      if ! ssh-keygen -F github.com >/dev/null 2>&1; then
        if mkdir -p "${HOME}/.ssh"; then
          chmod 700 "${HOME}/.ssh" || true
          ssh-keyscan -t ed25519 github.com >>"${HOME}/.ssh/known_hosts" || true
        fi
      fi
      ;;
  esac

  if GIT_SSH_COMMAND="${git_ssh}" git fetch --tags --force origin 2>"${err_file}"; then
    rm -f "${err_file}"
    return 0
  fi
  origin_err="$(cat "${err_file}")"

  if https_url="$(flynn_github_https_url "${origin_url}")"; then
    if GIT_SSH_COMMAND="${git_ssh}" git fetch --tags --force "${https_url}" '+refs/tags/*:refs/tags/*' 2>"${err_file}"; then
      rm -f "${err_file}"
      return 0
    fi
    https_err="$(cat "${err_file}")"
    rm -f "${err_file}"
    echo "Warning: Could not fetch tags from remote" >&2
    if [[ -n "${origin_err}" ]]; then
      printf '%s\n' "${origin_err}" >&2
    fi
    if [[ -n "${https_err}" ]]; then
      printf '%s\n' "${https_err}" >&2
    fi
    return 1
  fi

  rm -f "${err_file}"
  echo "Warning: Could not fetch tags from remote" >&2
  if [[ -n "${origin_err}" ]]; then
    printf '%s\n' "${origin_err}" >&2
  fi
  return 1
}
