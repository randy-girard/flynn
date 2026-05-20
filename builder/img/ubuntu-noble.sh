#!/bin/bash

TMP="$(mktemp --directory)"

BSDTAR="${TMP}/bsdtar"
curl -fSL \
  --retry 5 \
  --retry-delay 5 \
  --retry-connrefused \
  --retry-all-errors \
  -o "${BSDTAR}" \
  https://github.com/aspect-build/bsdtar-prebuilt/releases/download/v3.8.1-fix.1/tar_linux_$(dpkg --print-architecture)

chmod +x "${BSDTAR}"

BASE_URL="https://cloud-images.ubuntu.com/releases/noble/release"
FILENAME="ubuntu-24.04-server-cloudimg-$(dpkg --print-architecture)-root.tar.xz"
URL="${BASE_URL}/${FILENAME}"
TAR="${TMP}/ubuntu.tar.xz"
SHASUMS="${TMP}/SHA256SUMS"

echo "Downloading SHA256SUMS..."
curl -fSL \
  --retry 5 \
  --retry-delay 5 \
  --retry-connrefused \
  --retry-all-errors \
  -o "${SHASUMS}" \
  "${BASE_URL}/SHA256SUMS"

SHA=$(grep "${FILENAME}" "${SHASUMS}" | awk '{print $1}')
if [ -z "${SHA}" ]; then
  echo "Error: Could not find checksum for ${FILENAME} in SHA256SUMS"
  exit 1
fi
echo "Found checksum: ${SHA}"

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

chroot_archive_bind=
cleanup() {
  if [[ "${chroot_archive_bind}" == 1 ]] && [[ -n "${TMP:-}" ]]; then
    umount "${TMP}/root/var/cache/apt/archives" 2>/dev/null || true
  fi
  if [[ -n "${TMP:-}" ]] && [[ -d "${TMP}/root" ]]; then
    >"${TMP}/root/etc/resolv.conf"
  fi
}
trap cleanup EXIT

# When flynn-builder bind-mounts a host APT cache at /var/cache/apt/archives, propagate
# that mount into this image's temporary chroot (CAP_SYS_ADMIN is set for this layer).
if mountpoint -q /var/cache/apt/archives; then
  mkdir -p "${TMP}/root/var/cache/apt/archives"
  mount --bind /var/cache/apt/archives "${TMP}/root/var/cache/apt/archives"
  chroot_archive_bind=1
fi

chroot "${TMP}/root" bash -e < "builder/ubuntu-setup.sh"

if [[ "${chroot_archive_bind}" == 1 ]]; then
  umount "${TMP}/root/var/cache/apt/archives" || exit 1
  chroot_archive_bind=
fi

# Drop any unpacked .deb left in the staging rootfs (normally empty once the APT cache bind is detached).
# Never rm while the bind is mounted: that would purge the shared host cache directory.
rm -rf "${TMP}/root/var/cache/apt/archives"/* "${TMP}/root/var/cache/apt/archives"/partial/* 2>/dev/null || true

mksquashfs "${TMP}/root" "/mnt/out/layer.squashfs" -noappend
