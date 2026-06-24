#!/bin/bash

apt-get update
apt-get --yes install \
  git \
  zerofree \
  qemu-system-x86 \
  qemu-utils \
  qemu-kvm \
  iptables \
  iproute2 \
  jq

curl -fsSLo "/usr/local/bin/docker" "https://get.docker.com/builds/Linux/x86_64/docker-1.9.1"
chmod +x "/usr/local/bin/docker"

ln -sf /usr/bin/jq /usr/local/bin/jq

export HOME="/root"
git config --global "user.email" "test@flynn.io"
git config --global "user.name"  "Flynn Test"

if ! mountpoint -q /var/cache/apt/archives 2>/dev/null; then
  rm -rf /var/cache/apt/archives/* "/var/cache/apt/archives/partial"/*
fi
if ! mountpoint -q /var/lib/apt/lists 2>/dev/null; then
  rm -rf /var/lib/apt/lists/*
fi
