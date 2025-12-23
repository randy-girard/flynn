#!/bin/bash

TMP="$(mktemp --directory)"

URL="https://partner-images.canonical.com/core/bionic/current/ubuntu-bionic-core-cloudimg-amd64-root.tar.gz"
SHA="d44096a712fee7dc69ea0ad67d90e1364622488cdd58b1407fac135dc942a8d2"
curl -fSLo "${TMP}/ubuntu.tar.gz" "${URL}"
echo "${SHA}  ${TMP}/ubuntu.tar.gz" #| sha256sum -c -

mkdir -p "${TMP}/root"
tar xf "${TMP}/ubuntu.tar.gz" -C "${TMP}/root"

cp "/etc/resolv.conf" "${TMP}/root/etc/resolv.conf"
cleanup() {
  >"${TMP}/root/etc/resolv.conf"
}
trap cleanup EXIT

chroot "${TMP}/root" bash -e < "builder/ubuntu-setup.sh"

mksquashfs "${TMP}/root" "/mnt/out/layer.squashfs" -noappend
