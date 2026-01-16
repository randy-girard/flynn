#!/bin/bash

TMP="$(mktemp --directory)"

apt install busybox-static

mkdir "${TMP}/root"
cd "${TMP}/root"

# Basic filesystem layout
# Use /usr/bin as the real directory and /bin as a symlink to match Ubuntu Noble layout.
# This ensures binaries placed in /bin (via symlink to /usr/bin) during builds with
# Ubuntu Noble are accessible at /bin in the final image.
mkdir -p usr/bin etc dev dev/pts lib proc sys tmp builder
ln -s usr/bin bin
ln -s usr/builder builder

# Minimal config
touch etc/resolv.conf
cp /etc/nsswitch.conf etc/nsswitch.conf

echo root:x:0:0:root:/:/bin/sh > etc/passwd
echo root:x:0: > etc/group

# Compatibility symlinks
ln -s lib lib64
ln -s bin sbin

# BusyBox - install to /usr/bin (accessible via /bin symlink)
cp /bin/busybox usr/bin
for name in $(busybox --list); do
  # Skip busybox itself to avoid overwriting the binary with a self-referential symlink
  if [ "$name" != "busybox" ]; then
    ln -sf busybox "usr/bin/${name}"
  fi
done

# Required shared libraries (glibc-based system)
cp /lib/x86_64-linux-gnu/lib{c,dl,nsl,nss_*,pthread,resolv}.so.* lib
cp /lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 lib

# Build squashfs
mksquashfs "${TMP}/root" "/mnt/out/layer.squashfs" -noappend
