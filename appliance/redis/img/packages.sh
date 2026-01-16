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
apt-get clean
rm -rf /var/lib/apt/lists/*
