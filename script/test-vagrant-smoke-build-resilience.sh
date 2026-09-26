#!/bin/bash
# Regression: builder apt/network blips (download.docker.com InRelease) must not
# fail the smoke build step on the first try.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"

need() {
  local needle=$1 msg=$2
  if ! grep -qE "${needle}" "${smoke}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${smoke}" >&2
    exit 1
  fi
}

need 'FLYNN_BUILD_ATTEMPTS' \
  "smoke must retry the builder on transient apt/network failures"
need 'transient_build_failure' \
  "build retries must be gated on apt/network errors, not compile failures"
need 'download.docker.com' \
  "transient detector must include docker.com mirror blips"
need 'build/flynn-build-attempt.log' \
  "build attempt log must live in the synced repo (not /tmp/flynn-* which prep deletes)"
need 'build attempt' \
  "builder must log each build attempt"
need 'step_fail_summary' \
  "fail table must prefer apt E: lines / FAIL / DATA RACE over Get:1 docker.com or coverage noise"
need 'WARNING: DATA RACE' \
  "fail summary must surface go test -race reports hidden by trailing coverage lines"
need 'invalid runtime symbol table' \
  "builder unit tests must drop host-mounted Docker discoverd binaries that crash on the VM"

need_file() {
  local path=$1 msg=$2
  if [[ ! -f "${path}" ]]; then
    echo "${msg}" >&2
    exit 1
  fi
}
need_file "${ROOT}/builder/img/flynn-apt-get.sh" "image builds need an apt-get retry shim"
need_file "${ROOT}/script/lib/apt-retry.sh" "host apt-get update must retry and drop docker.com if needed"
grep -q 'flynn-apt-get' "${ROOT}/builder/build.go" \
  || { echo "flynn-builder must install the apt-get retry shim" >&2; exit 1; }
grep -q 'docker.sources' "${ROOT}/builder/build.go" \
  || { echo "image apt prelude must strip leaked docker.sources" >&2; exit 1; }
grep -q 'flynn_chroot_apt' "${ROOT}/builder/ubuntu-setup.sh" \
  || { echo "ubuntu-setup chroot must retry apt-get (PATH shim does not apply)" >&2; exit 1; }
grep -q 'apt_get_update_resilient' "${ROOT}/script/install-flynn" \
  || { echo "install-flynn must retry apt-get update" >&2; exit 1; }
grep -q 'timeout --signal=TERM --kill-after=10s 45' "${ROOT}/script/install-flynn.tmpl" \
  || { echo "install-flynn cleanup must time out a stuck destroy-volumes" >&2; exit 1; }
grep -q 'zpool destroy flynn-default did not finish' "${ROOT}/script/install-flynn.tmpl" \
  || { echo "install-flynn cleanup must continue when zpool destroy times out" >&2; exit 1; }
grep -q 'unmount_under_flynn' "${ROOT}/script/install-flynn" \
  || { echo "install --clean must unmount overlay/squashfs under /var/lib/flynn before rm -rf" >&2; exit 1; }
grep -q 'flynn_apt_update_host' "${ROOT}/setup.sh" \
  || { echo "setup.sh must retry host apt-get update" >&2; exit 1; }

echo "ok builder apt/network resilience (retry + docker.com isolation)"
