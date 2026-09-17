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
