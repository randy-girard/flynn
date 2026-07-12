#!/bin/bash
set -e

export DEBIAN_FRONTEND=noninteractive

# ---- Update base system ----
apt-get update -o Acquire::Retries=5
apt-get install -y \
  apt-transport-https \
  ca-certificates \
  curl \
  gnupg

# ---- Install ClickHouse from the official repository ----
# See https://clickhouse.com/docs/install/debian_ubuntu
install -d /usr/share/keyrings
curl -fsSL 'https://packages.clickhouse.com/rpm/lts/repodata/repomd.xml.key' \
  | gpg --dearmor -o /usr/share/keyrings/clickhouse-keyring.gpg

ARCH="$(dpkg --print-architecture)"
echo "deb [signed-by=/usr/share/keyrings/clickhouse-keyring.gpg arch=${ARCH}] https://packages.clickhouse.com/deb stable main" \
  > /etc/apt/sources.list.d/clickhouse.list

apt-get update -o Acquire::Retries=5
# clickhouse-keeper conflicts with clickhouse-server as separate deb packages,
# but the keeper binary is included via clickhouse-common-static (a dependency
# of clickhouse-server). Flynn runs keeper and server in separate jobs from the
# same image, so we only install the server and client packages here.
apt-get install -y clickhouse-server clickhouse-client

# ---- Data directory ----
mkdir -p /data

# ---- Cleanup ----
if ! mountpoint -q /var/cache/apt/archives 2>/dev/null; then
  apt-get clean
fi
rm -rf /var/lib/apt/lists/*
