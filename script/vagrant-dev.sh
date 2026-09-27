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

DEV_MEMORY="${VAGRANT_DEV_MEMORY:-30000}"
DEV_CPUS="${VAGRANT_DEV_CPUS:-4}"
SRC="/root/go/src/github.com/flynn/flynn"

usage() {
  cat <<EOF
usage: script/vagrant-dev.sh <setup|up|status|ssh|build|cli|bootstrap|update|reload|restart|stop|halt|destroy|teardown> [vm...]

  setup      Boot dev-builder, start the cluster, and connect this laptop
  up         Boot only dev-builder (${DEV_MEMORY} MB, ${DEV_CPUS} CPUs unless overridden)
  status     vagrant status of every VM in this env
  ssh        Shell on dev-builder
  build      Build cluster images on dev-builder (includes the Ubuntu base layer the first time)
  cli        Build the laptop flynn CLI and install it to /usr/local/bin
  bootstrap  First cluster on dev-builder (script/bootstrap-flynn)
  update     flynn-host update from the newest build/release tarball
  reload     Reboot VMs (vagrant reload --no-provision) and start the cluster
  restart    Same as reload
  stop       Halt VMs (vagrant halt); disks and ./build-dev stay
  halt       Same as stop
  destroy    Delete VMs (vagrant destroy -f); does not delete ./build-dev
  teardown   Same as destroy

reload/restart/stop/destroy take optional VM names (dev-builder, dev-node1, …).
With no names they act on every machine already in .vagrant-dev.

setup is the one-time command. After it finishes:

  flynn -c local apps
  open https://status.1.localflynn.com

This VM is not the smoke builder. Smoke uses .vagrant and 192.168.56.0/24.
Dev uses .vagrant-dev, dev-builder, and ${FLYNN_DEV_IP}. Extra hosts are
FLYNN_DEV_NODES=N (dev-node1..N on 192.168.57.(19+N)).

Override size with VAGRANT_DEV_MEMORY and VAGRANT_DEV_CPUS.
Image builds (script/vagrant-dev.sh build) need RAM: flynn-builder has been
OOM-killed at ~11GB RSS on a 12GB VM. Default is 30000 MB to match the smoke
builder. GOMEMLIMIT in script/flynn-builder is also capped to guest RAM.
The repo is mounted at ${SRC}. Flynn artifacts (binaries, images, tarballs)
sync to ./build-dev so they do not overwrite smoke's ./build. Sibling plugin
checkouts are mounted under /opt/flynn-plugins. Logs sync to
./flynn-logs/dev-builder.
EOF
}

need_vagrant() {
  command -v vagrant >/dev/null 2>&1 || {
    echo "vagrant is not installed" >&2
    exit 1
  }
}

# machine_state is running, poweroff, saved, not_created, or unknown.
# Smoke's machines live in .vagrant and do not count.
machine_state() {
  local m="$1"
  local line
  line="$(vagrant status --machine-readable "${m}" 2>/dev/null | awk -F, -v m="${m}" '$2==m && $3=="state" {print $4; exit}')"
  if [[ -z "${line}" ]]; then
    echo "unknown"
    return
  fi
  echo "${line}"
}

dev_vm_state() {
  machine_state "${DEV_MACHINE}"
}

# pin_dev_nodes keeps extra hosts in the Vagrantfile. Prefer FLYNN_DEV_NODES
# from the environment; otherwise count id files already in .vagrant-dev.
pin_dev_nodes() {
  if [[ -n "${FLYNN_DEV_NODES:-}" ]]; then
    return
  fi
  local n=0 i
  for i in $(seq 1 32); do
    if [[ -f "${VAGRANT_DOTFILE_PATH}/machines/dev-node${i}/virtualbox/id" ]]; then
      n=$i
    else
      break
    fi
  done
  export FLYNN_DEV_NODES="${n}"
}

list_dev_machines() {
  pin_dev_nodes
  local names=("${DEV_MACHINE}")
  local i
  for i in $(seq 1 "${FLYNN_DEV_NODES}"); do
    names+=("dev-node${i}")
  done
  printf '%s\n' "${names[@]}"
}

# define_named_nodes raises FLYNN_DEV_NODES so a requested extra host exists
# in this Vagrantfile (otherwise vagrant reload cannot see it).
define_named_nodes() {
  local name max=0 n
  pin_dev_nodes
  for name in "$@"; do
    if [[ "${name}" =~ ^dev-node([0-9]+)$ ]]; then
      n="${BASH_REMATCH[1]}"
      if [[ "${n}" -gt "${max}" ]]; then
        max="${n}"
      fi
    fi
  done
  if [[ "${max}" -gt "${FLYNN_DEV_NODES}" ]]; then
    export FLYNN_DEV_NODES="${max}"
  fi
}

# DEV_TARGETS is filled by collect_existing_dev_machines (bash 3.2 has no nameref).
DEV_TARGETS=()

# collect_existing_dev_machines lists VMs that exist in this env (skips
# not_created). Optional args limit the list. Unknown means the Vagrantfile
# does not define that machine in .vagrant-dev.
collect_existing_dev_machines() {
  DEV_TARGETS=()
  need_vagrant
  define_named_nodes "$@"
  local names=()
  local name state
  if [[ $# -eq 0 ]]; then
    while IFS= read -r name; do
      names+=("${name}")
    done < <(list_dev_machines)
  else
    names=("$@")
  fi
  for name in "${names[@]}"; do
    state="$(machine_state "${name}")"
    case "${state}" in
      not_created)
        echo "skipping ${name} (${state})" >&2
        ;;
      unknown)
        echo "${name} is ${state}; this env is .vagrant-dev, not smoke's .vagrant." >&2
        echo "Create this one with: script/vagrant-dev.sh up" >&2
        exit 1
        ;;
      *)
        DEV_TARGETS+=("${name}")
        ;;
    esac
  done
}

# stop_dev_machines powers off VMs already in this env. Disks stay.
stop_dev_machines() {
  local name
  collect_existing_dev_machines "$@"
  if [[ ${#DEV_TARGETS[@]} -eq 0 ]]; then
    echo "no VMs to stop."
    return 0
  fi
  for name in "${DEV_TARGETS[@]}"; do
    echo "stopping ${name}"
    vagrant halt "${name}"
  done
}

# destroy_dev_machines deletes VMs in this env. ./build-dev and flynn-logs stay.
destroy_dev_machines() {
  local name
  collect_existing_dev_machines "$@"
  if [[ ${#DEV_TARGETS[@]} -eq 0 ]]; then
    echo "no VMs to destroy."
    return 0
  fi
  for name in "${DEV_TARGETS[@]}"; do
    echo "destroying ${name}"
    vagrant destroy -f "${name}"
  done
}

reload_one() {
  local name="$1"
  if [[ "${name}" == "${DEV_MACHINE}" ]]; then
    echo "reloading ${name} (${DEV_MEMORY} MB, ${DEV_CPUS} CPUs)"
    VAGRANT_MEMORY="${DEV_MEMORY}" VAGRANT_CPUS="${DEV_CPUS}" vagrant reload --no-provision "${name}"
  else
    echo "reloading ${name}"
    vagrant reload --no-provision "${name}"
  fi
}

# reload_dev_machines reboots VMs already in this env. It does not create
# missing machines (that would look like a smoke `vagrant up`).
reload_dev_machines() {
  need_vagrant
  define_named_nodes "$@"
  local names=()
  local name state
  if [[ $# -eq 0 ]]; then
    while IFS= read -r name; do
      names+=("${name}")
    done < <(list_dev_machines)
  else
    names=("$@")
  fi
  local to_reload=()
  for name in "${names[@]}"; do
    state="$(machine_state "${name}")"
    case "${state}" in
      running|poweroff|saved|aborted)
        to_reload+=("${name}")
        ;;
      not_created)
        echo "skipping ${name} (${state})" >&2
        ;;
      *)
        echo "${name} is ${state}; cannot reload" >&2
        echo "Smoke's machines in .vagrant are a different env and are not used here." >&2
        echo "Create this one with: script/vagrant-dev.sh up" >&2
        exit 1
        ;;
    esac
  done
  if [[ ${#to_reload[@]} -eq 0 ]]; then
    echo "no VMs to reload." >&2
    echo "Create this env with: script/vagrant-dev.sh up" >&2
    echo "First cluster: script/vagrant-dev.sh setup" >&2
    exit 1
  fi
  for name in "${to_reload[@]}"; do
    reload_one "${name}"
  done
}

# start_existing_cluster brings flynn-host back after a reboot. Nested
# bootstrap on the builder does not install a systemd unit, so a VM reload
# leaves the daemon down. Do not bootstrap a new cluster from here.
start_existing_cluster() {
  set +e
  run_as_root "cd ${SRC} && script/vagrant-dev-ensure-cluster.sh"
  local st=$?
  set -e
  if [[ "${st}" -eq 2 ]]; then
    echo "VMs reloaded; cluster is not bootstrapped yet (script/vagrant-dev.sh bootstrap)"
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
      echo "Create this one with: script/vagrant-dev.sh up" >&2
      echo "First cluster: script/vagrant-dev.sh setup" >&2
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
    pin_dev_nodes
    vagrant status
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
