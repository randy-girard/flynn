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

# ---- Dependencies (repo keys only; purged after install) ----
apt-get update
apt-get install -y --no-install-recommends \
  curl \
  gnupg \
  ca-certificates \
  sudo

# ---- MongoDB 7.0 GPG key ----
curl -fsSL --retry 5 --retry-delay 3 https://pgp.mongodb.com/server-7.0.asc \
  | gpg --dearmor -o /usr/share/keyrings/mongodb-server-7.0.gpg

# ---- MongoDB repo ----
echo "deb [arch=${MONGO_ARCH} signed-by=/usr/share/keyrings/mongodb-server-7.0.gpg] \
https://repo.mongodb.org/apt/ubuntu jammy/mongodb-org/7.0 multiverse" \
  > /etc/apt/sources.list.d/mongodb-org-7.0.list

# mongod + dump/restore tools + mongosh. Skip the mongodb-org metapackage
# (mongos, legacy mongo shell).
apt-get update
apt-get install -y --no-install-recommends \
  mongodb-org-server \
  mongodb-database-tools \
  mongodb-mongosh

apt-get purge -y --auto-remove curl gnupg || true
# shellcheck source=builder/img/apt-slim-finish.sh
source builder/img/apt-slim-finish.sh
