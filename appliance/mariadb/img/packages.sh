#!/bin/bash
set -e

export DEBIAN_FRONTEND=noninteractive

# ---- Base dependencies ----
apt-get update
apt-get install -y \
  curl \
  ca-certificates \
  gnupg \
  lsb-release \
  sudo

# ---- MariaDB GPG key ----
curl -fsSL --retry 5 --retry-delay 3 https://mariadb.org/mariadb_release_signing_key.asc \
  | gpg --dearmor -o /usr/share/keyrings/mariadb.gpg

# ---- MariaDB 10.11 LTS repo (noble) ----
echo "deb [signed-by=/usr/share/keyrings/mariadb.gpg] \
https://mirror.mariadb.org/repo/10.11/ubuntu noble main" \
  > /etc/apt/sources.list.d/mariadb.list

# ---- Percona GPG key ----
curl -fsSL --retry 5 --retry-delay 3 https://repo.percona.com/apt/percona-release_latest.jammy_all.deb \
  -o /tmp/percona-release.deb

dpkg -i /tmp/percona-release.deb

# ---- Enable Percona tools repo ----
percona-release enable tools release

# ---- Update package lists ----
apt-get update

# ---- Install MariaDB + XtraBackup ----
apt-get install -y \
  mariadb-server \
  percona-xtrabackup-80

# ---- Cleanup ----
apt-get clean
rm -rf /var/lib/apt/lists/*
rm -f /tmp/percona-release.deb
