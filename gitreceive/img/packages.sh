#!/bin/bash

apt-get update
apt-get -qy install git

if ! mountpoint -q /var/cache/apt/archives 2>/dev/null; then
  rm -rf /var/cache/apt/archives/* "/var/cache/apt/archives/partial"/*
fi
rm -rf /var/lib/apt/lists/*
