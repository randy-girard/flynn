#!/bin/bash

TMP="$(mktemp --directory)"

URL="https://partner-images.canonical.com/core/xenial/current/ubuntu-xenial-core-cloudimg-amd64-root.tar.gz"
SHA="cc6f79ef87645ceaab78aa007084a34b20526e95f815cb50612930297e766d62"
curl -fSLo "${TMP}/ubuntu.tar.gz" "${URL}"
echo "${SHA}  ${TMP}/ubuntu.tar.gz" | sha256sum -c -

mkdir -p "${TMP}/root"
tar xf "${TMP}/ubuntu.tar.gz" -C "${TMP}/root"

cp "/etc/resolv.conf" "${TMP}/root/etc/resolv.conf"
mount --bind "/dev/pts" "${TMP}/root/dev/pts"
cleanup() {
  umount "${TMP}/root/dev/pts"
  >"${TMP}/root/etc/resolv.conf"
}
trap cleanup EXIT

chroot "${TMP}/root" bash -e < "builder/ubuntu-setup.sh"

mkdir -p /mnt/out
mksquashfs "${TMP}/root" "/mnt/out/layer.squashfs" -noappend
