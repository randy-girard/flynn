#!/bin/bash
set -e

export DEBIAN_FRONTEND=noninteractive

# ---- Install required tools ----
apt-get update
apt-get install -y \
  software-properties-common \
  apt-transport-https \
  curl \
  ca-certificates \
  gnupg \
  lsb-release \
  sudo

# ---- MariaDB 10.3 repo setup (ignore EOL warning) ----
curl -LsS https://downloads.mariadb.com/MariaDB/mariadb_repo_setup \
  | bash -s -- --mariadb-server-version=10.3 2>/dev/null || true

# ---- Percona repo setup ----
curl -fsSL https://repo.percona.com/apt/percona-release_latest.bionic_all.deb -o /tmp/percona.deb
dpkg -i /tmp/percona.deb

# ---- Update package lists ----
apt-get update

# ---- Install packages ----
apt-get install -y \
  mariadb-server \
  percona-xtrabackup \
  sudo

# ---- Cleanup ----
apt-get clean
apt-get autoremove -y
rm -f /tmp/percona.deb
