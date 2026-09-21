#!/usr/bin/env bash

set -euxo pipefail

export DEBIAN_FRONTEND=noninteractive

packages=(
  ca-certificates
  curl
  gzip
  pigz
  # BuildKit's OCI worker shells out to runc. heroku-24-build used to provide
  # it; ubuntu-noble does not.
  runc
)

apt-get update --error-on=any
apt-get install -y --no-install-recommends "${packages[@]}"

# GitHub Releases digest for buildkit-v0.23.2.linux-*.tar.gz
# (api.github.com/repos/moby/buildkit/releases/tags/v0.23.2).
BUILDKIT_VERSION=v0.23.2
BUILDKIT_SHA256_AMD64=2771c3403e3a1f75a83cde387a05365794d3b900c355e864772a36c3ce541f82
BUILDKIT_SHA256_ARM64=6385ff70b2fb4134b50ac3183eea3a0b06c6f6129173940d73178ae0477368f1
case "$(uname -m)" in
  x86_64|amd64)
    BUILDKIT_ARCH=amd64
    BUILDKIT_SHA256="${BUILDKIT_SHA256_AMD64}"
    ;;
  aarch64|arm64)
    BUILDKIT_ARCH=arm64
    BUILDKIT_SHA256="${BUILDKIT_SHA256_ARM64}"
    ;;
  *)
    echo "unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

mkdir -p /usr/local/buildkit
buildkit_tgz="$(mktemp)"
trap 'rm -f "${buildkit_tgz}"' EXIT
curl -fsSL "https://github.com/moby/buildkit/releases/download/${BUILDKIT_VERSION}/buildkit-${BUILDKIT_VERSION}.linux-${BUILDKIT_ARCH}.tar.gz" \
  -o "${buildkit_tgz}"
echo "${BUILDKIT_SHA256}  ${buildkit_tgz}" | sha256sum -c -
tar -xzf "${buildkit_tgz}" -C /usr/local/buildkit
rm -f "${buildkit_tgz}"
trap - EXIT

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
install -m 0755 "${script_dir}/buildctl-daemonless.sh" /usr/local/buildkit/bin/buildctl-daemonless.sh
ln -sf /usr/local/buildkit/bin/buildctl-daemonless.sh /usr/local/bin/buildctl-daemonless.sh
ln -sf /usr/local/buildkit/bin/buildctl /usr/local/bin/buildctl
ln -sf /usr/local/buildkit/bin/buildkitd /usr/local/bin/buildkitd

# Do not rm -rf /tmp: image jobs often share the host tmpdir; wiping it
# breaks later apt-key (GetTempFile permission denied).
# shellcheck source=builder/img/apt-slim-finish.sh
source builder/img/apt-slim-finish.sh
