#!/bin/bash
# Source this after lib/common.sh. Decides whether vagrant update must run
# flynn-builder before flynn-host update.
#
# Stamp is HEAD of build-relevant paths plus a digest of filtered dirty files.
# Docs, markdown, vagrant tests, and build outputs do not force a rebuild.

flynn_vagrant_build_stamp_file() {
  echo "${ROOT}/build/.vagrant-build-stamp"
}

# Paths that do not change cluster images / flynn-host binaries.
flynn_vagrant_build_excludes=(
  ':!docs'
  ':!demo'
  ':!.plans'
  ':!script/vagrant/test'
  ':!*.md'
  ':!flynn-logs'
  ':!ubuntu_ports_cache'
  ':!build'
  ':!build-dev'
)

flynn_vagrant_file_mtime() {
  stat -c %Y "$1" 2>/dev/null || stat -f %m "$1"
}

flynn_vagrant_newest_tarball() {
  ls -t "${ROOT}"/build/release/flynn-*.tar.gz 2>/dev/null | head -1 || true
}

flynn_vagrant_images_json() {
  if [[ -f "${ROOT}/build/manifests/images.json" ]]; then
    echo "${ROOT}/build/manifests/images.json"
  elif [[ -f "${ROOT}/build/images.json" ]]; then
    echo "${ROOT}/build/images.json"
  fi
}

flynn_vagrant_digest() {
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s' "$1" | sha256sum | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    printf '%s' "$1" | shasum -a 256 | awk '{print $1}'
  else
    printf '%s' "$1" | openssl dgst -sha256 | awk '{print $NF}'
  fi
}

flynn_vagrant_build_stamp() {
  local rev dirty digest
  rev="$(git -C "${ROOT}" log -1 --format=%H -- . "${flynn_vagrant_build_excludes[@]}" 2>/dev/null || true)"
  if [[ -z "${rev}" ]]; then
    rev="$(git -C "${ROOT}" rev-parse HEAD 2>/dev/null || echo none)"
  fi
  dirty="$(git -C "${ROOT}" status --porcelain --untracked-files=all -- . "${flynn_vagrant_build_excludes[@]}" 2>/dev/null || true)"
  digest="$(flynn_vagrant_digest "${dirty}")"
  printf '%s\n%s\n' "${rev}" "${digest}"
}

flynn_vagrant_write_build_stamp() {
  mkdir -p "${ROOT}/build"
  flynn_vagrant_build_stamp > "$(flynn_vagrant_build_stamp_file)"
}

# Return 0 when cluster images / host binaries must be rebuilt.
flynn_vagrant_cluster_build_needed() {
  if [[ "${FLYNN_VAGRANT_FORCE_BUILD:-}" == "1" ]]; then
    return 0
  fi
  local tarball images stamp_file current saved tm ct dirty
  tarball="$(flynn_vagrant_newest_tarball)"
  images="$(flynn_vagrant_images_json)"
  if [[ -z "${tarball}" && -z "${images}" ]]; then
    return 0
  fi
  current="$(flynn_vagrant_build_stamp)"
  stamp_file="$(flynn_vagrant_build_stamp_file)"
  if [[ -f "${stamp_file}" ]]; then
    saved="$(cat "${stamp_file}")"
    [[ "${current}" != "${saved}" ]]
    return
  fi
  dirty="$(git -C "${ROOT}" status --porcelain --untracked-files=all -- . "${flynn_vagrant_build_excludes[@]}" 2>/dev/null || true)"
  if [[ -n "${dirty}" ]]; then
    return 0
  fi
  if [[ -z "${tarball}" ]]; then
    return 0
  fi
  tm="$(flynn_vagrant_file_mtime "${tarball}")"
  ct="$(git -C "${ROOT}" log -1 --format=%ct -- . "${flynn_vagrant_build_excludes[@]}" 2>/dev/null || echo 0)"
  if [[ "${ct:-0}" -gt "${tm:-0}" ]]; then
    return 0
  fi
  flynn_vagrant_write_build_stamp
  return 1
}
