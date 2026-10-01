#!/bin/bash
# Persist FLYNN_ROOT on this VM so flynn-host plugin:install / plugin:update
# compile against the mounted Flynn checkout (DISCOVERD_AUTH_KEY / SEC-003).
# flynn-host on cluster nodes is /usr/bin, not inside the source tree, so
# walking up from the binary never finds Flynn. Interactive shells, PAM,
# and /etc/flynn/source-root all need the same path.
set -euo pipefail
# shellcheck source=../lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/common.sh"

if [[ ! -f "${SRC}/go.mod" ]]; then
  echo "ensure-flynn-root: no Flynn checkout at ${SRC}" >&2
  exit 0
fi

mkdir -p /etc/profile.d /etc/flynn
cat > /etc/profile.d/flynn-root.sh <<EOF
export FLYNN_ROOT=${SRC}
EOF
chmod 644 /etc/profile.d/flynn-root.sh

if [[ -f /etc/environment ]] && grep -q '^FLYNN_ROOT=' /etc/environment; then
  sed -i "s|^FLYNN_ROOT=.*|FLYNN_ROOT=${SRC}|" /etc/environment
else
  echo "FLYNN_ROOT=${SRC}" >> /etc/environment
fi

printf '%s\n' "${SRC}" > /etc/flynn/source-root
chmod 644 /etc/flynn/source-root

export FLYNN_ROOT="${SRC}"
echo "FLYNN_ROOT=${SRC}"
