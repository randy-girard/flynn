#!/bin/bash

export DEBIAN_FRONTEND=noninteractive

# ---- Base system deps (repo keys only; purged after install) ----
apt-get update
apt-get install -y --no-install-recommends \
  ca-certificates \
  curl \
  gnupg \
  sudo \
  locales \
  less

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
apt-get install -y --no-install-recommends \
  postgresql-16 \
  postgresql-contrib-16 \
  postgresql-16-pgextwlist \
  postgresql-16-postgis-3 \
  postgresql-16-pgrouting \
  timescaledb-2-postgresql-16 \
  timescaledb-tools \
  less

# Flynn appliances write postgresql.conf from appliance/postgresql/process.go.
# timescaledb-tune targets Debian's unused cluster conf and prints
# "missing: timescaledb.max_background_workers" in overlay image builds.

apt-get purge -y --auto-remove curl gnupg || true
# shellcheck source=builder/img/apt-slim-finish.sh
source builder/img/apt-slim-finish.sh

# ---- Disable psql history for root ----
echo "\set HISTFILE /dev/null" > /root/.psqlrc
