#!/usr/bin/env bash

set -euxo pipefail

export DEBIAN_FRONTEND=noninteractive

packages=(
  ca-certificates
  curl
)

apt-get update --error-on=any
apt-get install -y --no-install-recommends "${packages[@]}"

BUILDKIT_VERSION=v0.23.2
case "$(uname -m)" in
  x86_64|amd64) BUILDKIT_ARCH=amd64 ;;
  aarch64|arm64) BUILDKIT_ARCH=arm64 ;;
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
tar -xzf "${buildkit_tgz}" -C /usr/local/buildkit
rm -f "${buildkit_tgz}"
trap - EXIT

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
install -m 0755 "${script_dir}/buildctl-daemonless.sh" /usr/local/buildkit/bin/buildctl-daemonless.sh
ln -sf /usr/local/buildkit/bin/buildctl-daemonless.sh /usr/local/bin/buildctl-daemonless.sh
ln -sf /usr/local/buildkit/bin/buildctl /usr/local/bin/buildctl
ln -sf /usr/local/buildkit/bin/buildkitd /usr/local/bin/buildkitd

rm -rf /root/*
rm -rf /tmp/*
if ! mountpoint -q /var/cache/apt/archives 2>/dev/null; then
  rm -rf /var/cache/apt/archives/* "/var/cache/apt/archives/partial"/*
fi
if ! mountpoint -q /var/lib/apt/lists 2>/dev/null; then
  rm -rf /var/lib/apt/lists/*
fi
