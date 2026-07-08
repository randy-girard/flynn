#!/bin/bash
set -e

export DEBIAN_FRONTEND=noninteractive

KAFKA_VERSION="3.9.0"
SCALA_VERSION="2.13"
KAFKA_DIST="kafka_${SCALA_VERSION}-${KAFKA_VERSION}"

# ---- Update base system & install a JRE ----
apt-get update -o Acquire::Retries=5
apt-get install -y \
  openjdk-17-jre-headless \
  openssl \
  curl \
  ca-certificates

# ---- Install Kafka (KRaft mode, no ZooKeeper) ----
curl -fSL "https://archive.apache.org/dist/kafka/${KAFKA_VERSION}/${KAFKA_DIST}.tgz" \
  -o /tmp/kafka.tgz
mkdir -p /opt
tar -xzf /tmp/kafka.tgz -C /opt
mv "/opt/${KAFKA_DIST}" /opt/kafka
rm -f /tmp/kafka.tgz

# ---- Data directory ----
mkdir -p /data

# ---- Cleanup ----
if ! mountpoint -q /var/cache/apt/archives 2>/dev/null; then
  apt-get clean
fi
rm -rf /var/lib/apt/lists/*
