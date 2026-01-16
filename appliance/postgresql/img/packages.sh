#!/bin/bash

export DEBIAN_FRONTEND=noninteractive

# ---- Base system deps ----
apt-get update
apt-get install -y \
  ca-certificates \
  curl \
  gnupg \
  lsb-release \
  sudo \
  software-properties-common \
  locales

# ---- Locale ----
locale-gen en_US.UTF-8
update-locale LANG=en_US.UTF-8 LANGUAGE=en_US.UTF-8 LC_ALL=en_US.UTF-8

# ---- PostgreSQL PGDG GPG key (modern method) ----
curl -fsSL --retry 5 --retry-delay 3 https://www.postgresql.org/media/keys/ACCC4CF8.asc \
  | gpg --dearmor -o /usr/share/keyrings/postgresql.gpg

# ---- PostgreSQL PGDG repo (noble) ----
echo "deb [signed-by=/usr/share/keyrings/postgresql.gpg] \
https://apt.postgresql.org/pub/repos/apt noble-pgdg main" \
  > /etc/apt/sources.list.d/postgresql.list

# ---- TimescaleDB GPG key ----
curl -fsSL --retry 5 --retry-delay 3 https://packagecloud.io/timescale/timescaledb/gpgkey \
  | gpg --dearmor -o /usr/share/keyrings/timescaledb.gpg

# ---- TimescaleDB repo (noble) ----
echo "deb [signed-by=/usr/share/keyrings/timescaledb.gpg] \
https://packagecloud.io/timescale/timescaledb/ubuntu/ noble main" \
  > /etc/apt/sources.list.d/timescaledb.list

# ---- Install PostgreSQL + extensions ----
apt-get update -o Acquire::Retries=5
apt-get install -y \
  postgresql-16 \
  postgresql-contrib-16 \
  postgresql-16-pgextwlist \
  postgresql-16-postgis-3 \
  postgresql-16-pgrouting \
  timescaledb-2-postgresql-16 \
  less

# ---- Enable TimescaleDB ----
timescaledb-tune --yes

# ---- Cleanup ----
apt-get clean
rm -rf /var/lib/apt/lists/*

# ---- Disable psql history for root ----
echo "\set HISTFILE /dev/null" > /root/.psqlrc
