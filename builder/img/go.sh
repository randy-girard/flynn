#!/bin/bash

set -eo pipefail

GO_SH_REPO="$(pwd)"
go_version="1.24.12"
gobin_commit="ef6664e41f0bfe3007869844d318bb2bfa2627f9"
dir="/usr/local"

apt-get update
apt-get install --yes --no-install-recommends git build-essential pkg-config libseccomp-dev

curl --retry 5 --retry-delay 3 -fsSLo /tmp/go.tar.gz "https://go.dev/dl/go${go_version}.linux-$(dpkg --print-architecture).tar.gz"
rm -rf "${dir}/go"
tar xzf /tmp/go.tar.gz -C "${dir}"
rm /tmp/go.tar.gz

export GOROOT="/usr/local/go"
export GOPATH="/go"
export PATH="${GOROOT}/bin:${PATH}"

cp "builder/go-wrapper.sh" "/usr/local/bin/go"
cp "builder/go-wrapper.sh" "/usr/local/bin/cgo"
cp "builder/go-wrapper.sh" "/usr/local/bin/gobin"

# install gobin
git clone https://github.com/flynn/gobin "/tmp/gobin"
trap "rm -rf /tmp/gobin" EXIT
cd "/tmp/gobin"
git reset --hard ${gobin_commit}
/usr/local/bin/go build -o /usr/local/bin/gobin-noenv

# Keep src (needed to compile); drop docs/tests that are never used in image builds.
rm -rf "${dir}/go/test" "${dir}/go/api" "${dir}/go/doc" "${dir}/go/blog" || true

# Image-layer cleanup only. script/flynn-builder also runs this script on the
# builder host to bootstrap Go — do not purge the host's apt/docs. After the
# cd above, resolve the helper from the original cwd (repo /mnt/src).
if [[ -d /mnt/out && -f "${GO_SH_REPO}/builder/img/apt-slim-finish.sh" ]]; then
  # shellcheck source=builder/img/apt-slim-finish.sh
  source "${GO_SH_REPO}/builder/img/apt-slim-finish.sh"
fi
