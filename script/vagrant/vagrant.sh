#!/bin/bash
# Laptop loop: dev-builder plus dev-node1 by default, source synced from this
# checkout, cluster bootstrapped on the builder. This is a different Vagrant
# env from smoke (.vagrant-dev, 192.168.57.10 / .20). Do not use a bare
# `vagrant up` (that boots the smoke builder and cluster nodes).
#
# Public entry: script/vagrant.sh  (make vagrant-setup, make vagrant-up, …)
# Smoke stays on script/vagrant-smoke.sh so the two running envs never mix.
set -euo pipefail

VAGRANT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${VAGRANT_DIR}/lib/common.sh"
# shellcheck source=lib/env.sh
source "${VAGRANT_DIR}/lib/env.sh"
# shellcheck source=lib/lifecycle.sh
source "${VAGRANT_DIR}/lib/lifecycle.sh"

cd "${ROOT}"
flynn_vagrant_use dev

DEV_MACHINE="${FLYNN_VAGRANT_BUILDER}"
DEV_MEMORY="${BUILDER_MEMORY}"
DEV_CPUS="${BUILDER_CPUS}"

usage() {
  cat <<EOF
usage: script/vagrant.sh <setup|up|status|ssh|build|cli|bootstrap|update|reload|restart|stop|halt|destroy|teardown> [vm...]

  setup      Boot VMs, build images if none exist, bootstrap, and connect this laptop
  up         Boot dev-builder and extra nodes (default FLYNN_DEV_NODES=1 → dev-node1)
  status     vagrant status of every VM in this env
  ssh        Shell on dev-builder
  build      Boot the builder if needed and build images (no cluster required)
  cli        Build the laptop flynn CLI and install it to /usr/local/bin
  bootstrap  First cluster on dev-builder (script/bootstrap-flynn)
  update     flynn-host update from the newest build/release tarball
  reload     Reboot VMs (vagrant reload --no-provision) and start the cluster
  restart    Same as reload
  stop       Halt VMs (vagrant halt); disks and ./build-dev stay
  halt       Same as stop
  destroy    Delete VMs (vagrant destroy -f); does not delete ./build-dev
  teardown   Same as destroy

Make (from the repo root): make vagrant-setup, vagrant-up, vagrant-status,
vagrant-ssh, vagrant-build, vagrant-cli, vagrant-bootstrap, vagrant-update,
vagrant-reload, vagrant-stop, vagrant-destroy. make vagrant prints this help.

reload/restart/stop/destroy take optional VM names (dev-builder, dev-node1, …).
With no names they act on every machine already in .vagrant-dev.

setup is the one-time command. It builds images when none exist, then
bootstraps. You can also run build first, then setup, to bootstrap that
tarball.

  flynn -c local apps
  open https://status.1.localflynn.com

This VM is not the smoke builder. Smoke uses .vagrant and 192.168.56.0/24
(script/vagrant-smoke.sh). Dev uses .vagrant-dev, dev-builder at ${FLYNN_DEV_IP},
and dev-node1 at 192.168.57.20 by default (FLYNN_DEV_NODES=1). Set
FLYNN_DEV_NODES=0 for builder-only, or N for dev-node1..N on 192.168.57.(19+N).

Override size with VAGRANT_DEV_MEMORY and VAGRANT_DEV_CPUS.
Image builds (script/vagrant.sh build) need RAM: flynn-builder has been
OOM-killed at ~11GB RSS on a 12GB VM. Default is 30000 MB to match the smoke
builder. GOMEMLIMIT in script/flynn-builder is also capped to guest RAM.
The repo is mounted at ${SRC}. Flynn artifacts (binaries, images, tarballs)
sync to ./build-dev so they do not overwrite smoke's ./build. Sibling plugin
checkouts are mounted under /opt/flynn-plugins. Logs sync to
./flynn-logs/dev-builder. Source tree and ubuntu_ports_cache are shared with
smoke; running VMs, host-only IPs, and build outputs are not.
EOF
}

dev_vm_state() {
  machine_state "${DEV_MACHINE}"
}

# pin_dev_nodes / list_dev_machines keep extra hosts from .vagrant-dev
# (machines/dev-nodeN). Shared implementation lives in lib/lifecycle.sh.
pin_dev_nodes() { flynn_vagrant_pin_nodes; }
list_dev_machines() { flynn_vagrant_list_machines; }

# reload_dev_machines reboots VMs already in this env. It does not create
# missing machines (that would look like a smoke `vagrant up`).
reload_dev_machines() {
  flynn_vagrant_reload "$@"
}

stop_dev_machines() {
  flynn_vagrant_stop "$@"
}

destroy_dev_machines() {
  flynn_vagrant_destroy "$@"
}

# start_existing_cluster brings flynn-host back after a reboot. Nested
# bootstrap on the builder does not install a systemd unit, so a VM reload
# leaves the daemon down. Do not bootstrap a new cluster from here; exit 2
# from ensure-cluster means host.json is missing or the controller never
# answered (run script/vagrant.sh bootstrap / setup).
start_existing_cluster() {
  set +e
  run_as_root "cd ${SRC} && script/vagrant/guest/ensure-cluster.sh"
  local st=$?
  set -e
  if [[ "${st}" -eq 2 ]]; then
    echo "VMs reloaded; cluster is not bootstrapped yet (script/vagrant.sh bootstrap)"
    return 0
  fi
  return "${st}"
}

# ensure_dev_vm makes dev-builder reachable. A missing VM is not created here:
# bare `vagrant up` boots the smoke cluster, which is a different machine.
ensure_dev_vm() {
  need_vagrant
  local state
  state="$(dev_vm_state)"
  case "${state}" in
    running) return 0 ;;
    poweroff|saved|aborted)
      boot_builder
      ;;
    *)
      echo "${DEV_MACHINE} does not exist yet (${state})." >&2
      echo "Smoke's builder in .vagrant is a different VM and is not used here." >&2
      echo "Create this one with: script/vagrant.sh up or script/vagrant.sh build" >&2
      echo "First cluster: script/vagrant.sh setup (builds images if needed)" >&2
      exit 1
      ;;
  esac
}

# vagrant ssh -c runs as the vagrant user. The synced tree lives under /root,
# which that user cannot traverse, and bootstrap/update need root anyway.
run_as_root() {
  ensure_dev_vm
  vagrant ssh "${DEV_MACHINE}" -c "sudo -n bash -lc $(printf '%q' "$1")"
}

# restore_layers copies squashfs blobs from the newest local tarball into the
# layer cache. bootstrap-flynn reads /var/lib/flynn/layer-cache, and a release
# tarball does not leave those files there. Exit 2 means there is nothing to
# restore yet (setup/build will compile images instead of failing).
restore_layers() {
  run_as_root "cd ${SRC} && script/vagrant/guest/restore-layers.sh"
}

# build_images boots the builder if needed and runs flynn-builder. Does not
# require a bootstrapped cluster.
build_images() {
  boot_builder
  run_as_root "cd ${SRC} && script/vagrant/guest/build-images.sh"
}

# restore_or_build_layers restores a tarball, or builds images when setup ran
# before any vagrant-build (the old "run build then setup again" failure).
restore_or_build_layers() {
  set +e
  restore_layers
  local st=$?
  set -e
  if [[ "${st}" -eq 0 ]]; then
    return 0
  fi
  if [[ "${st}" -ne 2 ]]; then
    exit "${st}"
  fi
  echo "no cluster images yet; building on ${DEV_MACHINE}"
  build_images
  restore_layers
}

publish_cluster() {
  run_as_root "cd ${SRC} && script/vagrant/guest/publish.sh"
}

# boot_dev_cluster creates/starts every machine this env lists (builder plus
# FLYNN_DEV_NODES extra hosts, default one).
boot_dev_cluster() {
  flynn_vagrant_up_or_create "$@"
}

boot_builder() {
  boot_dev_cluster "${DEV_MACHINE}"
}

# ensure_cluster starts an existing cluster or bootstraps a new one.
# ensure-cluster.sh exits 2 when host.json is missing, is a DISCOVERD-only
# stub, or flynn-host came back without a controller (build.sh used to wipe
# volumes and leave host.json behind).
ensure_cluster() {
  set +e
  run_as_root "cd ${SRC} && script/vagrant/guest/ensure-cluster.sh"
  local st=$?
  set -e
  if [[ "${st}" -eq 0 ]]; then
    return 0
  fi
  if [[ "${st}" -ne 2 ]]; then
    exit "${st}"
  fi
  restore_or_build_layers
  run_as_root "cd ${SRC} && script/bootstrap-flynn"
}

# parse_dev_creds reads labeled FLYNN_DEV_PIN=/FLYNN_DEV_KEY= lines so a
# vagrant ssh login banner cannot shift pin/key onto the wrong fields.
parse_dev_creds() {
  local creds=$1
  pin="$(printf '%s\n' "${creds}" | sed -n 's/^FLYNN_DEV_PIN=//p' | tail -1 | tr -d '\r')"
  key="$(printf '%s\n' "${creds}" | sed -n 's/^FLYNN_DEV_KEY=//p' | tail -1 | tr -d '\r')"
  pin="${pin#"${pin%%[![:space:]]*}"}"
  pin="${pin%"${pin##*[![:space:]]}"}"
  key="${key#"${key%%[![:space:]]*}"}"
  key="${key%"${key##*[![:space:]]}"}"
  if [[ -z "${pin}" ]]; then
    pin="$(printf '%s\n' "${creds}" | grep -E '^[A-Za-z0-9+/]{43}=$' | tail -1 || true)"
  fi
  if [[ -z "${key}" ]]; then
    key="$(printf '%s\n' "${creds}" | grep -E '^[0-9a-f]{32}$' | tail -1 || true)"
  fi
}

connect_laptop() {
  local creds pin key
  creds="$(run_as_root "cd ${SRC} && script/vagrant/guest/creds.sh")"
  parse_dev_creds "${creds}"
  if [[ -z "${pin}" || -z "${key}" ]]; then
    echo "could not read the cluster pin and key" >&2
    exit 1
  fi
  CLUSTER_PIN="${pin}" CLUSTER_KEY="${key}" "${ROOT}/script/vagrant/host/mac.sh"
}

cmd="${1:-}"
case "${cmd}" in
  -h|--help|help|"")
    usage
    ;;
  setup)
    boot_dev_cluster
    ensure_cluster
    publish_cluster
    connect_laptop
    ;;
  up)
    boot_dev_cluster "${@:2}"
    publish_cluster || true
    ;;
  status)
    flynn_vagrant_status
    ;;
  ssh)
    flynn_vagrant_ssh "${DEV_MACHINE}"
    ;;
  build)
    build_images
    ;;
  cli)
    "${ROOT}/script/vagrant/host/cli.sh"
    ;;
  bootstrap)
    restore_or_build_layers
    run_as_root "cd ${SRC} && script/bootstrap-flynn"
    publish_cluster
    connect_laptop
    ;;
  update)
    run_as_root "cd ${SRC} && script/vagrant/guest/update.sh"
    ;;
  reload|restart)
    reload_dev_machines "${@:2}"
    start_existing_cluster
    publish_cluster || true
    ;;
  stop|halt)
    stop_dev_machines "${@:2}"
    ;;
  destroy|teardown)
    destroy_dev_machines "${@:2}"
    ;;
  *)
    usage >&2
    exit 1
    ;;
esac
