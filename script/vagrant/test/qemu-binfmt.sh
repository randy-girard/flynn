#!/bin/bash
# Regression: aarch64 Vagrant nodes must register qemu-user-static binfmt so
# Flynn jobs can exec x86_64 Heroku Node helpers. heroku-24 must install the
# amd64 dynamic linker; qemu itself stays on the VM, not in the slug image.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
mod="${ROOT}/script/vagrant"
qemu="${mod}/guest/ensure-qemu-binfmt.sh"
userland="${ROOT}/builder/img/amd64-qemu-userland.sh"
heroku="${ROOT}/builder/img/heroku-24.sh"
manifest="${ROOT}/builder/manifest.json.template"
smoke="${ROOT}/script/vagrant/suite.sh"
vagrant="${mod}/vagrant.sh"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE -- "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need_file() {
  local path=$1 msg=$2
  if [[ ! -f "${path}" ]]; then
    echo "${msg}" >&2
    echo "  missing ${path}" >&2
    exit 1
  fi
}

need_file "${qemu}" "Vagrant nodes must install qemu-user-static via ensure-qemu-binfmt.sh"
need_file "${userland}" "heroku-24 must have an aarch64 amd64-glibc helper"
if ! bash -n "${qemu}"; then
  echo "${qemu} failed bash -n" >&2
  exit 1
fi
if ! bash -n "${userland}"; then
  echo "${userland} failed bash -n" >&2
  exit 1
fi

need "${qemu}" 'qemu-user-static' \
  "ensure-qemu-binfmt.sh must install qemu-user-static"
need "${qemu}" 'binfmt-support' \
  "ensure-qemu-binfmt.sh must install binfmt-support"
need "${qemu}" 'aarch64' \
  "ensure-qemu-binfmt.sh must no-op except on aarch64"
need "${qemu}" 'QEMU_MIN_MAJOR' \
  "ensure-qemu-binfmt.sh must require qemu-user >= 9 (8.2 SIGSEGVs Node)"
need "${qemu}" 'MAPERR' \
  "ensure-qemu-binfmt.sh must document the Node qemu SIGSEGV"
need "${qemu}" 'echo -1' \
  "ensure-qemu-binfmt.sh must re-register binfmt after upgrading qemu (F-flag fd)"
need "${qemu}" 'qemu-user_10' \
  "ensure-qemu-binfmt.sh must overlay Debian qemu-user >= 9 (qemu-user-static is a stub)"
need "${qemu}" 'qemu-x86_64-static' \
  "ensure-qemu-binfmt.sh must install Ubuntu's qemu-x86_64-static interpreter path"
if grep -q 'already enabled' "${qemu}" && grep -q 'exit 0' "${qemu}"; then
  echo "ensure-qemu-binfmt.sh must not skip when qemu 8.2 is already registered" >&2
  exit 1
fi

need "${mod}/guest/install-node.sh" 'ensure-qemu-binfmt.sh' \
  "install-node.sh must register qemu binfmt on cluster install"
need "${mod}/guest/start-node.sh" 'ensure-qemu-binfmt.sh' \
  "start-node.sh must re-register qemu binfmt after reload"
need "${mod}/guest/update-cluster.sh" 'ensure-qemu-binfmt.sh' \
  "update-cluster.sh must keep qemu binfmt on node1 during flynn-host update"
need "${vagrant}" 'ensure-qemu-binfmt.sh' \
  "vagrant.sh update must run ensure-qemu-binfmt on every cluster node"
need "${smoke}" 'ensure-qemu-binfmt.sh' \
  "smoke install_flynn_on_node must register qemu binfmt on each node"

need "${heroku}" 'amd64-qemu-userland.sh' \
  "heroku-24.sh must source the amd64 userland helper"
need "${manifest}" 'builder/img/amd64-qemu-userland.sh' \
  "heroku-24 layer must declare amd64-qemu-userland.sh as an input"
need "${userland}" 'libc6:amd64' \
  "heroku-24 aarch64 builds must install libc6:amd64"
need "${userland}" 'ld-linux-x86-64.so.2' \
  "heroku-24 aarch64 builds must verify the amd64 dynamic linker"
need "${userland}" 'add-architecture amd64' \
  "heroku-24 aarch64 builds must dpkg --add-architecture amd64"
need "${userland}" 'ubuntu-amd64.sources' \
  "amd64 packages must come from archive.ubuntu.com/kernel.org, not ports"
if grep -qE '^[^#]*qemu-user-static' "${userland}"; then
  echo "amd64-qemu-userland.sh must not install qemu-user-static (jobs cannot register binfmt)" >&2
  exit 1
fi
if ! grep -q 'skipping' "${userland}"; then
  echo "amd64-qemu-userland.sh must skip on non-aarch64 so amd64 CI images stay native" >&2
  exit 1
fi

need "${ROOT}/slugbuilder/builder/build.sh" 'qemu-user is translating' \
  "slugbuilder must print a qemu-mode notice on arm64 git push"
need "${ROOT}/gitreceive/receiver/flynn-receive.go" 'printQemuTranslationNotice' \
  "gitreceive must print a qemu-mode notice on arm64 git push"
need "${ROOT}/slugbuilder/builder/patch-heroku-python-sqlite.sh" 'flynn-python-sqlite3.sh' \
  "slugbuilder must patch the python sqlite3 vendor step on aarch64"
need "${ROOT}/slugbuilder/builder/build.sh" 'patch-heroku-python-sqlite.sh' \
  "slugbuilder compile must re-patch python sqlite3 for custom buildpacks"
need "${ROOT}/slugbuilder/builder/flynn-python-sqlite3.sh" 'sqlite3_copy_if' \
  "python sqlite3 vendor step must copy stack libs, not mv empty globs"
if grep -qE 'mv[[:space:]]+"\$\{extract_dir\}' "${ROOT}/slugbuilder/builder/flynn-python-sqlite3.sh"; then
  echo "flynn-python-sqlite3.sh must not mv empty include/lib globs" >&2
  exit 1
fi
need "${ROOT}/slugbuilder/img/packages.sh" 'patch-heroku-python-sqlite.sh' \
  "slugbuilder image build must run the python sqlite3 aarch64 patch"
need "${ROOT}/builder/img/heroku-24.sh" 'libsqlite3-0' \
  "heroku-24 must ship libsqlite3 so Python stdlib sqlite3 works"
need "${ROOT}/builder/img/heroku-24-build.sh" 'libsqlite3-dev' \
  "heroku-24-build must ship sqlite headers for the python buildpack vendor step"
need "${userland}" 'libsqlite3-0:amd64' \
  "aarch64 heroku-24 must install amd64 libsqlite3 for qemu x86_64 Python"

echo "ok aarch64 qemu-user binfmt + heroku-24 amd64 userland"
