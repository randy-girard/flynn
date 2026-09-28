#!/bin/bash
# Contract for the laptop Vagrant loop. Does not boot a VM.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
entry="${ROOT}/script/vagrant.sh"
mod="${ROOT}/script/vagrant"
script="${mod}/vagrant.sh"

in_mod() {
  grep -RFq "$1" "${mod}"
}

if [[ ! -x "${entry}" || ! -x "${script}" ]]; then
  echo "script/vagrant.sh and script/vagrant/vagrant.sh must exist" >&2
  exit 1
fi
# bash -n catches an unmatched quote at parse time (unexpected EOF looking for
# matching "). A run that overlapped an edit of this file used to fail that way.
if ! bash -n "${entry}" || ! bash -n "${script}"; then
  echo "script/vagrant.sh failed bash -n (unmatched quote or similar parse error)" >&2
  exit 1
fi
if ! grep -Fq 'vagrant/vagrant.sh' "${entry}"; then
  echo "script/vagrant.sh must exec script/vagrant/vagrant.sh" >&2
  exit 1
fi

for want in \
  "flynn_vagrant_use dev" \
  ".vagrant-dev" \
  "dev-builder" \
  "ensure_dev_vm" \
  "sudo -n bash -lc" \
  "script/bootstrap-flynn" \
  "guest/restore-layers.sh" \
  "guest/build-images.sh" \
  "restore_or_build_layers" \
  "build_images" \
  "host/mac.sh" \
  "host/cli.sh" \
  "guest/update.sh" \
  "build-dev" \
  "GOMEMLIMIT" \
  "setup)"; do
  if ! grep -Fq "${want}" "${script}"; then
    echo "script/vagrant/vagrant.sh missing ${want}" >&2
    exit 1
  fi
done
if ! grep -Fq 'FLYNN_VAGRANT_ENV=dev' "${mod}/lib/env.sh"; then
  echo "lib/env.sh must set FLYNN_VAGRANT_ENV=dev for the laptop loop" >&2
  exit 1
fi
if grep -Fq 'vagrant up builder' "${script}"; then
  echo "vagrant.sh must not boot the smoke builder" >&2
  exit 1
fi
if ! grep -Fq "flynn-host update" "${mod}/guest/update.sh"; then
  echo "guest/update.sh must run flynn-host update" >&2
  exit 1
fi
if ! grep -Fq 'build-dev/bin' "${mod}/host/cli.sh"; then
  echo "host/cli.sh must write the laptop CLI into build-dev, not smoke's build/" >&2
  exit 1
fi
if grep -Eq 'vagrant up node[0-9]' "${script}"; then
  echo "vagrant.sh must not boot smoke cluster nodes (node1); extra hosts are dev-nodeN" >&2
  exit 1
fi
if ! grep -Fq 'flynn_vagrant_up_or_create' "${script}"; then
  echo "setup/up must create the default laptop machines (builder + node1)" >&2
  exit 1
fi
if ! grep -Fq 'boot_dev_cluster' "${script}"; then
  echo "vagrant.sh must boot the default laptop cluster, not only the builder" >&2
  exit 1
fi
if ! in_mod 'vagrant reload --no-provision'; then
  echo "reload must use vagrant reload --no-provision" >&2
  exit 1
fi
if ! grep -Fq 'reload|restart)' "${script}"; then
  echo "vagrant.sh must accept reload and restart" >&2
  exit 1
fi
if ! grep -Fq 'FLYNN_VAGRANT_NODE_PREFIX=dev-node' "${mod}/lib/env.sh"; then
  echo "laptop loop must discover extra hosts as dev-nodeN from .vagrant-dev" >&2
  exit 1
fi
if ! grep -Fq 'export FLYNN_DEV_NODES=1' "${mod}/lib/env.sh"; then
  echo "laptop loop must default FLYNN_DEV_NODES=1 (dev-builder + dev-node1)" >&2
  exit 1
fi
if ! grep -Fq 'not_created|unknown' "${mod}/lib/lifecycle.sh"; then
  echo "destroy/stop must skip unknown machines instead of exiting 1" >&2
  exit 1
fi
if grep -Fq 'flynn_vagrant_unknown_machine' "${mod}/lib/lifecycle.sh"; then
  echo "destroy must not treat unknown as a hard error" >&2
  exit 1
fi
if ! grep -Fq 'start_existing_cluster' "${script}"; then
  echo "vagrant.sh reload must start an existing cluster without bootstrapping" >&2
  exit 1
fi
if ! in_mod 'vagrant halt'; then
  echo "stop must use vagrant halt" >&2
  exit 1
fi
if ! in_mod 'vagrant destroy -f'; then
  echo "destroy must use vagrant destroy -f" >&2
  exit 1
fi
if ! grep -Fq 'stop|halt)' "${script}"; then
  echo "vagrant.sh must accept stop and halt" >&2
  exit 1
fi
if ! grep -Fq 'destroy|teardown)' "${script}"; then
  echo "vagrant.sh must accept destroy and teardown" >&2
  exit 1
fi
if ! grep -q 'flynn_path_is_mount' "${ROOT}/script/clean-flynn"; then
  echo "clean-flynn must not rm -rf guest build/ when it is the build-dev mount" >&2
  exit 1
fi
if grep -Fq 'cp -n' "${mod}/guest/restore-layers.sh"; then
  echo "restore-layers must not use cp -n (GNU coreutils on Ubuntu 24.04 warns it is non-portable)" >&2
  exit 1
fi
if ! grep -Fq 'if [[ ! -e "${dest}" ]]' "${mod}/guest/restore-layers.sh"; then
  echo "restore-layers must skip existing layer blobs without GNU cp -n" >&2
  exit 1
fi
if ! grep -Fq 'sudo mkdir -p /etc/flynn' "${ROOT}/script/start-flynn-host"; then
  echo "start-flynn-host must mkdir /etc/flynn before flynn-host so configure-host-auth can write host.json" >&2
  exit 1
fi
if ! grep -Fq 'MkdirAll' "${ROOT}/host/config/config.go"; then
  echo "host config WriteTo must MkdirAll the host.json parent dir" >&2
  exit 1
fi
if ! grep -Fq 'write %s: %s' "${ROOT}/host/http.go"; then
  echo "ConfigureAuthKey must return the host.json write error instead of unknown_error" >&2
  exit 1
fi
if ! grep -Fq 'INVOCATION_ID' "${ROOT}/host/http.go"; then
  echo "ConfigureAuthKey must detect systemd via INVOCATION_ID so vagrant-dev (start-stop-daemon) applies auth in-process" >&2
  exit 1
fi
if ! grep -Fq 'enableAuthInProcess' "${ROOT}/host/http.go"; then
  echo "ConfigureAuthKey must apply the host auth key in-process when flynn-host is not a systemd unit" >&2
  exit 1
fi
if ! grep -Fq 'start-stop-daemon' "${ROOT}/script/start-flynn-host"; then
  echo "start-flynn-host must keep start-stop-daemon (vagrant-dev is not systemd flynn-host)" >&2
  exit 1
fi
if ! grep -Fq 'FLANNEL_BACKEND="alloc"' "${ROOT}/script/bootstrap-flynn"; then
  echo "bootstrap-flynn must use the alloc flannel backend on the laptop loop (no vxlan device)" >&2
  exit 1
fi
if grep -Fq 'Lease().Network.IP.String()' "${ROOT}/flannel/main.go"; then
  echo "flannel HTTP must not bind the CIDR network address; alloc has no IP on .0 (EADDRNOTAVAIL)" >&2
  exit 1
fi
if ! grep -Fq 'FirstUsable()' "${ROOT}/flannel/main.go"; then
  echo "flannel HTTP must listen on the first usable overlay IP (bridge .1) under FLANNEL_BACKEND=alloc" >&2
  exit 1
fi
if ! grep -Fq 'cli-add-command' "${mod}/guest/creds.sh"; then
  echo "guest/creds.sh must read AUTH_KEY from the running controller job, not leftover host.json" >&2
  exit 1
fi
if ! grep -Fq 'FLYNN_DEV_PIN=' "${mod}/guest/creds.sh"; then
  echo "guest/creds.sh must print labeled FLYNN_DEV_PIN so ssh banners cannot steal line 1" >&2
  exit 1
fi
if ! grep -Fq 'rejected cluster key' "${ROOT}/cli/cluster.go"; then
  echo "cluster:add must fail on GET /apps 401 so setup does not store a leftover host.json key" >&2
  exit 1
fi
sample_add=$'Install the Flynn CLI\n\nflynn cluster:add -p KGCENkp53YF5OvOKkZIry71+czFRkSw2ZdMszZ/0ljs= default 1.localflynn.com e09dc5301d72be755a3d666f617c4600\n'
python3 - <<PY
import re
text = """${sample_add}"""
m = re.search(r"flynn cluster:add -p (\S+) \S+ \S+ (\S+)\s*$", text, re.M)
if not m or m.group(2) != "e09dc5301d72be755a3d666f617c4600":
    raise SystemExit("cli-add-command parse failed")
PY
if ! grep -Fq 'parse_dev_creds' "${script}"; then
  echo "vagrant.sh must parse labeled creds, not ssh stdout line 1/2" >&2
  exit 1
fi
if ! grep -Fq 'CAClient' "${ROOT}/cli/cluster.go"; then
  echo "cluster:add must fetch /ca-cert without the cluster key" >&2
  exit 1
fi
if ! grep -Fq 'guest-cli.sh' "${mod}/guest/publish.sh"; then
  echo "publish must install flynn-host on the guest PATH" >&2
  exit 1
fi
if ! grep -Fq '192.0.2.200:1111' "${mod}/guest/guest-cli.sh"; then
  echo "guest flynn-host wrapper must set DISCOVERD to the TEST-NET listen IP" >&2
  exit 1
fi
if ! grep -Fq 'discoverd.DefaultClient = discoverd.NewClient()' "${ROOT}/host/host.go"; then
  echo "flynn-host CLI must recreate DefaultClient after loading host.json DISCOVERD" >&2
  exit 1
fi
if ! grep -Fq 'FLYNN_SKIP_UPDATE_CHECK' "${mod}/host/mac.sh"; then
  echo "host/mac.sh must skip the CLI update nag during cluster:add" >&2
  exit 1
fi
if ! grep -Fq './build.sh cluster' "${mod}/guest/build-images.sh"; then
  echo "guest/build-images.sh must run ./build.sh cluster when the base layer exists" >&2
  exit 1
fi
if ! grep -Fq 'pack-release.sh' "${mod}/guest/build-images.sh"; then
  echo "guest/build-images.sh must pack a release tarball so setup can restore layers" >&2
  exit 1
fi
if ! grep -Fq 'if [[ -f /etc/flynn/host.json ]]; then' "${mod}/guest/build-images.sh"; then
  echo "build must set FLYNN_KEEP_CLUSTER only when host.json exists (fresh builder has none)" >&2
  exit 1
fi
if ! grep -Fq 'FLYNN_KEEP_CLUSTER=1' "${mod}/guest/build-images.sh"; then
  echo "build-images must keep a bootstrapped cluster (build.sh cluster stop-all kills discoverd)" >&2
  exit 1
fi
if grep -Fq 'export FLYNN_KEEP_CLUSTER=1 FLYNN_ROOT' "${script}"; then
  echo "vagrant.sh must not force KEEP_CLUSTER on a builder that has never been set up" >&2
  exit 1
fi
if ! grep -Fq 'boot_builder' "${script}"; then
  echo "vagrant.sh build must boot the builder so it works before setup" >&2
  exit 1
fi
if ! grep -Fq 'exit 2' "${mod}/guest/restore-layers.sh"; then
  echo "restore-layers must exit 2 when there is no tarball so setup can build images" >&2
  exit 1
fi
if grep -Eqi 'run .*build first' "${mod}/guest/restore-layers.sh"; then
  echo "restore-layers must not tell the operator to run build then setup again" >&2
  exit 1
fi
if ! grep -Fq 'keep_bootstrapped_cluster' "${ROOT}/build.sh"; then
  echo "build.sh must skip teardown/stop-all when FLYNN_KEEP_CLUSTER=1 and host.json exist" >&2
  exit 1
fi
if grep -Fq 'image builds may fail' "${ROOT}/build.sh"; then
  echo "build.sh start must not continue into toolchain when discoverd is down" >&2
  exit 1
fi
if ! grep -Fq 'falling back to start-all' "${ROOT}/build.sh"; then
  echo "KEEP_CLUSTER with leftover host.json (failed bootstrap) must start-all so flynn-builder can list hosts" >&2
  exit 1
fi
if ! grep -Fq '192.0.2.200:1111/ping' "${ROOT}/build.sh"; then
  echo "discoverd_up must probe /ping (GET /services is 404; curl -f treated a live discoverd as down)" >&2
  exit 1
fi
if grep -Fq '192.0.2.200:1111/services"' "${ROOT}/build.sh"; then
  echo "build.sh must not curl -f GET /services to decide if discoverd is up" >&2
  exit 1
fi
if ! grep -Fq 'not retrying flynn-builder' "${ROOT}/build.sh"; then
  echo "toolchain must fail fast when discoverd is down instead of 10 flynn-builder attempts" >&2
  exit 1
fi
if ! grep -Fq 'export_host_json_secrets' "${ROOT}/build.sh"; then
  echo "KEEP_CLUSTER builds must export DISCOVERD_AUTH_KEY from host.json for flynn-builder" >&2
  exit 1
fi
if ! grep -Fq 'startStopDaemonRestartScript' "${ROOT}/host/cli/github_updater.go"; then
  echo "flynn-host update must restart start-stop-daemon on vagrant-dev" >&2
  exit 1
fi
if ! grep -Fq -e '--bin-dir=' "${mod}/guest/update.sh"; then
  echo "guest/update.sh must install the tarball into build/bin for start-flynn-host" >&2
  exit 1
fi
if ! grep -Fq 'treating as not bootstrapped' "${mod}/guest/ensure-cluster.sh"; then
  echo "ensure-cluster must exit 2 when leftover host.json has no controller so setup bootstraps" >&2
  exit 1
fi
if ! grep -Fq 'host_json_bootstrapped' "${mod}/guest/ensure-cluster.sh"; then
  echo "ensure-cluster must ignore a guest-cli DISCOVERD-only host.json stub" >&2
  exit 1
fi
sample=$'Welcome to Ubuntu\nFLYNN_DEV_PIN=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nFLYNN_DEV_KEY=0123456789abcdef0123456789abcdef\n'
pin="$(printf '%s\n' "${sample}" | sed -n 's/^FLYNN_DEV_PIN=//p' | tail -1)"
key="$(printf '%s\n' "${sample}" | sed -n 's/^FLYNN_DEV_KEY=//p' | tail -1)"
if [[ "${pin}" != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" || "${key}" != "0123456789abcdef0123456789abcdef" ]]; then
  echo "labeled creds parse failed: pin=${pin} key=${key}" >&2
  exit 1
fi
bash "${ROOT}/script/vagrant/test/clean-flynn.sh"
help="$(bash "${entry}" help)"
for want in "setup" "cli" "bootstrap" "update" "reload" "restart" "stop" "destroy" "teardown" "build-dev" "flynn -c local apps" "make vagrant-setup" "dev-node1" "build images if none exist" "no cluster required"; do
  if ! grep -Fq "${want}" <<<"${help}"; then
    echo "help missing ${want}" >&2
    exit 1
  fi
done
echo "ok"
