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
apt-get clean

curl -fsSLo "/usr/local/bin/docker" "https://get.docker.com/builds/Linux/x86_64/docker-1.9.1"
chmod +x "/usr/local/bin/docker"

ln -sf /usr/bin/jq /usr/local/bin/jq

export HOME="/root"
git config --global "user.email" "test@flynn.io"
git config --global "user.name"  "Flynn Test"
