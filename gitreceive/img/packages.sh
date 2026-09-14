#!/bin/bash

export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get -qy install --no-install-recommends git

# shellcheck source=builder/img/apt-slim-finish.sh
source builder/img/apt-slim-finish.sh
