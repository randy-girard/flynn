#!/bin/bash
set -e

export DEBIAN_FRONTEND=noninteractive

# ---- MongoDB 3.2 GPG key (more reliable than keyserver) ----
apt-key adv --fetch-keys https://www.mongodb.org/static/pgp/server-3.2.asc

# ---- MongoDB 3.2 repo for Trusty ----
echo "deb [trusted=yes] http://repo.mongodb.org/apt/ubuntu trusty/mongodb-org/3.2 multiverse" \
  > /etc/apt/sources.list.d/mongodb-org-3.2.list

# ---- Allow expired Release files (repo is EOL) ----
cat >/etc/apt/apt.conf.d/99mongodb-allow-old <<EOF
Acquire::Check-Valid-Until "false";
EOF

# ---- Update & install ----
apt-get update
apt-get install -y sudo mongodb-org

# ---- Cleanup ----
apt-get clean
apt-get autoremove -y
