#!/bin/bash
# Run on the builder as root. Build cluster images (and a release tarball).
# The builder starts Flynn only so flynn-builder can compile; it is not the
# live laptop cluster. Do not set FLYNN_KEEP_CLUSTER — that preserved a
# nested operator cluster on this VM. Cluster nodes run the operator cluster.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"
cd "${ROOT}"
export FLYNN_ROOT="${ROOT}"
if [[ -f /var/lib/flynn/base-layer.squashfs ]]; then
  ./build.sh cluster
else
  ./build.sh
fi
bash "${FLYNN_VAGRANT_GUEST}/pack-release.sh"
# shellcheck source=build-needed.sh
source "${FLYNN_VAGRANT_GUEST}/build-needed.sh"
flynn_vagrant_write_build_stamp
