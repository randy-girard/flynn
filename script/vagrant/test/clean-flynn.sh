#!/bin/bash
# Regression: vagrant-dev mounts ./build-dev on guest build/. make clean must
# not rm the mountpoint (Device or resource busy).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
# shellcheck source=script/lib/clean-build.sh
source "${ROOT}/script/lib/clean-build.sh"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/flynn-clean-build.XXXXXX")"
trap 'rm -rf "${tmp}"' EXIT

dir="${tmp}/build"
mkdir -p "${dir}/bin"
echo x >"${dir}/bin/flynn"
echo y >"${dir}/.hidden"

if flynn_path_is_mount "${dir}"; then
  echo "temp dir must not be a mountpoint" >&2
  exit 1
fi

flynn_empty_dir "${dir}"
if [[ -e "${dir}/bin" ]] || [[ -e "${dir}/.hidden" ]]; then
  echo "flynn_empty_dir must delete children including hidden files" >&2
  exit 1
fi
if [[ ! -d "${dir}" ]]; then
  echo "flynn_empty_dir must keep the directory (mountpoint)" >&2
  exit 1
fi

mkdir -p "${dir}/bin"
echo z >"${dir}/bin/flynn"
flynn_remove_or_empty_build "${dir}"
if [[ -e "${dir}" ]]; then
  echo "flynn_remove_or_empty_build must rm -rf a non-mount build dir" >&2
  exit 1
fi

if ! grep -q 'flynn_path_is_mount' "${ROOT}/script/clean-flynn"; then
  echo "clean-flynn must skip rm -rf when build/ is a mount" >&2
  exit 1
fi
if ! grep -q -- '-mindepth 1' "${ROOT}/script/clean-flynn"; then
  echo "clean-flynn must empty a mounted build dir" >&2
  exit 1
fi
if grep -qE 'sudo rm -rf "\$\{ROOT\}/build"' "${ROOT}/script/clean-flynn" && ! grep -q 'flynn_path_is_mount' "${ROOT}/script/clean-flynn"; then
  echo "unconditional rm -rf of ROOT/build is the vagrant-dev EBUSY failure" >&2
  exit 1
fi

echo "ok clean-flynn empties a mounted build dir instead of removing the mountpoint"
