#!/bin/bash

apt-get update
apt-get -qy install git
if ! mountpoint -q /var/cache/apt/archives 2>/dev/null; then
  apt-get clean
fi
