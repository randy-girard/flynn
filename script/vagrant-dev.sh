#!/bin/bash
# Laptop loop: dev-builder only, source synced from this checkout, cluster
# bootstrapped on that VM. This is a different Vagrant env from smoke
# (.vagrant-dev, 192.168.57.10). Do not use a bare `vagrant up` (that boots
# the smoke builder and cluster nodes).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${ROOT}"

# Separate from smoke: own Vagrant index, VM name, and host-only subnet.
export FLYNN_VAGRANT_ENV=dev
export VAGRANT_DOTFILE_PATH="${ROOT}/.vagrant-dev"
export FLYNN_DEV_IP="${FLYNN_DEV_IP:-192.168.57.10}"
DEV_MACHINE=dev-builder

DEV_MEMORY="${VAGRANT_DEV_MEMORY:-12288}"
DEV_CPUS="${VAGRANT_DEV_CPUS:-4}"
SRC="/root/go/src/github.com/flynn/flynn"

usage() {
  cat <<EOF
usage: script/vagrant-dev.sh <setup|up|status|ssh|build|cli|bootstrap|update>

  setup      Boot dev-builder, start the cluster, and connect this laptop
  up         Boot only dev-builder (${DEV_MEMORY} MB, ${DEV_CPUS} CPUs unless overridden)
  status     vagrant status dev-builder
  ssh        Shell on dev-builder
  build      Build cluster images on dev-builder (includes the Ubuntu base layer the first time)
  cli        Build the laptop flynn CLI and install it to /usr/local/bin
  bootstrap  First cluster on dev-builder (script/bootstrap-flynn)
  update     flynn-host update from the newest build/release tarball

setup is the one-time command. After it finishes:

  flynn -c local apps
  open https://status.1.localflynn.com

This VM is not the smoke builder. Smoke uses .vagrant and 192.168.56.0/24.
Dev uses .vagrant-dev, dev-builder, and ${FLYNN_DEV_IP}. Extra hosts are
FLYNN_DEV_NODES=N (dev-node1..N on 192.168.57.(19+N)).

Override size with VAGRANT_DEV_MEMORY and VAGRANT_DEV_CPUS.
The repo is mounted at ${SRC}. Sibling plugin checkouts are mounted under
/opt/flynn-plugins. Logs sync to ./flynn-logs/dev-builder.
EOF
}

need_vagrant() {
  command -v vagrant >/dev/null 2>&1 || {
    echo "vagrant is not installed" >&2
    exit 1
  }
}

# vagrant ssh -c runs as the vagrant user. The synced tree lives under /root,
# which that user cannot traverse, and bootstrap/update need root anyway.
run_as_root() {
  need_vagrant
  vagrant ssh "${DEV_MACHINE}" -c "sudo -n bash -lc $(printf '%q' "$1")"
}

# restore_layers copies squashfs blobs from the newest local tarball into the
# layer cache. bootstrap-flynn reads /var/lib/flynn/layer-cache, and a release
# tarball does not leave those files there.
restore_layers() {
  run_as_root "cd ${SRC} && script/vagrant-dev-restore-layers.sh"
}

publish_cluster() {
  run_as_root "cd ${SRC} && script/vagrant-dev-publish.sh"
}

boot_builder() {
  need_vagrant
  echo "booting ${DEV_MACHINE} only (${DEV_MEMORY} MB, ${DEV_CPUS} CPUs) at ${FLYNN_DEV_IP}"
  VAGRANT_MEMORY="${DEV_MEMORY}" VAGRANT_CPUS="${DEV_CPUS}" vagrant up "${DEV_MACHINE}"
}

# ensure_cluster starts an existing cluster or bootstraps a new one.
ensure_cluster() {
  set +e
  run_as_root "cd ${SRC} && script/vagrant-dev-ensure-cluster.sh"
  local st=$?
  set -e
  if [[ "${st}" -eq 0 ]]; then
    return 0
  fi
  if [[ "${st}" -ne 2 ]]; then
    exit "${st}"
  fi
  restore_layers
  run_as_root "cd ${SRC} && script/bootstrap-flynn"
}

connect_laptop() {
  local creds pin key
  creds="$(run_as_root "cd ${SRC} && script/vagrant-dev-creds.sh")"
  pin="$(printf '%s\n' "${creds}" | sed -n '1p')"
  key="$(printf '%s\n' "${creds}" | sed -n '2p')"
  if [[ -z "${pin}" || -z "${key}" ]]; then
    echo "could not read the cluster pin and key" >&2
    exit 1
  fi
  CLUSTER_PIN="${pin}" CLUSTER_KEY="${key}" "${ROOT}/script/vagrant-dev-mac.sh"
}

cmd="${1:-}"
case "${cmd}" in
  -h|--help|help|"")
    usage
    ;;
  setup)
    boot_builder
    ensure_cluster
    publish_cluster
    connect_laptop
    ;;
  up)
    boot_builder
    publish_cluster || true
    ;;
  status)
    need_vagrant
    vagrant status "${DEV_MACHINE}"
    ;;
  ssh)
    need_vagrant
    exec vagrant ssh "${DEV_MACHINE}" -- -t "sudo -n bash -lc 'cd ${SRC} && exec bash -l'"
    ;;
  build)
    # cluster reuses /var/lib/flynn/base-layer.squashfs. The first build has to
    # create that layer (./build.sh with no phase is base, then cluster).
    run_as_root "cd ${SRC} && if [ -f /var/lib/flynn/base-layer.squashfs ]; then ./build.sh cluster; else ./build.sh; fi"
    ;;
  cli)
    "${ROOT}/script/vagrant-dev-cli.sh"
    ;;
  bootstrap)
    restore_layers
    run_as_root "cd ${SRC} && script/bootstrap-flynn"
    publish_cluster
    connect_laptop
    ;;
  update)
    run_as_root "cd ${SRC} && script/vagrant-dev-update.sh"
    ;;
  *)
    usage >&2
    exit 1
    ;;
esac
