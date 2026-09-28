#!/bin/bash
# Smoke Vagrant env (.vagrant, builder + nodeN on 192.168.56.0/24). Lifecycle
# only: does not run the acceptance suite. The laptop loop is a different
# env (.vagrant-dev); these commands never touch it.
set -euo pipefail

VAGRANT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${VAGRANT_DIR}/lib/common.sh"
# shellcheck source=lib/env.sh
source "${VAGRANT_DIR}/lib/env.sh"
# shellcheck source=lib/lifecycle.sh
source "${VAGRANT_DIR}/lib/lifecycle.sh"

cd "${ROOT}"
flynn_vagrant_use smoke

BUILDER="${FLYNN_VAGRANT_BUILDER}"

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

Make (from the repo root): make vagrant-smoke-status, vagrant-smoke-ssh,
vagrant-smoke-up, vagrant-smoke-reload, vagrant-smoke-stop,
vagrant-smoke-destroy. make vagrant-smoke runs --item quick.

Optional VM names (builder, node1, …). With no names they act on every
machine already in .vagrant. This env is not the laptop loop
(script/vagrant.sh, .vagrant-dev).

The acceptance suite is unchanged:

  script/vagrant-smoke.sh --item quick
  script/vagrant-smoke.sh --item datastores
EOF
}

cmd="${1:-}"
case "${cmd}" in
  -h|--help|help|"")
    usage
    ;;
  status)
    flynn_vagrant_status
    ;;
  ssh)
    flynn_vagrant_ssh "${2:-${BUILDER}}"
    ;;
  up)
    flynn_vagrant_up_existing "${@:2}"
    ;;
  reload|restart)
    flynn_vagrant_reload "${@:2}"
    ;;
  stop|halt)
    flynn_vagrant_stop "${@:2}"
    ;;
  destroy|teardown)
    flynn_vagrant_destroy "${@:2}"
    ;;
  *)
    usage >&2
    exit 1
    ;;
esac
