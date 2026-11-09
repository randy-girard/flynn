#!/bin/bash
# Flynn replacement for cloudfoundry/staticfile-buildpack scripts/install_go.sh.
# flynn_staticfile_go_arm64
#
# Upstream downloads Go 1.22.5 linux_x64. go.mod says 1.23.4, so that toolchain
# then fetches another linux/amd64 Go. On an arm64 host qemu-user faults in
# that runtime's epoll poller (runtime.netpoll). Install the official
# linux-arm64 Go 1.23.4 toolchain there and keep the local toolchain.
# amd64 keeps the upstream Cloud Foundry archive so the native toolchain
# download still runs.

set -euo pipefail

flynn_staticfile_go_arm64=1

main() {
  local version expected_sha url dir strip
  case "$(uname -m)" in
    aarch64|arm64)
      version="1.23.4"
      expected_sha="16e5017863a7f6071363782b1b8042eb12c6ca4f4cd71528b2123f0a1275b13e"
      url="https://go.dev/dl/go${version}.linux-arm64.tar.gz"
      strip=1
      ;;
    *)
      version="1.22.5"
      expected_sha="ddb12ede43eef214c7d4376761bd5ba6297d5fa7a06d5635ea3e7a276b3db730"
      url="https://buildpacks.cloudfoundry.org/dependencies/go/go_${version}_linux_x64_cflinuxfs3_${expected_sha:0:8}.tgz"
      strip=0
      ;;
  esac

  dir="/tmp/go${version}"
  mkdir -p "${dir}"

  if [[ ! -x "${dir}/bin/go" ]]; then
    echo "-----> Download go ${version}"
    curl "${url}" \
      --silent \
      --location \
      --retry 15 \
      --retry-delay 2 \
      --output "/tmp/go.tgz"

    local sha
    sha="$(shasum -a 256 /tmp/go.tgz | cut -d ' ' -f 1)"
    if [[ "${sha}" != "${expected_sha}" ]]; then
      echo " **ERROR** SHA256 mismatch: got ${sha}, expected ${expected_sha}"
      exit 1
    fi

    if [[ "${strip}" -eq 1 ]]; then
      tar xzf "/tmp/go.tgz" -C "${dir}" --strip-components=1
    else
      tar xzf "/tmp/go.tgz" -C "${dir}"
    fi
    rm -f "/tmp/go.tgz"
  fi

  if [[ ! -x "${dir}/bin/go" ]]; then
    echo " **ERROR** Could not download go"
    exit 1
  fi

  GoInstallDir="${dir}"
  export GoInstallDir
  if [[ "${strip}" -eq 1 ]]; then
    export GOTOOLCHAIN=local
  fi
}

main "${@:-}"
