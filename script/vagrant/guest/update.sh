#!/bin/bash
# Run on the builder as root. Rebuild cluster images when Flynn source is
# newer than the last tarball, then pack build/release. Does not apply the
# tarball to this VM: the live cluster is on dev-nodeN.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"
# shellcheck source=build-needed.sh
source "${FLYNN_VAGRANT_GUEST}/build-needed.sh"
cd "${ROOT}"

if flynn_vagrant_cluster_build_needed; then
  echo "Flynn source changed since the last cluster images; building before update"
  bash "${FLYNN_VAGRANT_GUEST}/build-images.sh"
fi

tarball="$(bash "${FLYNN_VAGRANT_GUEST}/pack-release.sh" | tail -1)"
if [[ -z "${tarball}" ]]; then
  echo "no build/release tarball; run script/vagrant.sh build" >&2
  exit 1
fi
echo "${tarball}"
