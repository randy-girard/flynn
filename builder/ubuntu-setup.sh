#!/bin/bash
#
# A script to setup an Ubuntu cloud image to be container image friendly.
#
# Adapted from the upstream docker-brew-ubuntu-core image recipes.

ln -s -f /bin/true /usr/bin/chfn

echo '#!/bin/sh' > /usr/sbin/policy-rc.d
echo 'exit 101' >> /usr/sbin/policy-rc.d
chmod +x /usr/sbin/policy-rc.d
# https://github.com/docker/docker/blob/9a9fc01af8fb5d98b8eec0740716226fadb3735c/contrib/mkimage/debootstrap#L54-L56
dpkg-divert --local --rename --add /sbin/initctl
cp -a /usr/sbin/policy-rc.d /sbin/initctl
sed -i 's/^exit.*/exit 0/' /sbin/initctl
# https://github.com/docker/docker/blob/9a9fc01af8fb5d98b8eec0740716226fadb3735c/contrib/mkimage/debootstrap#L71-L78
echo 'force-unsafe-io' > /etc/dpkg/dpkg.cfg.d/docker-apt-speedup
# Docker's debootstrap recipe also installs Post-Invoke hooks that delete *.deb archives
# after every apt operation. Omit those so "$(pwd)/ubuntu_ports_cache" bind-mounted at
# /var/cache/apt/archives can reuse downloads across flynn-builder jobs; layers remove
# archives explicitly (apt-get clean / rm) where image size matters.
# https://github.com/docker/docker/blob/9a9fc01af8fb5d98b8eec0740716226fadb3735c/contrib/mkimage/debootstrap#L85-L105
echo 'Dir::Cache::pkgcache ""; Dir::Cache::srcpkgcache "";' > /etc/apt/apt.conf.d/docker-apt-mini
# https://github.com/docker/docker/blob/9a9fc01af8fb5d98b8eec0740716226fadb3735c/contrib/mkimage/debootstrap#L109-L115
echo 'Acquire::Languages "none";' > /etc/apt/apt.conf.d/docker-no-languages
# https://github.com/docker/docker/blob/9a9fc01af8fb5d98b8eec0740716226fadb3735c/contrib/mkimage/debootstrap#L118-L130
echo 'Acquire::GzipIndexes "true"; Acquire::CompressionTypes::Order:: "gz";' > /etc/apt/apt.conf.d/docker-gzip-indexes
# https://github.com/docker/docker/blob/9a9fc01af8fb5d98b8eec0740716226fadb3735c/contrib/mkimage/debootstrap#L134-L151
echo 'Apt::AutoRemove::SuggestsImportant "false";' > /etc/apt/apt.conf.d/docker-autoremove-suggests

cat > /etc/apt/apt.conf.d/80-retries <<'EOF'
Acquire::Retries "5";
Acquire::http::Timeout "30";
Acquire::https::Timeout "30";
Acquire::Queue-Mode "access";
EOF

export DEBIAN_FRONTEND=noninteractive

# flynn-builder flynnAptLayerPrelude only prepares the outer build root; Noble uses chroot +
# ubuntu-setup.sh. Match that prelude here so _apt can use the bind-mounted
# /var/cache/apt/archives and /var/lib/apt/lists, and sandboxing does not hit
# root-owned partial/ files (pkgAcquire Permission denied).
mkdir -p /tmp
chmod 1777 /tmp 2>/dev/null || true
mkdir -p /var/cache/apt/archives/partial /var/lib/apt/lists/partial
chmod a+rwx /var/cache/apt/archives /var/cache/apt/archives/partial 2>/dev/null || true
chmod -R a+rwX /var/cache/apt/archives/partial 2>/dev/null || true
chmod a+rwx /var/lib/apt/lists /var/lib/apt/lists/partial 2>/dev/null || true
chmod -R a+rwX /var/lib/apt/lists/partial 2>/dev/null || true
install -d /etc/apt/apt.conf.d
printf '%s\n' 'APT::Sandbox::User "root";' > /etc/apt/apt.conf.d/50flynn-apt-sandbox.conf

# ---- Configure APT mirrors early ----

# Force IPv4 (prevents archive.ubuntu.com IPv6 blackholes)
cat > /etc/apt/apt.conf.d/99force-ipv4 <<'EOF'
Acquire::ForceIPv4 "true";
EOF

# Replace archive.ubuntu.com with reliable mirrors
sed -i \
  -e 's|http://archive.ubuntu.com/ubuntu|http://mirrors.edge.kernel.org/ubuntu|g' \
  -e 's|http://security.ubuntu.com/ubuntu|http://security.ubuntu.com/ubuntu|g' \
  /etc/apt/sources.list

# Cloud tarball list slices reference pool versions from image build day; ubuntu-ports can 404 old
# .deb URLs once the pool rotates. Drop the stale, image-shipped indexes so APT re-fetches against
# current Packages files — but only when the lists dir is not a shared host cache (which already
# holds current indexes maintained across builds).
if ! mountpoint -q /var/lib/apt/lists 2>/dev/null; then
  rm -rf /var/lib/apt/lists/*
fi

# Builder host apt sources (docker.com / mongodb) must not leak into this chroot.
rm -f /etc/apt/sources.list.d/docker.sources \
  /etc/apt/sources.list.d/docker.list \
  /etc/apt/sources.list.d/mongodb-org-8.0.list \
  /etc/apt/sources.list.d/mongodb-org-*.list 2>/dev/null || true

flynn_chroot_apt() {
  local n=1 max=5 delay=5 rc=0
  while true; do
    if apt-get "$@"; then
      return 0
    fi
    rc=$?
    if [[ "${n}" -ge "${max}" ]]; then
      return "${rc}"
    fi
    echo "chroot apt-get $* failed (attempt ${n}/${max}, rc=${rc}); retrying in ${delay}s..." >&2
    rm -rf /var/lib/apt/lists/partial/* /var/cache/apt/archives/partial/* 2>/dev/null || true
    if [[ "${n}" -ge 2 ]]; then
      rm -f /var/lib/apt/lists/*InRelease /var/lib/apt/lists/*_InRelease 2>/dev/null || true
    fi
    sleep "${delay}"
    n=$((n + 1))
    delay=$((delay * 2))
    if [[ "${delay}" -gt 45 ]]; then
      delay=45
    fi
  done
}

# update packages
flynn_chroot_apt update
flynn_chroot_apt dist-upgrade --yes

# install common Flynn image tools (iproute2: diagnostics matching flynn-host collect-debug-info)
flynn_chroot_apt install --yes --no-install-recommends squashfs-tools curl gnupg coreutils iproute2 ca-certificates

# Cloud images ship snapd/cloud-init/landscape/etc. Flynn containers never use them.
flynn_chroot_apt purge --yes --auto-remove \
  snapd \
  cloud-init \
  cloud-initramfs-copymods \
  cloud-initramfs-dyn-netconf \
  landscape-common \
  pollinate \
  ubuntu-pro-client \
  ubuntu-pro-client-l10n \
  command-not-found \
  python3-commandnotfound \
  fwupd \
  unattended-upgrades \
  open-vm-tools \
  plymouth \
  popularity-contest \
  lxd-installer \
  || true
rm -rf /root/snap /var/lib/snapd /var/cache/snapd /usr/lib/snapd || true

# Strip downloaded packages from this rootfs unless a flynn-builder host APT cache bind is mounted
# (see builder/build.go). Keeps Noble/SquashFS layers slim without wiping the shared ./ubuntu_ports_cache.
if ! mountpoint -q /var/cache/apt/archives 2>/dev/null; then
  rm -rf /var/cache/apt/archives/* "/var/cache/apt/archives/partial"/*
fi

# delete the apt list files baked into this rootfs (big, stale fast). Skip when a host
# lists cache is bind-mounted — that cache is shared across builds and never goes in the layer.
if ! mountpoint -q /var/lib/apt/lists 2>/dev/null; then
  rm -rf /var/lib/apt/lists/*
fi
# this forces "apt-get update" in dependent images (incremental against the host cache), which is also good

# enable the universe
sed -i 's/^#\s*\(deb.*universe\)$/\1/g' /etc/apt/sources.list

# make systemd-detect-virt return "docker"
# See: https://github.com/systemd/systemd/blob/aa0c34279ee40bce2f9681b496922dedbadfca19/src/basic/virt.c#L434
mkdir -p /run/systemd && echo 'docker' > /run/systemd/container
