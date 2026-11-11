#!/bin/bash
# GitHub-hosted Ubuntu runners list azure.archive.ubuntu.com in
# /etc/apt/apt-mirrors.txt. When that name does not resolve, apt-get spends
# its retries there and the job ends before any test runs. archive.ubuntu.com
# is the public mirror those runners can still reach.

flynn_ci_use_public_ubuntu_mirror() {
  local f tmp
  for f in "$@"; do
    [[ -f "${f}" ]] || continue
    tmp="$(mktemp)"
    sed 's/azure\.archive\.ubuntu\.com/archive.ubuntu.com/g' "${f}" > "${tmp}"
    cat "${tmp}" > "${f}"
    rm -f "${tmp}"
  done
}
