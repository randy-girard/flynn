#!/bin/bash
# Smoke Vagrant env (.vagrant, builder + nodeN on 192.168.56.0/24). Lifecycle
# only: does not run the acceptance suite. The laptop loop is a different
# env (.vagrant-dev); these commands never touch it.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${ROOT}"

export FLYNN_VAGRANT_ENV=smoke
export VAGRANT_DOTFILE_PATH="${ROOT}/.vagrant"
SRC="/root/go/src/github.com/flynn/flynn"
BUILDER=builder
BUILDER_MEMORY="${BUILDER_MEMORY:-30000}"
BUILDER_CPUS="${BUILDER_CPUS:-8}"
NODE_MEMORY="${VAGRANT_NODE_MEMORY:-6144}"
NODE_CPUS="${VAGRANT_NODE_CPUS:-2}"
SMOKE_TARGETS=()

usage() {
  cat <<EOF
usage: script/vagrant-smoke.sh <status|ssh|up|reload|restart|stop|halt|destroy|teardown> [vm...]

  status     vagrant status of every VM in this env
  ssh        Shell on builder (or the named VM)
  up         Boot VMs already in .vagrant (does not create missing nodeN)
  reload     Reboot VMs (vagrant reload --no-provision)
  restart    Same as reload
  stop       Halt VMs (vagrant halt); disks and ./build stay
  halt       Same as stop
  destroy    Delete VMs (vagrant destroy -f); does not delete ./build
  teardown   Same as destroy

Optional VM names (builder, node1, …). With no names they act on every
machine already in .vagrant. This env is not the laptop loop
(script/vagrant-dev.sh, .vagrant-dev).

The acceptance suite is unchanged:

  script/vagrant-smoke.sh --item quick
EOF
}

need_vagrant() {
  command -v vagrant >/dev/null 2>&1 || {
    echo "vagrant is not installed" >&2
    exit 1
  }
}

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

# pin_smoke_nodes keeps nodeN in the Vagrantfile. Prefer FLYNN_MAX_NODES from
# the environment; otherwise use the highest node id already in .vagrant.
pin_smoke_nodes() {
  if [[ -n "${FLYNN_MAX_NODES:-}" ]]; then
    return
  fi
  local n=0 i
  local dir="${VAGRANT_DOTFILE_PATH}/machines"
  if [[ -d "${dir}" ]]; then
    for d in "${dir}"/node*; do
      [[ -d "${d}" ]] || continue
      i="$(basename "${d}")"
      i="${i#node}"
      if [[ "${i}" =~ ^[0-9]+$ ]] && [[ "${i}" -gt "${n}" ]]; then
        n="${i}"
      fi
    done
  fi
  if [[ "${n}" -lt 1 ]]; then
    n=1
  fi
  export FLYNN_MAX_NODES="${n}"
}

list_smoke_machines() {
  pin_smoke_nodes
  local name i
  if [[ -f "${VAGRANT_DOTFILE_PATH}/machines/${BUILDER}/virtualbox/id" ]]; then
    echo "${BUILDER}"
  fi
  for i in $(seq 1 "${FLYNN_MAX_NODES}"); do
    name="node${i}"
    if [[ -f "${VAGRANT_DOTFILE_PATH}/machines/${name}/virtualbox/id" ]]; then
      echo "${name}"
    fi
  done
}

define_named_smoke_nodes() {
  local name max=0 n
  pin_smoke_nodes
  for name in "$@"; do
    if [[ "${name}" =~ ^node([0-9]+)$ ]]; then
      n="${BASH_REMATCH[1]}"
      if [[ "${n}" -gt "${max}" ]]; then
        max="${n}"
      fi
    fi
  done
  if [[ "${max}" -gt "${FLYNN_MAX_NODES}" ]]; then
    export FLYNN_MAX_NODES="${max}"
  fi
}

collect_existing_smoke_machines() {
  SMOKE_TARGETS=()
  need_vagrant
  define_named_smoke_nodes "$@"
  local names=()
  local name state
  if [[ $# -eq 0 ]]; then
    while IFS= read -r name; do
      names+=("${name}")
    done < <(list_smoke_machines)
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
        echo "${name} is ${state}; this env is .vagrant, not the laptop loop's .vagrant-dev." >&2
        echo "Boot smoke with: script/vagrant-smoke.sh --item quick" >&2
        exit 1
        ;;
      *)
        SMOKE_TARGETS+=("${name}")
        ;;
    esac
  done
}

up_one() {
  local name="$1"
  if [[ "${name}" == "${BUILDER}" ]]; then
    echo "booting ${name} (${BUILDER_MEMORY} MB, ${BUILDER_CPUS} CPUs)"
    VAGRANT_MEMORY="${BUILDER_MEMORY}" VAGRANT_CPUS="${BUILDER_CPUS}" vagrant up "${name}"
  else
    echo "booting ${name} (${NODE_MEMORY} MB, ${NODE_CPUS} CPUs)"
    VAGRANT_MEMORY="${NODE_MEMORY}" VAGRANT_CPUS="${NODE_CPUS}" vagrant up "${name}"
  fi
}

reload_one() {
  local name="$1"
  if [[ "${name}" == "${BUILDER}" ]]; then
    echo "reloading ${name} (${BUILDER_MEMORY} MB, ${BUILDER_CPUS} CPUs)"
    VAGRANT_MEMORY="${BUILDER_MEMORY}" VAGRANT_CPUS="${BUILDER_CPUS}" vagrant reload --no-provision "${name}"
  else
    echo "reloading ${name} (${NODE_MEMORY} MB, ${NODE_CPUS} CPUs)"
    VAGRANT_MEMORY="${NODE_MEMORY}" VAGRANT_CPUS="${NODE_CPUS}" vagrant reload --no-provision "${name}"
  fi
}

up_smoke_machines() {
  local name state
  collect_existing_smoke_machines "$@"
  if [[ ${#SMOKE_TARGETS[@]} -eq 0 ]]; then
    echo "no VMs to start."
    echo "Create this env with: script/vagrant-smoke.sh --item quick" >&2
    return 0
  fi
  for name in "${SMOKE_TARGETS[@]}"; do
    state="$(machine_state "${name}")"
    case "${state}" in
      running)
        echo "${name} is already running"
        ;;
      poweroff|saved|aborted)
        up_one "${name}"
        ;;
      *)
        echo "skipping ${name} (${state})" >&2
        ;;
    esac
  done
}

reload_smoke_machines() {
  local name
  collect_existing_smoke_machines "$@"
  if [[ ${#SMOKE_TARGETS[@]} -eq 0 ]]; then
    echo "no VMs to reload." >&2
    echo "Create this env with: script/vagrant-smoke.sh --item quick" >&2
    exit 1
  fi
  for name in "${SMOKE_TARGETS[@]}"; do
    reload_one "${name}"
  done
}

stop_smoke_machines() {
  local name
  collect_existing_smoke_machines "$@"
  if [[ ${#SMOKE_TARGETS[@]} -eq 0 ]]; then
    echo "no VMs to stop."
    return 0
  fi
  for name in "${SMOKE_TARGETS[@]}"; do
    echo "stopping ${name}"
    vagrant halt "${name}"
  done
}

destroy_smoke_machines() {
  local name
  collect_existing_smoke_machines "$@"
  if [[ ${#SMOKE_TARGETS[@]} -eq 0 ]]; then
    echo "no VMs to destroy."
    return 0
  fi
  for name in "${SMOKE_TARGETS[@]}"; do
    echo "destroying ${name}"
    vagrant destroy -f "${name}"
  done
}

cmd="${1:-}"
case "${cmd}" in
  -h|--help|help|"")
    usage
    ;;
  status)
    need_vagrant
    pin_smoke_nodes
    vagrant status
    ;;
  ssh)
    need_vagrant
    pin_smoke_nodes
    local_vm="${2:-${BUILDER}}"
    exec vagrant ssh "${local_vm}" -- -t "sudo -n bash -lc 'cd ${SRC} && exec bash -l'"
    ;;
  up)
    up_smoke_machines "${@:2}"
    ;;
  reload|restart)
    reload_smoke_machines "${@:2}"
    ;;
  stop|halt)
    stop_smoke_machines "${@:2}"
    ;;
  destroy|teardown)
    destroy_smoke_machines "${@:2}"
    ;;
  *)
    usage >&2
    exit 1
    ;;
esac
