#!/bin/bash

export DEBIAN_FRONTEND=noninteractive

apt-get update
# openssh-client: ssh submodules for apps in SSH_CLIENT_KEY_APPS are fetched
# in this image (EDGE-26). git only Recommends it, so install it explicitly.
apt-get -qy install --no-install-recommends git openssh-client

# shellcheck source=builder/img/apt-slim-finish.sh
source builder/img/apt-slim-finish.sh
