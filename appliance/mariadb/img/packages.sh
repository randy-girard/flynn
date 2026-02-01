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

# ---- Update package lists ----
apt-get update

# ---- Install MariaDB + mariabackup ----
# mariadb-backup package contains mariabackup binary (required for MariaDB 10.3+)
# Percona XtraBackup is NOT compatible with MariaDB 10.11
apt-get install -y \
  mariadb-server \
  mariadb-backup

# ---- Cleanup ----
apt-get clean
rm -rf /var/lib/apt/lists/*
