# Build cache helpers, sourced by build.sh (BUILD-07).
#
# BUILD_CACHE_URL and BUILD_CACHE_PUT_URL are expiring bearer URLs for this
# app's cache object: the first can only read it, the second can only write it.
# Buildpack code runs as the unprivileged user in build.sh's environment, so
# root does the GET before any buildpack starts and does the PUT after the last
# one finishes. The URLs are unset from the environment as soon as they are
# read, and the write URL is kept only in a shell variable that is never
# exported.

_build_cache_put_url=""

# build_cache_restore DIR extracts the cached tarball into DIR, then removes
# both cache URLs from the environment.
build_cache_restore() {
  local dir="$1"
  local get_url="${BUILD_CACHE_URL}"
  _build_cache_put_url="${BUILD_CACHE_PUT_URL}"
  unset BUILD_CACHE_URL BUILD_CACHE_PUT_URL
  if [[ -n "${get_url}" ]]; then
    curl --location "${get_url}" | tar --extract --gunzip --directory "${dir}" &>/dev/null || true
  fi
}

# build_cache_save DIR uploads DIR as the cache tarball when a write URL was
# provided.
build_cache_save() {
  local dir="$1"
  if [[ -z "${_build_cache_put_url}" ]]; then
    return 0
  fi
  tar \
    --create \
    --directory "${dir}" \
    --use-compress-program="${FLYNN_CACHE_COMPRESS:-pigz}" \
    . \
  | curl \
    --output "$(mktemp)" \
    --request PUT \
    --upload-file - \
    "${_build_cache_put_url}"
}
