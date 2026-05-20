#!/bin/bash
set -e

export DEBIAN_FRONTEND=noninteractive

# ---- Update base system ----
apt-get update -o Acquire::Retries=5
apt-get install -y \
  redis-server \
  curl

# ---- Data directory ----
mkdir -p /data

# ---- Cleanup ----
if ! mountpoint -q /var/cache/apt/archives 2>/dev/null; then
  apt-get clean
fi
rm -rf /var/lib/apt/lists/*
