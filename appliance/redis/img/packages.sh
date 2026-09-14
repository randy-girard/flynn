#!/bin/bash
set -e

export DEBIAN_FRONTEND=noninteractive

# ---- Update base system ----
apt-get update -o Acquire::Retries=5
apt-get install -y --no-install-recommends \
  redis-server \
  curl

# ---- Data directory ----
mkdir -p /data

# curl is required at runtime (appliance/redis/restore.sh).
# shellcheck source=builder/img/apt-slim-finish.sh
source builder/img/apt-slim-finish.sh
