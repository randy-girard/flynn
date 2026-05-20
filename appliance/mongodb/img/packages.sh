#!/bin/bash
set -e

export DEBIAN_FRONTEND=noninteractive

ARCH="$(dpkg --print-architecture)"

# Map Debian arch names to MongoDB repo arch names if needed
case "$ARCH" in
  amd64|arm64)
    MONGO_ARCH="$ARCH"
    ;;
  *)
    echo "MongoDB 7.0 does not provide packages for architecture: $ARCH"
    exit 1
    ;;
esac

# ---- Dependencies ----
apt-get update
apt-get install -y \
  curl \
  gnupg \
  ca-certificates

# ---- MongoDB 7.0 GPG key ----
curl -fsSL --retry 5 --retry-delay 3 https://pgp.mongodb.com/server-7.0.asc \
  | gpg --dearmor -o /usr/share/keyrings/mongodb-server-7.0.gpg

# ---- MongoDB repo ----
echo "deb [arch=${MONGO_ARCH} signed-by=/usr/share/keyrings/mongodb-server-7.0.gpg] \
https://repo.mongodb.org/apt/ubuntu jammy/mongodb-org/7.0 multiverse" \
  > /etc/apt/sources.list.d/mongodb-org-7.0.list

# ---- Install MongoDB and mongosh ----
apt-get update
apt-get install -y mongodb-org mongodb-mongosh

# ---- Cleanup ----
if ! mountpoint -q /var/cache/apt/archives 2>/dev/null; then
  apt-get clean
fi
rm -rf /var/lib/apt/lists/*