#!/bin/bash
set -e -x

# init environment
export LC_ALL=C

# set up ubuntu user
addgroup docker
adduser --disabled-password --gecos "" ubuntu
usermod -a -G sudo ubuntu
usermod -a -G docker ubuntu
mkdir -p /etc/sudoers.d
echo %ubuntu ALL=NOPASSWD:ALL > /etc/sudoers.d/ubuntu
chmod 0440 /etc/sudoers.d/ubuntu
echo ubuntu:ubuntu | chpasswd

# set up fstab
echo "LABEL=rootfs / ext4 defaults 0 1" > /etc/fstab

# setup networking
cat > /etc/systemd/network/10-flynn.network <<EOF
[Match]
Name=en*

[Network]
DHCP=ipv4
EOF
systemctl enable systemd-networkd.service

# configure hosts and dns resolution
echo "127.0.0.1 localhost localhost.localdomain" > /etc/hosts

# enable universe
sed -i "s/^#\s*\(deb.*universe\)\$/\1/g" /etc/apt/sources.list

# use GCP apt mirror
sed -i \
  "s/archive.ubuntu.com/us-central1.gce.archive.ubuntu.com/g" \
  /etc/apt/sources.list

# disable apt caching and add speedups
echo "force-unsafe-io" > /etc/dpkg/dpkg.cfg.d/02apt-speedup
cat >/etc/apt/apt.conf.d/no-cache <<EOF
DPkg::Post-Invoke {
  "rm -f \
    /var/cache/apt/archives/*.deb \
    /var/cache/apt/archives/partial/*.deb \
    /var/cache/apt/*.bin \
    || true";
};
APT::Update::Post-Invoke {
  "rm -f \
    /var/cache/apt/archives/*.deb \
    /var/cache/apt/archives/partial/*.deb \
    /var/cache/apt/*.bin \
    || true";
};
Dir::Cache::pkgcache "";
Dir::Cache::srcpkgcache "";
EOF
echo 'Acquire::Languages "none";' > /etc/apt/apt.conf.d/no-languages

# update packages
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install --install-recommends linux-image-generic udev \
  -y \
  -o Dpkg::Options::="--force-confdef" \
  -o Dpkg::Options::="--force-confold"
apt-get dist-upgrade \
  -y \
  -o Dpkg::Options::="--force-confdef" \
  -o Dpkg::Options::="--force-confold"

# install ssh server and go deps
apt-get install -y apt-transport-https openssh-server git make curl ca-certificates gnupg

# install docker
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
chmod a+r /etc/apt/keyrings/docker.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu noble stable" \
  > /etc/apt/sources.list.d/docker.list
apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io
systemctl disable docker

apt-get update

# install build dependencies
apt-get install -y \
  build-essential \
  zfsutils-linux \
  btrfs-progs \
  inotify-tools \
  libsasl2-dev \
  libseccomp-dev \
  squashfs-tools \
  pkg-config

# install flynn test dependencies: postgres, redis, mariadb
# (normally these are used via appliances; install locally for unit tests)
apt-get -qy --fix-missing install language-pack-en
update-locale LANG=en_US.UTF-8 LANGUAGE=en_US.UTF-8 LC_ALL=en_US.UTF-8
dpkg-reconfigure locales

# add the mongodb-org repo (mongodb is not in the noble archive)
curl -fsSL https://pgp.mongodb.com/server-8.0.asc \
  | gpg --dearmor -o /etc/apt/keyrings/mongodb.gpg
chmod a+r /etc/apt/keyrings/mongodb.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/mongodb.gpg] https://repo.mongodb.org/apt/ubuntu noble/mongodb-org/8.0 multiverse" \
  > /etc/apt/sources.list.d/mongodb-org-8.0.list

# update lists
apt-get update

# install packages (postgres, mariadb, redis come from the noble archive)
apt-get install -y postgresql-16 postgresql-contrib-16 redis-server mariadb-server mariadb-backup mongodb-org sudo net-tools

pg_ctlcluster --skip-systemctl-redirect 16-main start
sudo -u postgres createuser --superuser ubuntu
pg_ctlcluster --skip-systemctl-redirect 16-main -m fast stop

systemctl disable postgresql
systemctl disable mysql
systemctl disable mongod
systemctl disable redis-server

# allow the test runner to set certain environment variables
echo AcceptEnv TEST_RUNNER_AUTH_KEY BLOBSTORE_S3_CONFIG BLOBSTORE_GCS_CONFIG BLOBSTORE_AZURE_CONFIG >> /etc/ssh/sshd_config

# install Bats and jq for running script unit tests
apt-get install -y jq
git clone https://github.com/sstephenson/bats.git "${tmpdir}/bats"
"${tmpdir}/bats/install.sh" "/usr/local"

# cleanup
apt-get autoremove -y
apt-get clean
