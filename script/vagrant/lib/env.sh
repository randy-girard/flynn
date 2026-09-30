#!/bin/bash
# Select the smoke or laptop-loop Vagrant env. Call flynn_vagrant_use first.
# Smoke: .vagrant, builder/nodeN, 192.168.56.0/24, ./build
# Dev:   .vagrant-dev, dev-builder/dev-nodeN, 192.168.57.0/24, ./build-dev
# Both share the source tree, ubuntu_ports_cache, and this library.

flynn_vagrant_get_nodes() {
  case "${FLYNN_VAGRANT_NODE_COUNT_VAR}" in
    FLYNN_DEV_NODES) printf '%s' "${FLYNN_DEV_NODES:-}" ;;
    FLYNN_MAX_NODES) printf '%s' "${FLYNN_MAX_NODES:-}" ;;
  esac
}

flynn_vagrant_set_nodes() {
  case "${FLYNN_VAGRANT_NODE_COUNT_VAR}" in
    FLYNN_DEV_NODES) export FLYNN_DEV_NODES="$1" ;;
    FLYNN_MAX_NODES) export FLYNN_MAX_NODES="$1" ;;
  esac
}

flynn_vagrant_use() {
  local env="${1:?flynn_vagrant_use needs smoke or dev}"
  case "${env}" in
    dev)
      export FLYNN_VAGRANT_ENV=dev
      export VAGRANT_DOTFILE_PATH="${ROOT}/.vagrant-dev"
      export FLYNN_DEV_IP="${FLYNN_DEV_IP:-192.168.57.10}"
      FLYNN_VAGRANT_BUILDER=dev-builder
      FLYNN_VAGRANT_NODE_PREFIX=dev-node
      FLYNN_VAGRANT_NODE_COUNT_VAR=FLYNN_DEV_NODES
      FLYNN_VAGRANT_PIN_STYLE=consecutive
      FLYNN_VAGRANT_PIN_MIN=0
      FLYNN_VAGRANT_LIST_CREATED_ONLY=0
      if [[ -z "${FLYNN_DEV_NODES:-}" ]]; then
        export FLYNN_DEV_NODES=1
      fi
      export FLYNN_DEV_DOMAIN="${FLYNN_DEV_DOMAIN:-1.localflynn.com}"
      BUILDER_MEMORY="${VAGRANT_DEV_MEMORY:-30000}"
      BUILDER_CPUS="${VAGRANT_DEV_CPUS:-4}"
      NODE_MEMORY="${VAGRANT_NODE_MEMORY:-6144}"
      NODE_CPUS="${VAGRANT_NODE_CPUS:-2}"
      FLYNN_VAGRANT_EMPTY_HINT="script/vagrant.sh up or script/vagrant.sh build (first cluster: script/vagrant.sh setup)"
      ;;
    smoke)
      export FLYNN_VAGRANT_ENV=smoke
      export VAGRANT_DOTFILE_PATH="${ROOT}/.vagrant"
      FLYNN_VAGRANT_BUILDER=builder
      FLYNN_VAGRANT_NODE_PREFIX=node
      FLYNN_VAGRANT_NODE_COUNT_VAR=FLYNN_MAX_NODES
      FLYNN_VAGRANT_PIN_STYLE=max
      FLYNN_VAGRANT_PIN_MIN=1
      FLYNN_VAGRANT_LIST_CREATED_ONLY=1
      BUILDER_MEMORY="${BUILDER_MEMORY:-30000}"
      BUILDER_CPUS="${BUILDER_CPUS:-8}"
      NODE_MEMORY="${VAGRANT_NODE_MEMORY:-6144}"
      NODE_CPUS="${VAGRANT_NODE_CPUS:-2}"
      FLYNN_VAGRANT_EMPTY_HINT="script/vagrant-smoke.sh --item quick"
      ;;
    *)
      echo "FLYNN_VAGRANT_ENV must be smoke or dev (got ${env})" >&2
      exit 1
      ;;
  esac
}
