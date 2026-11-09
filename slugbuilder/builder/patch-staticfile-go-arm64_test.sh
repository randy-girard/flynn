#!/bin/bash
# The staticfile buildpack downloads Go 1.22.5 linux_x64, then that toolchain
# fetches Go 1.23.4 linux/amd64. qemu-user SIGSEGVs in that runtime's epoll
# poller, so an arm64 slugbuilder must replace the script with the official
# linux-arm64 toolchain before compile.
set -euo pipefail

root="$(cd "$(dirname "$0")" && pwd)"
patch="${root}/patch-staticfile-go-arm64.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

bp="${tmp}/000002_staticfile-buildpack"
mkdir -p "${bp}/scripts"
cat > "${bp}/scripts/install_go.sh" << 'EOF'
url="https://buildpacks.cloudfoundry.org/dependencies/go/go_1.22.5_linux_x64_cflinuxfs3_ddb12ede.tgz"
EOF

bash "${patch}" "${tmp}"
grep -q 'flynn_staticfile_go_arm64' "${bp}/scripts/install_go.sh"
grep -q 'linux-arm64' "${bp}/scripts/install_go.sh"
grep -q '16e5017863a7f6071363782b1b8042eb12c6ca4f4cd71528b2123f0a1275b13e' "${bp}/scripts/install_go.sh"
# A second run leaves the replacement in place.
bash "${patch}" "${tmp}"
grep -q 'flynn_staticfile_go_arm64' "${bp}/scripts/install_go.sh"
