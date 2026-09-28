# Shared helpers for script/clean-flynn. Sourced; not executed.
#
# vagrant-dev overlays host ./build-dev onto guest build/ as a synced_folder.
# rm -rf of that path is rm of a mountpoint and fails with
# "Device or resource busy". Empty the contents instead.

flynn_path_is_mount() {
  local dir=$1
  [[ -d "${dir}" ]] || return 1
  if command -v mountpoint >/dev/null 2>&1; then
    mountpoint -q "${dir}"
    return
  fi
  [[ -r /proc/self/mountinfo ]] || return 1
  awk -v d="${dir}" '$5 == d { found=1 } END { exit !found }' /proc/self/mountinfo
}

# Delete children of dir, not dir itself (safe for a mountpoint).
flynn_empty_dir() {
  local dir=$1
  [[ -d "${dir}" ]] || return 0
  find "${dir}" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
}

# Remove a normal build directory, or empty it when it is a mount.
flynn_remove_or_empty_build() {
  local dir=$1
  if [[ ! -e "${dir}" ]]; then
    return 0
  fi
  if flynn_path_is_mount "${dir}"; then
    flynn_empty_dir "${dir}"
    return 0
  fi
  rm -rf "${dir}"
}
