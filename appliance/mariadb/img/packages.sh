#!/bin/bash
set -e

export DEBIAN_FRONTEND=noninteractive

# ---- Base dependencies (repo keys only; purged after install) ----
apt-get update
apt-get install -y --no-install-recommends \
  curl \
  ca-certificates \
  gnupg \
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
apt-get install -y --no-install-recommends \
  mariadb-server \
  mariadb-backup

apt-get purge -y --auto-remove curl gnupg || true
# shellcheck source=builder/img/apt-slim-finish.sh
source builder/img/apt-slim-finish.sh
