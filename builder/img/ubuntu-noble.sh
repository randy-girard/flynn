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

# Content-addressed cache for the rootfs tarball. The upstream URL lives under
# cloud-images.ubuntu.com/releases/noble/release/, which is a rolling pointer to the
# current point release: the same URL can serve a new tarball with a new checksum at
# any time. The flynn-curl shim is keyed by URL alone and would happily replay a
# stale blob, so route this download around the shim and dedup by the resolved SHA
# instead. Verification below still runs unconditionally.
ROOTFS_CACHE_FILE=""
if [[ -n "${FLYNN_HTTP_CACHE_ROOT:-}" ]] && [[ -d "${FLYNN_HTTP_CACHE_ROOT}" ]]; then
  ROOTFS_CACHE_DIR="${FLYNN_HTTP_CACHE_ROOT}/ubuntu-rootfs"
  mkdir -p "${ROOTFS_CACHE_DIR}"
  ROOTFS_CACHE_FILE="${ROOTFS_CACHE_DIR}/${SHA}.tar.xz"
fi

if [[ -n "${ROOTFS_CACHE_FILE}" ]] && [[ -f "${ROOTFS_CACHE_FILE}" ]]; then
  echo "Using cached Ubuntu Noble rootfs (sha256=${SHA})..."
  cp -p -- "${ROOTFS_CACHE_FILE}" "${TAR}"
else
  echo "Downloading Ubuntu Noble rootfs..."
  FLYNN_NO_HTTP_CACHE=1 curl -fSL \
    --retry 5 \
    --retry-delay 10 \
    --retry-connrefused \
    --retry-all-errors \
    -o "${TAR}" \
    "${URL}"
fi

echo "Verifying checksum..."
echo "${SHA}  ${TAR}" | sha256sum -c -

# Promote a freshly verified rootfs into the content-addressed cache atomically so
# concurrent builds racing on the same SHA see either the old blob or the new one.
if [[ -n "${ROOTFS_CACHE_FILE}" ]] && [[ ! -f "${ROOTFS_CACHE_FILE}" ]]; then
  tmp_cache="${ROOTFS_CACHE_FILE}.$$.tmp"
  cp -p -- "${TAR}" "${tmp_cache}"
  mv -f "${tmp_cache}" "${ROOTFS_CACHE_FILE}"
fi

echo "Extracting root filesystem..."

mkdir -p "${TMP}/root"
"${BSDTAR}" -xpf "${TAR}" -C "${TMP}/root" --no-xattrs

rm -f "${TMP}/root/etc/resolv.conf"
cp "/etc/resolv.conf" "${TMP}/root/etc/resolv.conf"

chroot_archive_bind=
chroot_lists_bind=
cleanup() {
  if [[ "${chroot_archive_bind}" == 1 ]] && [[ -n "${TMP:-}" ]]; then
    umount "${TMP}/root/var/cache/apt/archives" 2>/dev/null || true
  fi
  if [[ "${chroot_lists_bind}" == 1 ]] && [[ -n "${TMP:-}" ]]; then
    umount "${TMP}/root/var/lib/apt/lists" 2>/dev/null || true
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

# Same for the apt lists bind mount: lets apt-get update do conditional/incremental
# refreshes against the shared host index cache instead of redownloading every build.
if mountpoint -q /var/lib/apt/lists; then
  mkdir -p "${TMP}/root/var/lib/apt/lists"
  mount --bind /var/lib/apt/lists "${TMP}/root/var/lib/apt/lists"
  chroot_lists_bind=1
fi

chroot "${TMP}/root" bash -e < "builder/ubuntu-setup.sh"

if [[ "${chroot_archive_bind}" == 1 ]]; then
  umount "${TMP}/root/var/cache/apt/archives" || exit 1
  chroot_archive_bind=
fi
if [[ "${chroot_lists_bind}" == 1 ]]; then
  umount "${TMP}/root/var/lib/apt/lists" || exit 1
  chroot_lists_bind=
fi

# Drop any unpacked .deb or lingering apt-list files in the staging rootfs (normally empty
# once the bind mounts are detached). Never rm while the binds are mounted: that would purge
# the shared host cache directories.
rm -rf "${TMP}/root/var/cache/apt/archives"/* "${TMP}/root/var/cache/apt/archives"/partial/* 2>/dev/null || true
rm -rf "${TMP}/root/var/lib/apt/lists"/* 2>/dev/null || true

mksquashfs "${TMP}/root" "/mnt/out/layer.squashfs" -noappend
