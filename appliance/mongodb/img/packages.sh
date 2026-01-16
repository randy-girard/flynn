#!/bin/bash
set -e

export DEBIAN_FRONTEND=noninteractive

# ---- Dependencies ----
apt-get update
apt-get install -y \
  curl \
  gnupg \
  ca-certificates

# ---- MongoDB 7.0 GPG key ----
curl -fsSL --retry 5 --retry-delay 3 https://pgp.mongodb.com/server-7.0.asc \
  | gpg --dearmor -o /usr/share/keyrings/mongodb-server-7.0.gpg

# ---- MongoDB repo (jammy on noble) ----
echo "deb [arch=amd64 signed-by=/usr/share/keyrings/mongodb-server-7.0.gpg] \
https://repo.mongodb.org/apt/ubuntu jammy/mongodb-org/7.0 multiverse" \
  > /etc/apt/sources.list.d/mongodb-org-7.0.list

# ---- Install MongoDB ----
apt-get update
apt-get install -y mongodb-org

# ---- Cleanup ----
apt-get clean
rm -rf /var/lib/apt/lists/*
