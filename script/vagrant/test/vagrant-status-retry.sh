#!/bin/bash
# run_as_root_on in script/vagrant/vagrant.sh with a stubbed vagrant. Does not
# boot a VM.
#
# Right after `vagrant up` a single `vagrant status` can fail (VirtualBox busy,
# the machine lock), and machine_state then says "unknown". A chaos
# node-reboot round ran the next command on that node and the whole run
# aborted with "dev-node3 is not running (unknown)" although the VM was
# running (EDGE-102). run_as_root_on must ask again a few times before it
# gives up on an unknown state, and print vagrant's own error when it does.
# A clear answer (poweroff) still fails at once.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
script="${ROOT}/script/vagrant/vagrant.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
fail=0

fn="$(sed -n '/^run_as_root_on() {/,/^}/p' "${script}")"
[[ -n "${fn}" ]] || { echo "vagrant-status-retry: ${script} must define run_as_root_on()" >&2; exit 1; }

# run <name> <answers>: answers is a space list, one per `vagrant status`
# call: "fail" (exit 1 with an error), "empty" (no state line), or a state.
run() {
  local name="$1" answers="$2"
  : > "${tmp}/${name}.calls"
  set +e
  ANSWERS="${answers}" CALLS="${tmp}/${name}.calls" bash -c '
set -euo pipefail
source "$1"
eval "$2"
need_vagrant() { :; }
sleep() { echo "sleep $1" >> "${CALLS}"; }
vagrant() {
  case "$1" in
    status)
      local n answer
      n=$(grep -c "^status" "${CALLS}" || true)
      echo "status" >> "${CALLS}"
      read -r -a a <<< "${ANSWERS}"
      answer="${a[$n]:-${a[${#a[@]}-1]}}"
      case "${answer}" in
        fail) echo "The machine index which stores all required information about running Vagrant environments is locked" >&2; return 1 ;;
        empty) return 0 ;;
        *) echo "1700000000,dev-node3,state,${answer}" ;;
      esac
      ;;
    ssh) echo "ssh $2" >> "${CALLS}"; echo "ran on $2" ;;
  esac
}
run_as_root_on dev-node3 "true"
' _ "${ROOT}/script/vagrant/lib/common.sh" "${fn}" > "${tmp}/${name}.out" 2>&1
  echo $? > "${tmp}/${name}.st"
  set -e
}
check() {
  local name="$1" what="$2"
  shift 2
  if ! "$@"; then
    echo "vagrant-status-retry ${name}: ${what}" >&2
    cat "${tmp}/${name}.out" "${tmp}/${name}.calls" >&2
    fail=1
  fi
}

run once "fail running"
check once "one failed vagrant status must not stop run_as_root_on" [ "$(cat "${tmp}/once.st")" -eq 0 ]
check once "the command must run after the retry" grep -q '^ssh dev-node3$' "${tmp}/once.calls"
check once "the retry must wait briefly" grep -q '^sleep ' "${tmp}/once.calls"

run twice "empty fail running"
check twice "two bad answers must still be retried" [ "$(cat "${tmp}/twice.st")" -eq 0 ]

run never "fail"
check never "a status that keeps failing must fail" [ "$(cat "${tmp}/never.st")" -ne 0 ]
check never "it must not run the command" bash -c '! grep -q "^ssh" "$1"' _ "${tmp}/never.calls"
check never "it must ask a bounded number of times (3-6)" bash -c 'n=$(grep -c "^status" "$1"); [ "$n" -ge 3 ] && [ "$n" -le 6 ]' _ "${tmp}/never.calls"
check never "it must say the node is not running" grep -Fq "dev-node3 is not running (unknown)" "${tmp}/never.out"
check never "it must print vagrant's own error" grep -Fq "machine index which stores all required information" "${tmp}/never.out"

run off "poweroff running"
check off "a clear poweroff must fail at once" [ "$(cat "${tmp}/off.st")" -ne 0 ]
check off "poweroff must not be retried" [ "$(grep -c '^status' "${tmp}/off.calls")" -eq 1 ]
check off "poweroff must be reported" grep -Fq "dev-node3 is not running (poweroff)" "${tmp}/off.out"

run up "running"
check up "a running node must run the command with one status call" [ "$(grep -c '^status' "${tmp}/up.calls")" -eq 1 ]

if [[ "${fail}" -ne 0 ]]; then
  exit 1
fi
echo "vagrant-status-retry: ok"
