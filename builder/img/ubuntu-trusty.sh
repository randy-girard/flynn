#!/bin/bash

TMP="$(mktemp --directory)"

URL="https://partner-images.canonical.com/core/trusty/current/ubuntu-trusty-core-cloudimg-amd64-root.tar.gz"
SHA="e09b2c56f2239f08d97c085da8b81b47361cecf73e22063af20bf5cfbb967bc8"
curl -fSLo "${TMP}/ubuntu.tar.gz" "${URL}"
echo "${SHA}  ${TMP}/ubuntu.tar.gz" #| sha256sum -c -

mkdir -p "${TMP}/root"
tar xf "${TMP}/ubuntu.tar.gz" -C "${TMP}/root"

cp "/etc/resolv.conf" "${TMP}/root/etc/resolv.conf"
chroot "${TMP}/root" bash -e < "builder/ubuntu-setup.sh"

>"${TMP}/root/etc/resolv.conf"

mkdir -p /mnt/out
mksquashfs "${TMP}/root" "/mnt/out/layer.squashfs" -noappend
