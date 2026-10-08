#!/bin/bash
# Laptop loop: dev-builder compiles Flynn; dev-node1 (default) runs the live
# cluster. This is a different Vagrant env from smoke (.vagrant-dev,
# 192.168.57.10 builder / .20 node1). Do not use a bare `vagrant up` (that
# boots the smoke builder and cluster nodes).
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
# shellcheck source=lib/cluster.sh
source "${VAGRANT_DIR}/lib/cluster.sh"

cd "${ROOT}"
flynn_vagrant_use dev

FLYNN_VAGRANT_YES="${FLYNN_VAGRANT_YES:-0}"
FLYNN_VAGRANT_FORCE_BUILD="${FLYNN_VAGRANT_FORCE_BUILD:-0}"
FLYNN_VAGRANT_SKIP_LAPTOP_CONNECT="${FLYNN_VAGRANT_SKIP_LAPTOP_CONNECT:-0}"
parsed=()
for arg in "$@"; do
  case "${arg}" in
    --yes|-y) FLYNN_VAGRANT_YES=1 ;;
    --force-build) FLYNN_VAGRANT_FORCE_BUILD=1 ;;
    --skip-laptop-connect) FLYNN_VAGRANT_SKIP_LAPTOP_CONNECT=1 ;;
    *) parsed+=("${arg}") ;;
  esac
done
if [[ ${#parsed[@]} -gt 0 ]]; then
  set -- "${parsed[@]}"
else
  set --
fi
export FLYNN_VAGRANT_YES FLYNN_VAGRANT_FORCE_BUILD FLYNN_VAGRANT_SKIP_LAPTOP_CONNECT
if [[ "${FLYNN_VAGRANT_YES}" == "1" ]]; then
  export DEBIAN_FRONTEND=noninteractive
  export FLYNN_SKIP_UPDATE_CHECK=1
fi

DEV_MACHINE="${FLYNN_VAGRANT_BUILDER}"
DEV_MEMORY="${BUILDER_MEMORY}"
DEV_CPUS="${BUILDER_CPUS}"

usage() {
  cat <<EOF
usage: script/vagrant.sh [--yes] [--force-build] [--skip-laptop-connect] <setup|up|status|ssh|build|cli|bootstrap|update|probe|reload|restart|stop|halt|destroy|teardown> [vm...]

  --yes, -y              Non-interactive: sudo -n, no TTY prompts (also FLYNN_VAGRANT_YES=1)
  --force-build          Rebuild cluster images on update even if the stamp matches
  --skip-laptop-connect  Do not write /etc/hosts or flynn cluster:add on this laptop

  setup      Boot VMs, build images if none exist, bootstrap cluster nodes, connect this laptop
  up         Boot dev-builder and extra nodes (default FLYNN_DEV_NODES=1 → dev-node1)
  status     vagrant status of every VM in this env
  ssh        Shell on a VM (default dev-builder; VM=dev-node1 for the live cluster)
  build      Boot the builder if needed and build images (no cluster required; builder Flynn is compile-only)
  cli        Build the laptop flynn CLI and install it to /usr/local/bin
  bootstrap  Install the tarball and flynn-host bootstrap on cluster nodes (not the builder)
  update     Build on the builder if Flynn source changed, then flynn-host update on running cluster nodes
  probe      Check the live cluster from node1 (hosts, controller, flynn-host list)
  reload     Reboot VMs (vagrant reload --no-provision) and start flynn-host on cluster nodes
  restart    Same as reload
  stop       Halt VMs (vagrant halt); disks and ./build-dev stay
  halt       Same as stop
  destroy    Delete VMs (vagrant destroy -f); does not delete ./build-dev
  teardown   Same as destroy

Make (from the repo root): make vagrant-setup YES=1, vagrant-update YES=1 FORCE_BUILD=1,
vagrant-ssh VM=dev-node1, vagrant-setup NODES=3 YES=1. make vagrant prints this help.

reload/restart/stop/destroy take optional VM names (dev-builder, dev-node1, …).
With no names they act on every machine already in .vagrant-dev.

The builder only starts Flynn so flynn-builder can compile images. The live
cluster is on cluster nodes (default FLYNN_DEV_NODES=1 → dev-node1).
FLYNN_DEV_NODES=0 is builder-only (no live cluster). setup/bootstrap need at
least one node. update applies flynn-host update --all-nodes --tarball --force
on the running nodes so you exercise the same path as production.

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
./flynn-logs/dev-builder and ./flynn-logs/dev-nodeN. Source tree and
ubuntu_ports_cache are shared with smoke; running VMs, host-only IPs, and
build outputs are not.
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

run_as_root_on() {
  local vm="$1"
  local cmd="$2"
  need_vagrant
  local state
  state="$(machine_state "${vm}")"
  if [[ "${state}" != "running" ]]; then
    echo "${vm} is not running (${state})" >&2
    exit 1
  fi
  vagrant ssh "${vm}" -c "sudo -n bash -lc $(printf '%q' "${cmd}")"
}

# restore_layers copies squashfs blobs from the newest local tarball into the
# layer cache. bootstrap-flynn reads /var/lib/flynn/layer-cache, and a release
# tarball does not leave those files there. Exit 2 means there is nothing to
# restore yet (setup/build will compile images instead of failing).
restore_layers() {
  run_as_root "cd ${SRC} && script/vagrant/guest/restore-layers.sh"
}

# build_images boots the builder if needed and runs flynn-builder. Does not
# require a bootstrapped cluster. Builder Flynn is compile-only.
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

# boot_dev_cluster creates/starts every machine this env lists (builder plus
# FLYNN_DEV_NODES extra hosts, default one).
boot_dev_cluster() {
  flynn_vagrant_up_or_create "$@"
}

boot_builder() {
  boot_dev_cluster "${DEV_MACHINE}"
}

require_cluster_nodes() {
  local n
  n="$(flynn_vagrant_get_nodes)"
  if [[ -z "${n}" || "${n}" -lt 1 ]]; then
    echo "no cluster nodes (FLYNN_DEV_NODES=${n:-0})." >&2
    echo "The builder only compiles Flynn. Set FLYNN_DEV_NODES=1 (default) so setup bootstraps dev-node1." >&2
    exit 1
  fi
}

live_cluster_up() {
  local ip domain code
  ip="$(flynn_vagrant_cluster_ip)" || return 1
  domain="$(flynn_vagrant_cluster_domain)"
  code="$(curl -sk --max-time 3 -o /dev/null -w '%{http_code}' \
    --resolve "controller.${domain}:443:${ip}" \
    "https://controller.${domain}/" || true)"
  if [[ "${code}" == "200" || "${code}" == "401" ]]; then
    return 0
  fi
  code="$(curl -s --max-time 3 -o /dev/null -w '%{http_code}' \
    --resolve "controller.${domain}:80:${ip}" \
    "http://controller.${domain}/" || true)"
  [[ "${code}" == "200" || "${code}" == "401" ]]
}

install_and_bootstrap_nodes() {
  local nodes=() name ip peers min_hosts domain cluster_ip
  while IFS= read -r name; do
    [[ -n "${name}" ]] || continue
    nodes+=("${name}")
  done < <(flynn_vagrant_running_cluster_nodes)
  if [[ ${#nodes[@]} -eq 0 ]]; then
    echo "no running cluster nodes. Boot them with script/vagrant.sh up (default dev-node1)." >&2
    exit 1
  fi
  peers="$(flynn_vagrant_peer_ips)"
  min_hosts="${#nodes[@]}"
  domain="$(flynn_vagrant_cluster_domain)"
  cluster_ip="$(flynn_vagrant_cluster_ip)"
  echo "installing Flynn on cluster nodes ${nodes[*]} (peer-ips=${peers})"
  for name in "${nodes[@]}"; do
    ip="$(flynn_vagrant_node_ip "${name}")"
    run_as_root_on "${name}" "cd ${SRC} && script/vagrant/guest/install-node.sh"
    run_as_root_on "${name}" "cd ${SRC} && CLUSTER_IP=${cluster_ip} CLUSTER_DOMAIN=${domain} script/vagrant/guest/node-dns.sh"
    run_as_root_on "${name}" "cd ${SRC} && PEER_IPS=${peers} EXTERNAL_IP=${ip} script/vagrant/guest/init-node.sh"
  done
  echo "bootstrapping Layer 1 on ${nodes[0]} (min-hosts=${min_hosts})"
  run_as_root_on "${nodes[0]}" "cd ${SRC} && CLUSTER_DOMAIN=${domain} MIN_HOSTS=${min_hosts} PEER_IPS=${peers} script/vagrant/guest/bootstrap-node.sh"
}

# ensure_live_cluster installs and bootstraps Flynn on cluster nodes. The
# builder is never the operator cluster.
ensure_live_cluster() {
  require_cluster_nodes
  if live_cluster_up; then
    echo "cluster already up on $(flynn_vagrant_cluster_ip)"
    return 0
  fi
  restore_or_build_layers
  install_and_bootstrap_nodes
}

# start_existing_cluster brings flynn-host back after a reboot. Cluster nodes
# use systemd (install-flynn). The builder is not started as a live cluster.
start_existing_cluster() {
  local name st=0 any=0
  while IFS= read -r name; do
    [[ -n "${name}" ]] || continue
    any=1
    set +e
    run_as_root_on "${name}" "cd ${SRC} && CLUSTER_DOMAIN=$(flynn_vagrant_cluster_domain) CLUSTER_IP=$(flynn_vagrant_cluster_ip) script/vagrant/guest/start-node.sh"
    st=$?
    set -e
    if [[ "${st}" -eq 2 ]]; then
      echo "${name}: cluster is not bootstrapped yet (script/vagrant.sh bootstrap)"
      return 0
    fi
    if [[ "${st}" -ne 0 ]]; then
      return "${st}"
    fi
  done < <(flynn_vagrant_running_cluster_nodes)
  if [[ "${any}" -eq 0 ]]; then
    echo "VMs reloaded; no running cluster nodes (the builder does not host the live cluster)"
    return 0
  fi
  return 0
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
  local creds pin key node domain ip
  require_cluster_nodes
  node="${FLYNN_VAGRANT_NODE_PREFIX}1"
  domain="$(flynn_vagrant_cluster_domain)"
  ip="$(flynn_vagrant_cluster_ip)"
  probe_live_cluster
  if [[ "${FLYNN_VAGRANT_SKIP_LAPTOP_CONNECT}" == "1" ]]; then
    echo "skipping laptop /etc/hosts and cluster:add (--skip-laptop-connect)"
    return 0
  fi
  creds="$(run_as_root_on "${node}" "cd ${SRC} && FLYNN_CLUSTER_IP=${ip} FLYNN_CLUSTER_DOMAIN=${domain} script/vagrant/guest/creds.sh")"
  parse_dev_creds "${creds}"
  if [[ -z "${pin}" || -z "${key}" ]]; then
    echo "could not read the cluster pin and key from ${node}" >&2
    exit 1
  fi
  CLUSTER_PIN="${pin}" CLUSTER_KEY="${key}" FLYNN_DEV_CLUSTER_IP="${ip}" FLYNN_DEV_DOMAIN="${domain}" FLYNN_VAGRANT_YES="${FLYNN_VAGRANT_YES}" "${ROOT}/script/vagrant/host/mac.sh"
}

probe_live_cluster() {
  local node domain ip min_hosts
  require_cluster_nodes
  node="${FLYNN_VAGRANT_NODE_PREFIX}1"
  domain="$(flynn_vagrant_cluster_domain)"
  ip="$(flynn_vagrant_cluster_ip)"
  min_hosts="$(flynn_vagrant_get_nodes)"
  echo "probing live cluster on ${node} (${ip}, min-hosts=${min_hosts})"
  run_as_root_on "${node}" "cd ${SRC} && CLUSTER_DOMAIN=${domain} CLUSTER_IP=${ip} MIN_HOSTS=${min_hosts} BUILDER_IP=${FLYNN_DEV_IP:-192.168.57.10} script/vagrant/guest/probe-cluster.sh"
}

update_running_cluster() {
  local nodes=() name
  while IFS= read -r name; do
    [[ -n "${name}" ]] || continue
    nodes+=("${name}")
  done < <(flynn_vagrant_running_cluster_nodes)
  if [[ ${#nodes[@]} -eq 0 ]]; then
    echo "no running cluster nodes to update." >&2
    echo "The builder is compile-only. Start the cluster with script/vagrant.sh up, then retry, or run script/vagrant.sh setup." >&2
    exit 1
  fi
  echo "updating live cluster on ${nodes[*]} (flynn-host update --all-nodes)"
  run_as_root_on "${nodes[0]}" "cd ${SRC} && script/vagrant/guest/update-cluster.sh"
  local domain ip
  domain="$(flynn_vagrant_cluster_domain)"
  ip="$(flynn_vagrant_cluster_ip)"
  for name in "${nodes[@]}"; do
    run_as_root_on "${name}" "cd ${SRC} && CLUSTER_IP=${ip} CLUSTER_DOMAIN=${domain} script/vagrant/guest/node-dns.sh"
    run_as_root_on "${name}" "cd ${SRC} && script/vagrant/guest/ensure-flynn-root.sh"
    run_as_root_on "${name}" "cd ${SRC} && script/vagrant/guest/ensure-qemu-binfmt.sh"
  done
}

cmd="${1:-}"
case "${cmd}" in
  -h|--help|help|"")
    usage
    ;;
  setup)
    boot_dev_cluster
    ensure_live_cluster
    connect_laptop
    ;;
  up)
    boot_dev_cluster "${@:2}"
    ;;
  status)
    flynn_vagrant_status
    ;;
  ssh)
    flynn_vagrant_ssh "${2:-${DEV_MACHINE}}"
    ;;
  build)
    build_images
    ;;
  cli)
    "${ROOT}/script/vagrant/host/cli.sh"
    ;;
  bootstrap)
    boot_dev_cluster
    require_cluster_nodes
    restore_or_build_layers
    install_and_bootstrap_nodes
    connect_laptop
    ;;
  update)
    run_as_root "cd ${SRC} && FLYNN_VAGRANT_FORCE_BUILD=${FLYNN_VAGRANT_FORCE_BUILD} script/vagrant/guest/update.sh"
    update_running_cluster
    probe_live_cluster
    ;;
  probe)
    probe_live_cluster
    ;;
  reload|restart)
    reload_dev_machines "${@:2}"
    start_existing_cluster
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
