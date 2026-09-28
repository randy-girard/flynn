#!/bin/bash
# Run on the builder as root. Build cluster images (and a release tarball)
# without requiring a bootstrapped cluster. If /etc/flynn/host.json exists,
# keep that cluster; otherwise start-all / stop-all around flynn-builder.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"
cd "${ROOT}"
export FLYNN_ROOT="${ROOT}"
if [[ -f /etc/flynn/host.json ]]; then
  export FLYNN_KEEP_CLUSTER=1
fi
if [[ -f /var/lib/flynn/base-layer.squashfs ]]; then
  ./build.sh cluster
else
  ./build.sh
fi
bash "${FLYNN_VAGRANT_GUEST}/pack-release.sh"
