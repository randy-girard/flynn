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

# ---- Strip file capabilities from the clickhouse binary ----
# The deb sets cap_net_admin,cap_ipc_lock,cap_sys_nice,cap_net_bind_service=ep
# on /usr/bin/clickhouse. Those "ep" file capabilities must be present in the
# process bounding set at exec time, but Flynn's container bounding set omits
# net_admin/ipc_lock/sys_nice, so execve() fails with EPERM ("operation not
# permitted"). ClickHouse runs without them (it only skips the corresponding
# optimizations), so clear them so the server and keeper jobs can start.
apt-get install -y libcap2-bin
setcap -r /usr/bin/clickhouse || true

# ---- Data directory ----
mkdir -p /data

# ---- Cleanup ----
if ! mountpoint -q /var/cache/apt/archives 2>/dev/null; then
  apt-get clean
fi
rm -rf /var/lib/apt/lists/*
