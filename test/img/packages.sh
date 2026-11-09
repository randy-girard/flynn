#!/bin/bash

apt-get update
apt-get --yes install \
  curl \
  ca-certificates \
  git \
  zerofree \
  qemu-system-x86 \
  qemu-utils \
  qemu-kvm \
  iptables \
  iproute2 \
  jq

# The client must match the machine building the image. docker-1.9.1 was
# x86_64-only and cannot exec on arm64. 27.5.1 is a static CLI; the host
# daemon is mounted in at test time.
case "$(uname -m)" in
  x86_64) docker_arch=x86_64 ;;
  aarch64 | arm64) docker_arch=aarch64 ;;
  *)
    echo "unsupported docker client arch $(uname -m)" >&2
    exit 1
    ;;
esac
curl -fsSL -o /tmp/docker.tgz "https://download.docker.com/linux/static/stable/${docker_arch}/docker-27.5.1.tgz"
tar -xz -C /usr/local/bin --strip-components=1 -f /tmp/docker.tgz docker/docker
rm -f /tmp/docker.tgz
chmod +x /usr/local/bin/docker

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
