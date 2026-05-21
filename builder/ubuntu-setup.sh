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
Acquire::Retries "3";
Acquire::http::Timeout "15";
Acquire::https::Timeout "15";
Acquire::Queue-Mode "access";
EOF

export DEBIAN_FRONTEND=noninteractive

# flynn-builder flynnAptLayerPrelude only prepares the outer build root; Noble uses chroot +
# ubuntu-setup.sh. Match that prelude here so _apt can use a bind-mounted
# /var/cache/apt/archives and sandboxing does not hit root-owned partial/ files (pkgAcquire Permission denied).
mkdir -p /var/cache/apt/archives/partial
chmod a+rwx /var/cache/apt/archives /var/cache/apt/archives/partial 2>/dev/null || true
chmod -R a+rwX /var/cache/apt/archives/partial 2>/dev/null || true
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
# .deb URLs once the pool rotates. Drop lists before update so APT matches current Packages indexes.
rm -rf /var/lib/apt/lists/*

# update packages
apt-get update
apt-get dist-upgrade --yes

# install common Flynn image tools (net-tools / iproute2: diagnostics matching flynn-host collect-debug-info)
apt-get install --yes squashfs-tools curl gnupg coreutils net-tools iproute2

# Strip downloaded packages from this rootfs unless a flynn-builder host APT cache bind is mounted
# (see builder/build.go). Keeps Noble/SquashFS layers slim without wiping the shared ./ubuntu_ports_cache.
if ! mountpoint -q /var/cache/apt/archives 2>/dev/null; then
  rm -rf /var/cache/apt/archives/* "/var/cache/apt/archives/partial"/*
fi

# delete all the apt list files since they're big and get stale quickly
rm -rf /var/lib/apt/lists/*
# this forces "apt-get update" in dependent images, which is also good

# enable the universe
sed -i 's/^#\s*\(deb.*universe\)$/\1/g' /etc/apt/sources.list

# make systemd-detect-virt return "docker"
# See: https://github.com/systemd/systemd/blob/aa0c34279ee40bce2f9681b496922dedbadfca19/src/basic/virt.c#L434
mkdir -p /run/systemd && echo 'docker' > /run/systemd/container
