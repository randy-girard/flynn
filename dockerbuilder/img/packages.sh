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

# Do not rm -rf /tmp: image jobs often share the host tmpdir; wiping it
# breaks later apt-key (GetTempFile permission denied).
# shellcheck source=builder/img/apt-slim-finish.sh
source builder/img/apt-slim-finish.sh
