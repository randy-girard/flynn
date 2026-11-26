#!/bin/bash
# Post-update ssh steps of script/vagrant.sh update (EDGE-120), with a fake
# `vagrant` and `VBoxManage` on PATH. Does not boot a VM.
#
# After flynn-host update succeeds, `update` runs short idempotent steps on
# every node over `vagrant ssh` and probes the cluster. VirtualBox NAT port
# forwards can wedge under load or after a snapshot restore: ssh then exits
# 255 or hangs forever. Those steps must have a deadline, retry a wedged ssh
# a bounded number of times after toggling the VM's NAT link, still fail on a
# real step failure, and say plainly that the cluster update itself completed.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
script="${ROOT}/script/vagrant/vagrant.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
log="${tmp}/log"
mkdir -p "${tmp}/bin" "${tmp}/dot/machines"
for n in 1 2 3; do
  mkdir -p "${tmp}/dot/machines/dev-node${n}/virtualbox"
  echo "uuid-node${n}" > "${tmp}/dot/machines/dev-node${n}/virtualbox/id"
done

# Fake vagrant: `vagrant ssh <vm> -c <cmd>`. The action for each call is the
# first line of ${tmp}/act.<vm>.<guest-script> (ok when missing): ok, fail
# (exit 1), wedge (exit 255) or hang (sleeps far past any deadline).
cat > "${tmp}/bin/vagrant" <<'EOF'
#!/bin/bash
vm="$2"; cmd="$4"
s="$(printf '%s' "${cmd}" | grep -o 'guest/[a-z-]*\.sh' | head -1 | sed 's|guest/||')"
echo "ssh ${vm} ${s}" >> "${STUB_LOG}"
f="${STUB_DIR}/act.${vm}.${s}"
act=ok
if [[ -s "${f}" ]]; then
  act="$(head -1 "${f}")"
  tail -n +2 "${f}" > "${f}.n" && mv "${f}.n" "${f}"
fi
case "${act}" in
  ok) exit 0 ;;
  fail) exit 1 ;;
  wedge) exit 255 ;;
  hang) exec sleep 60 ;;
esac
EOF
cat > "${tmp}/bin/VBoxManage" <<'EOF'
#!/bin/bash
echo "vbox $*" >> "${STUB_LOG}"
EOF
chmod +x "${tmp}/bin/vagrant" "${tmp}/bin/VBoxManage"

fns=""
for f in run_as_root_on run_as_root_on_bounded vm_nat_reset probe_live_cluster wait_cluster_stable update_running_cluster; do
  body="$(sed -n "/^${f}() {/,/^}/p" "${script}")"
  if [[ -z "${body}" ]]; then
    echo "update-ssh: ${script} must define ${f}()" >&2
    exit 1
  fi
  fns+="${body}"$'\n'
done

run() {
  rm -f "${tmp}"/act.*
  : > "${log}"
  local spec
  for spec in "${ACTS[@]}"; do
    # spec: <vm>.<script>=<action> [<action>...]
    local key="${spec%%=*}" a
    for a in ${spec#*=}; do echo "${a}" >> "${tmp}/act.${key}"; done
  done
  set +e
  PATH="${tmp}/bin:${PATH}" STUB_LOG="${log}" STUB_DIR="${tmp}" VAGRANT_DOTFILE_PATH="${tmp}/dot" \
    FLYNN_VAGRANT_SSH_TIMEOUT="${FLYNN_VAGRANT_SSH_TIMEOUT:-2}" bash -c '
set -euo pipefail
FLYNN_VAGRANT_NODE_PREFIX=dev-node
SRC=/src
sleep() { :; }
need_vagrant() { :; }
machine_state() { echo running; }
require_cluster_nodes() { :; }
flynn_vagrant_get_nodes() { echo 3; }
flynn_vagrant_cluster_domain() { echo test.local; }
flynn_vagrant_cluster_ip() { echo 192.0.2.1; }
flynn_vagrant_running_cluster_nodes() { printf "%s\n" dev-node1 dev-node2 dev-node3; }
'"${fns}"'
"$@"
' _ "$@" > "${tmp}/out" 2>&1
  echo $? > "${tmp}/st"
  set -e
}

fail() {
  echo "update-ssh: $1" >&2
  echo "--- output (tail)" >&2
  tail -n 20 "${tmp}/out" >&2
  echo "--- calls (tail)" >&2
  tail -n 20 "${log}" >&2
  exit 1
}
st() { cat "${tmp}/st"; }
calls() { grep -c "^ssh $1 $2\$" "${log}" || true; }

# Healthy: every post-update step runs once on every node.
ACTS=()
run update_running_cluster
[[ "$(st)" -eq 0 ]] || fail "a clean update must pass"
for n in 1 2 3; do
  [[ "$(calls dev-node${n} node-dns.sh)" -eq 1 ]] || fail "node-dns.sh must run once on dev-node${n}"
done

# A wedged ssh (exit 255) on a post-update step is retried after toggling
# that VM's NAT link, and the update passes.
ACTS=("dev-node2.ensure-flynn-root.sh=wedge")
run update_running_cluster
[[ "$(st)" -eq 0 ]] || fail "one wedged post-update ssh must be retried, not fail the update"
[[ "$(calls dev-node2 ensure-flynn-root.sh)" -eq 2 ]] || fail "the wedged step must run again"
grep -Fxq "vbox controlvm uuid-node2 setlinkstate1 off" "${log}" \
  && grep -Fxq "vbox controlvm uuid-node2 setlinkstate1 on" "${log}" \
  || fail "a wedged ssh must toggle that VM's NAT link before the retry"

# A hung ssh is killed at the deadline and retried.
ACTS=("dev-node3.node-dns.sh=hang")
start=${SECONDS}
FLYNN_VAGRANT_SSH_TIMEOUT=1 run update_running_cluster
(( SECONDS - start < 30 )) || fail "a hung post-update ssh must be cut off at FLYNN_VAGRANT_SSH_TIMEOUT"
[[ "$(st)" -eq 0 ]] || fail "a hung post-update ssh must be retried"
[[ "$(calls dev-node3 node-dns.sh)" -eq 2 ]] || fail "the hung step must run again"

# Wedged on every try: bounded, non-zero, and the message says the cluster
# update itself completed and which harness step failed.
ACTS=("dev-node2.node-dns.sh=wedge wedge wedge wedge wedge")
FLYNN_VAGRANT_SSH_TRIES=3 run update_running_cluster
[[ "$(st)" -ne 0 ]] || fail "a post-update step that never gets through must fail the command"
[[ "$(calls dev-node2 node-dns.sh)" -eq 3 ]] || fail "a wedged step must be tried FLYNN_VAGRANT_SSH_TRIES times"
grep -q "cluster update completed" "${tmp}/out" || fail "the failure must say the cluster update completed"
grep -q "post-update step node-dns.sh on dev-node2" "${tmp}/out" || fail "the failure must name the step and VM"

# A real step failure (exit 1) is not retried or masked.
ACTS=("dev-node1.ensure-qemu-binfmt.sh=fail")
run update_running_cluster
[[ "$(st)" -ne 0 ]] || fail "a failed post-update step must fail the command"
[[ "$(calls dev-node1 ensure-qemu-binfmt.sh)" -eq 1 ]] || fail "a step that fails by itself must not be retried"

# flynn-host update failing is a cluster update failure, not a harness one.
ACTS=("dev-node1.update-cluster.sh=fail")
run update_running_cluster
[[ "$(st)" -ne 0 ]] || fail "a failed flynn-host update must fail the command"
grep -q "cluster update completed" "${tmp}/out" && fail "a failed flynn-host update must not be reported as completed"
[[ "$(calls dev-node1 node-dns.sh)" -eq 0 ]] || fail "post-update steps must not run after a failed update"

# The stable gate's probe has a deadline too: a hung probe counts as one
# failed try instead of blocking forever.
ACTS=("dev-node1.probe-cluster.sh=hang")
start=${SECONDS}
FLYNN_VAGRANT_PROBE_TIMEOUT=1 run wait_cluster_stable update 3 1
(( SECONDS - start < 30 )) || fail "a hung probe ssh must be cut off at the deadline"
[[ "$(st)" -eq 0 ]] || fail "the gate must pass once the probe answers"

echo "update-ssh ok"
