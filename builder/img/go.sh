#!/bin/bash

set -eo pipefail

go_version="1.24.12"
go_shasum="bddf8e653c82429aea7aec2520774e79925d4bb929fe20e67ecc00dd5af44c50"
gobin_commit="ef6664e41f0bfe3007869844d318bb2bfa2627f9"
dir="/usr/local"

apt-get update
apt-get install --yes git build-essential libseccomp-dev
apt-get clean

curl --retry 5 --retry-delay 3 -fsSLo /tmp/go.tar.gz "https://go.dev/dl/go${go_version}.linux-amd64.tar.gz"
echo "${go_shasum}  /tmp/go.tar.gz" | shasum -c -
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
