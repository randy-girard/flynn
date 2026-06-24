#!/bin/bash
#
# A script to install Docker 1.9.1 on Ubuntu 16.04.

set -eo pipefail

case "$(dpkg --print-architecture)" in
  amd64)
    DOCKER_ARCH="x86_64"
    ;;
  arm64)
    DOCKER_ARCH="aarch64"
    ;;
  *)
    echo "Unsupported architecture: $(dpkg --print-architecture)"
    exit 1
    ;;
esac

DOCKER_VERSION="28.1.1"

URL="https://download.docker.com/linux/static/stable/${DOCKER_ARCH}/docker-${DOCKER_VERSION}.tgz"

DOCKER="/usr/local/bin/docker"

main() {
  if [[ -e "${DOCKER}" ]]; then
    exit
  fi

  download_docker
  add_docker_group
  install_systemd_service
}

download_docker() {
  echo "Downloading Docker to ${DOCKER}..."
  local tmp="$(mktemp --directory)"
  trap "rm -rf ${tmp}" EXIT
  curl -fSLo "${tmp}/docker" "${URL}"
  sudo mv "${tmp}/docker" "${DOCKER}"
  sudo chmod +x "${DOCKER}"
}

add_docker_group() {
  echo "Adding Docker group"
  sudo groupadd docker
  sudo usermod -a -G docker "$(whoami)"
}

install_systemd_service() {
  echo "Installing Docker systemd unit"
  local root="$(cd "$(dirname "$0")" && pwd)"
  sudo cp "${root}/docker.socket"  "/lib/systemd/system/docker.socket"
  sudo systemctl enable docker.socket
  sudo cp "${root}/docker.service" "/lib/systemd/system/docker.service"
  sudo systemctl enable docker.service
  sudo systemctl start docker.service
}

main $@
