#!/bin/bash
# Run on the builder as root. Apply the newest release tarball with flynn-host.
# The builder PATH often has no flynn-host; the binary is in build/bin.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${ROOT}"

tarball="$(ls -t build/release/flynn-*.tar.gz 2>/dev/null | head -1 || true)"
images=""
if [[ -f build/manifests/images.json ]]; then
  images="build/manifests/images.json"
elif [[ -f build/images.json ]]; then
  images="build/images.json"
fi
if [[ -n "${images}" ]]; then
  newer=0
  if [[ -z "${tarball}" || "${images}" -nt "${tarball}" ]]; then
    newer=1
  fi
  if [[ "${newer}" -eq 1 ]]; then
    version=""
    if [[ -x build/bin/flynn-host ]]; then
      version="$(build/bin/flynn-host version 2>/dev/null || true)"
    fi
    if [[ ! "${version}" =~ ^v[0-9]{8}\.[0-9]+$ ]]; then
      # shellcheck source=lib/release.sh
      source "${ROOT}/script/lib/release.sh"
      version="$(next_release_version_from_tags)"
    fi
    echo "packaging ${images} as ${version}"
    ./script/release --target tarball --version "${version}" --output "${ROOT}/build/release"
    tarball="$(ls -t build/release/flynn-*.tar.gz | head -1)"
  fi
fi
if [[ -z "${tarball}" ]]; then
  echo "no build/release tarball; run script/vagrant-dev.sh build first" >&2
  exit 1
fi

host_bin=""
for candidate in "${ROOT}/build/bin/flynn-host" /usr/local/libexec/flynn-host; do
  if [[ -x "${candidate}" ]] && ! head -1 "${candidate}" | grep -q '^#!'; then
    host_bin="${candidate}"
    break
  fi
done
if [[ -z "${host_bin}" ]]; then
  echo "flynn-host was not found in build/bin or /usr/local/libexec" >&2
  exit 1
fi

echo "updating from ${tarball} using ${host_bin}"
install -m 0755 "${host_bin}" /usr/local/libexec/flynn-host
export FLYNN_ROOT="${ROOT}"
export PATH="/usr/local/bin:${ROOT}/build/bin:${PATH}"
# Install the tarball into build/bin so start-flynn-host (vagrant-dev) execs
# the new binary. Default --bin-dir is /usr/local/bin and would leave the
# start-stop-daemon --exec path on the previous inode.
flynn-host update --tarball "${ROOT}/${tarball}" --force --bin-dir="${ROOT}/build/bin"
bash "${ROOT}/script/vagrant-dev-guest-cli.sh"
if ! curl -s --max-time 2 -o /dev/null "http://192.0.2.200:1111/services"; then
  echo "discoverd is not on 192.0.2.200:1111; starting the existing cluster if it was bootstrapped"
  if ! bash "${ROOT}/script/vagrant-dev-ensure-cluster.sh"; then
    echo "cluster did not come back. vagrant-dev.sh build used to tear it down; run script/vagrant-dev.sh bootstrap" >&2
    exit 1
  fi
fi
