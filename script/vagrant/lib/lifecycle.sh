#!/bin/bash
# VM lifecycle parameterized by flynn_vagrant_use. Does not mix smoke and
# laptop-loop indexes: VAGRANT_DOTFILE_PATH is already set.

FLYNN_VAGRANT_TARGETS=()

flynn_vagrant_pin_nodes() {
  if [[ -n "$(flynn_vagrant_get_nodes)" ]]; then
    return
  fi
  local n=0 i
  local dir="${VAGRANT_DOTFILE_PATH}/machines"
  if [[ "${FLYNN_VAGRANT_PIN_STYLE}" == "consecutive" ]]; then
    for i in $(seq 1 32); do
      if [[ -f "${dir}/${FLYNN_VAGRANT_NODE_PREFIX}${i}/virtualbox/id" ]]; then
        n=$i
      else
        break
      fi
    done
  elif [[ -d "${dir}" ]]; then
    for d in "${dir}/${FLYNN_VAGRANT_NODE_PREFIX}"*; do
      [[ -d "${d}" ]] || continue
      i="$(basename "${d}")"
      i="${i#${FLYNN_VAGRANT_NODE_PREFIX}}"
      if [[ "${i}" =~ ^[0-9]+$ ]] && [[ "${i}" -gt "${n}" ]]; then
        n="${i}"
      fi
    done
  fi
  if [[ "${n}" -lt "${FLYNN_VAGRANT_PIN_MIN}" ]]; then
    n="${FLYNN_VAGRANT_PIN_MIN}"
  fi
  flynn_vagrant_set_nodes "${n}"
}

flynn_vagrant_list_machines() {
  flynn_vagrant_pin_nodes
  local i name
  if [[ "${FLYNN_VAGRANT_LIST_CREATED_ONLY}" -eq 1 ]]; then
    if [[ -f "${VAGRANT_DOTFILE_PATH}/machines/${FLYNN_VAGRANT_BUILDER}/virtualbox/id" ]]; then
      echo "${FLYNN_VAGRANT_BUILDER}"
    fi
    for i in $(seq 1 "$(flynn_vagrant_get_nodes)"); do
      name="${FLYNN_VAGRANT_NODE_PREFIX}${i}"
      if [[ -f "${VAGRANT_DOTFILE_PATH}/machines/${name}/virtualbox/id" ]]; then
        echo "${name}"
      fi
    done
    return
  fi
  echo "${FLYNN_VAGRANT_BUILDER}"
  for i in $(seq 1 "$(flynn_vagrant_get_nodes)"); do
    echo "${FLYNN_VAGRANT_NODE_PREFIX}${i}"
  done
}

# flynn_vagrant_define_named_nodes raises the node count so a requested extra
# host exists in this Vagrantfile (otherwise vagrant reload cannot see it).
flynn_vagrant_define_named_nodes() {
  local name max=0 n
  flynn_vagrant_pin_nodes
  for name in "$@"; do
    if [[ "${name}" =~ ^${FLYNN_VAGRANT_NODE_PREFIX}([0-9]+)$ ]]; then
      n="${BASH_REMATCH[1]}"
      if [[ "${n}" -gt "${max}" ]]; then
        max="${n}"
      fi
    fi
  done
  local cur
  cur="$(flynn_vagrant_get_nodes)"
  if [[ "${max}" -gt "${cur}" ]]; then
    flynn_vagrant_set_nodes "${max}"
  fi
}

# flynn_vagrant_collect_existing lists VMs that exist in this env (skips
# not_created and unknown). Optional args limit the list.
flynn_vagrant_collect_existing() {
  FLYNN_VAGRANT_TARGETS=()
  need_vagrant
  flynn_vagrant_define_named_nodes "$@"
  local names=()
  local name state
  if [[ $# -eq 0 ]]; then
    while IFS= read -r name; do
      names+=("${name}")
    done < <(flynn_vagrant_list_machines)
  else
    names=("$@")
  fi
  for name in "${names[@]}"; do
    state="$(machine_state "${name}")"
    case "${state}" in
      not_created|unknown)
        echo "skipping ${name} (${state})" >&2
        ;;
      *)
        FLYNN_VAGRANT_TARGETS+=("${name}")
        ;;
    esac
  done
}

flynn_vagrant_up_one() {
  local name="$1"
  if [[ "${name}" == "${FLYNN_VAGRANT_BUILDER}" ]]; then
    echo "booting ${name} (${BUILDER_MEMORY} MB, ${BUILDER_CPUS} CPUs)"
    VAGRANT_MEMORY="${BUILDER_MEMORY}" VAGRANT_CPUS="${BUILDER_CPUS}" vagrant up "${name}"
  else
    echo "booting ${name} (${NODE_MEMORY} MB, ${NODE_CPUS} CPUs)"
    VAGRANT_MEMORY="${NODE_MEMORY}" VAGRANT_CPUS="${NODE_CPUS}" vagrant up "${name}"
  fi
}

flynn_vagrant_reload_one() {
  local name="$1"
  if [[ "${name}" == "${FLYNN_VAGRANT_BUILDER}" ]]; then
    echo "reloading ${name} (${BUILDER_MEMORY} MB, ${BUILDER_CPUS} CPUs)"
    VAGRANT_MEMORY="${BUILDER_MEMORY}" VAGRANT_CPUS="${BUILDER_CPUS}" vagrant reload --no-provision "${name}"
  else
    echo "reloading ${name} (${NODE_MEMORY} MB, ${NODE_CPUS} CPUs)"
    VAGRANT_MEMORY="${NODE_MEMORY}" VAGRANT_CPUS="${NODE_CPUS}" vagrant reload --no-provision "${name}"
  fi
}

# flynn_vagrant_up_or_create boots every machine this env lists, creating
# missing VMs (laptop setup/up). Unlike flynn_vagrant_up_existing, not_created
# is a create, not a skip.
flynn_vagrant_up_or_create() {
  need_vagrant
  flynn_vagrant_define_named_nodes "$@"
  local names=() name state
  if [[ $# -eq 0 ]]; then
    while IFS= read -r name; do
      names+=("${name}")
    done < <(flynn_vagrant_list_machines)
  else
    names=("$@")
  fi
  if [[ ${#names[@]} -eq 0 ]]; then
    echo "no VMs to start."
    echo "Create this env with: ${FLYNN_VAGRANT_EMPTY_HINT}" >&2
    return 0
  fi
  for name in "${names[@]}"; do
    state="$(machine_state "${name}")"
    case "${state}" in
      running)
        echo "${name} is already running"
        ;;
      *)
        flynn_vagrant_up_one "${name}"
        ;;
    esac
  done
}

# flynn_vagrant_up_existing boots VMs already in this env. It does not create
# missing machines (that would look like a smoke `vagrant up`).
flynn_vagrant_up_existing() {
  local name state
  flynn_vagrant_collect_existing "$@"
  if [[ ${#FLYNN_VAGRANT_TARGETS[@]} -eq 0 ]]; then
    echo "no VMs to start."
    echo "Create this env with: ${FLYNN_VAGRANT_EMPTY_HINT}" >&2
    return 0
  fi
  for name in "${FLYNN_VAGRANT_TARGETS[@]}"; do
    state="$(machine_state "${name}")"
    case "${state}" in
      running)
        echo "${name} is already running"
        ;;
      poweroff|saved|aborted)
        flynn_vagrant_up_one "${name}"
        ;;
      *)
        echo "skipping ${name} (${state})" >&2
        ;;
    esac
  done
}

flynn_vagrant_reload() {
  flynn_vagrant_collect_existing "$@"
  if [[ ${#FLYNN_VAGRANT_TARGETS[@]} -eq 0 ]]; then
    echo "no VMs to reload." >&2
    echo "Create this env with: ${FLYNN_VAGRANT_EMPTY_HINT}" >&2
    exit 1
  fi
  local name
  for name in "${FLYNN_VAGRANT_TARGETS[@]}"; do
    flynn_vagrant_reload_one "${name}"
  done
}

# flynn_vagrant_stop powers off VMs already in this env. Disks stay.
flynn_vagrant_stop() {
  local name
  flynn_vagrant_collect_existing "$@"
  if [[ ${#FLYNN_VAGRANT_TARGETS[@]} -eq 0 ]]; then
    echo "no VMs to stop."
    return 0
  fi
  for name in "${FLYNN_VAGRANT_TARGETS[@]}"; do
    echo "stopping ${name}"
    vagrant halt "${name}"
  done
}

# flynn_vagrant_destroy deletes VMs in this env. Host build dirs stay.
flynn_vagrant_destroy() {
  local name
  flynn_vagrant_collect_existing "$@"
  if [[ ${#FLYNN_VAGRANT_TARGETS[@]} -eq 0 ]]; then
    echo "no VMs to destroy."
    return 0
  fi
  for name in "${FLYNN_VAGRANT_TARGETS[@]}"; do
    echo "destroying ${name}"
    vagrant destroy -f "${name}"
  done
}

flynn_vagrant_status() {
  need_vagrant
  flynn_vagrant_pin_nodes
  vagrant status
}

flynn_vagrant_ssh() {
  need_vagrant
  flynn_vagrant_pin_nodes
  local vm="${1:-${FLYNN_VAGRANT_BUILDER}}"
  exec vagrant ssh "${vm}" -- -t "sudo -n bash -lc 'cd ${SRC} && exec bash -l'"
}
