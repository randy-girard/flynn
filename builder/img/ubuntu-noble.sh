#!/bin/bash

TMP="$(mktemp --directory)"

BSDTAR="${TMP}/bsdtar"
curl -fSL \
  --retry 5 \
  --retry-delay 5 \
  --retry-connrefused \
  --retry-all-errors \
  -o "${BSDTAR}" \
  https://github.com/aspect-build/bsdtar-prebuilt/releases/download/v3.8.1-fix.1/tar_linux_amd64

chmod +x "${BSDTAR}"

URL="https://cloud-images.ubuntu.com/releases/noble/release/ubuntu-24.04-server-cloudimg-amd64-root.tar.xz"
SHA="397fcb08daf4e3a8ac787460294f262d56943be14e3b970ad8de233d206c63ab"
TAR="${TMP}/ubuntu.tar.xz"

echo "Downloading Ubuntu Noble rootfs..."
curl -fSL \
  --retry 5 \
  --retry-delay 10 \
  --retry-connrefused \
  --retry-all-errors \
  -o "${TAR}" \
  "${URL}"

echo "Verifying checksum..."
echo "${SHA}  ${TAR}" | sha256sum -c -

echo "Extracting root filesystem..."

mkdir -p "${TMP}/root"
"${BSDTAR}" -xpf "${TAR}" -C "${TMP}/root" --no-xattrs

rm -f "${TMP}/root/etc/resolv.conf"
cp "/etc/resolv.conf" "${TMP}/root/etc/resolv.conf"
cleanup() {
  >"${TMP}/root/etc/resolv.conf"
}
trap cleanup EXIT

chroot "${TMP}/root" bash -e < "builder/ubuntu-setup.sh"

mksquashfs "${TMP}/root" "/mnt/out/layer.squashfs" -noappend
