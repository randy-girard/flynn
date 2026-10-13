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
  "lib/cluster.sh" \
  "guest/restore-layers.sh" \
  "guest/build-images.sh" \
  "guest/install-node.sh" \
  "guest/init-node.sh" \
  "guest/bootstrap-node.sh" \
  "guest/update-cluster.sh" \
  "guest/start-node.sh" \
  "guest/probe-cluster.sh" \
  "guest/wait-tenant-http.sh" \
  "probe_live_cluster" \
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
if grep -Fq 'script/bootstrap-flynn' "${script}"; then
  echo "vagrant.sh must not bootstrap a live cluster on the builder (script/bootstrap-flynn)" >&2
  exit 1
fi
if grep -Fq 'guest/publish.sh' "${script}"; then
  echo "vagrant.sh must not DNAT a nested builder cluster onto the laptop" >&2
  exit 1
fi
if grep -Fq 'guest/ensure-cluster.sh' "${script}"; then
  echo "vagrant.sh must not start the live cluster on the builder via ensure-cluster.sh" >&2
  exit 1
fi
if ! grep -Fq 'FLYNN_VAGRANT_ENV=dev' "${mod}/lib/env.sh"; then
  echo "lib/env.sh must set FLYNN_VAGRANT_ENV=dev for the laptop loop" >&2
  exit 1
fi
if grep -Fq 'vagrant up builder' "${script}"; then
  echo "vagrant.sh must not boot the smoke builder" >&2
  exit 1
fi
if ! grep -Fq -e 'flynn-host update --all-nodes' "${mod}/guest/update-cluster.sh"; then
  echo "guest/update-cluster.sh must run flynn-host update --all-nodes on cluster nodes" >&2
  exit 1
fi
if ! grep -Fq 'flynn-linux-arm64.gz' "${mod}/guest/install-node.sh"; then
  echo "install-node.sh must install the Flynn CLI from the tarball so probe can run flynn apps" >&2
  exit 1
fi
if ! grep -Fq 'ensure-go.sh' "${mod}/guest/install-node.sh"; then
  echo "install-node.sh must install Go so plugin:install can compile on cluster nodes" >&2
  exit 1
fi
if ! grep -Fq 'ensure-flynn-root.sh' "${mod}/guest/ensure-go.sh"; then
  echo "ensure-go.sh must persist FLYNN_ROOT for plugin-build Auth-Key" >&2
  exit 1
fi
if ! grep -Fq 'ensure-flynn-root.sh' "${mod}/guest/update-cluster.sh"; then
  echo "update-cluster.sh must persist FLYNN_ROOT on vagrant-update" >&2
  exit 1
fi
if ! grep -Fq 'ensure-flynn-root.sh' "${script}"; then
  echo "vagrant.sh must persist FLYNN_ROOT on every cluster node" >&2
  exit 1
fi
if ! grep -Fq 'ensure-flynn-root.sh' "${mod}/guest/start-node.sh"; then
  echo "start-node.sh must persist FLYNN_ROOT after reload" >&2
  exit 1
fi
if ! grep -Fq 'ensure-qemu-binfmt.sh' "${mod}/guest/install-node.sh"; then
  echo "install-node.sh must register qemu-user binfmt on aarch64 cluster nodes" >&2
  exit 1
fi
if ! grep -Fq 'ensure-qemu-binfmt.sh' "${mod}/guest/start-node.sh"; then
  echo "start-node.sh must register qemu-user binfmt after reload" >&2
  exit 1
fi
if ! grep -Fq 'ensure-qemu-binfmt.sh' "${script}"; then
  echo "vagrant.sh update must register qemu-user binfmt on every cluster node" >&2
  exit 1
fi
if ! grep -Fq '/etc/flynn/source-root' "${mod}/guest/ensure-flynn-root.sh"; then
  echo "ensure-flynn-root.sh must write /etc/flynn/source-root" >&2
  exit 1
fi
if ! grep -Fq 'FLYNN_ROOT=' "${mod}/guest/ensure-flynn-root.sh"; then
  echo "ensure-flynn-root.sh must export FLYNN_ROOT" >&2
  exit 1
fi
if ! grep -Fq 'builder/img/go.sh' "${mod}/guest/ensure-go.sh"; then
  echo "ensure-go.sh must pin Go from builder/img/go.sh" >&2
  exit 1
fi
if ! grep -Fq 'visudo -cf' "${mod}/guest/ensure-go.sh"; then
  echo "ensure-go.sh must lint only flynn-go with visudo -cf (global visudo -c fails on Vagrant 0644 sudoers.d/vagrant)" >&2
  exit 1
fi
if grep -Eq 'visudo -c >/dev/null' "${mod}/guest/ensure-go.sh"; then
  echo "ensure-go.sh must not treat an unrelated visudo -c failure as a bad flynn-go fragment" >&2
  exit 1
fi
if ! grep -Fq 'chmod 440 /etc/sudoers.d/vagrant' "${mod}/guest/ensure-go.sh"; then
  echo "ensure-go.sh must chmod Vagrant sudoers.d/vagrant to 0440 so visudo -c can pass" >&2
  exit 1
fi
if ! grep -Fq 'export PATH="/usr/local/go/bin:${PATH}"' "${mod}/guest/install-node.sh"; then
  echo "install-node.sh must put /usr/local/go/bin on PATH after ensure-go.sh (child export does not apply)" >&2
  exit 1
fi
if ! grep -Fq 'flynn apps --all' "${mod}/guest/probe-cluster.sh"; then
  echo "probe-cluster.sh must list system apps with flynn apps --all (GET /apps hides them)" >&2
  exit 1
fi
if grep -Fq 'flynn-host update' "${mod}/guest/update.sh"; then
  echo "guest/update.sh must only build/pack on the builder; cluster update is update-cluster.sh" >&2
  exit 1
fi
if ! grep -Fq 'flynn_vagrant_cluster_build_needed' "${mod}/guest/update.sh"; then
  echo "guest/update.sh must rebuild cluster images when Flynn source changed" >&2
  exit 1
fi
if ! grep -Fq 'flynn_vagrant_write_build_stamp' "${mod}/guest/build-images.sh"; then
  echo "build-images.sh must record a stamp so update can skip a no-op rebuild" >&2
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
if ! grep -Fq 'verify_host_key = :never' "${ROOT}/Vagrantfile"; then
  echo "Vagrantfile must auto-accept SSH host keys so setup is non-interactive" >&2
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
if grep -Fq 'if [[ -n "$(flynn_vagrant_get_nodes)" ]]; then' "${mod}/lib/lifecycle.sh"; then
  echo "pin_nodes must discover extra hosts even when FLYNN_DEV_NODES defaults to 1" >&2
  exit 1
fi
if ! grep -Fq 'if [[ -z "${cur}" || "${n}" -gt "${cur}" ]]' "${mod}/lib/lifecycle.sh"; then
  echo "pin_nodes must raise the node count to VMs already in .vagrant-dev" >&2
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
if ! grep -Fq 'guest/start-node.sh' "${script}"; then
  echo "reload must start flynn-host.service on cluster nodes, not nested bootstrap on the builder" >&2
  exit 1
fi
if ! grep -Fq 'reload_cluster_rolling' "${script}"; then
  echo "reload must reboot cluster nodes one at a time" >&2
  exit 1
fi
if ! grep -Fq 'wait_cluster_stable' "${script}"; then
  echo "reload must wait for Flynn to settle after each cluster node" >&2
  exit 1
fi
if ! grep -Fq 'wait_cluster_stable "${name}" 180 1' "${script}"; then
  echo "rolling reload must wait for every postgres peer to be healthy before the next node" >&2
  exit 1
fi
if ! grep -Fq 'PROBE_SIRENIA' "${mod}/guest/probe-cluster.sh"; then
  echo "probe-cluster.sh must support PROBE_SIRENIA postgres peer checks" >&2
  exit 1
fi
if ! grep -Fq 'PROBE_FULL_STRENGTH="${sirenia}" probe_live_cluster' "${script}" \
  || ! grep -Fq 'PROBE_FULL_STRENGTH=${PROBE_FULL_STRENGTH:-}' "${script}"; then
  echo "rolling reload must wait for every service to reach its formation count before the next node" >&2
  exit 1
fi
if ! grep -Fq 'formations?active=true' "${mod}/guest/probe-cluster.sh" \
  || ! grep -Fq 'services below formation count' "${mod}/guest/probe-cluster.sh"; then
  echo "probe-cluster.sh PROBE_FULL_STRENGTH must compare discoverd instances with active formations" >&2
  exit 1
fi
if ! grep -E '^  update\)' -A20 "${script}" | grep -Fq 'wait_cluster_stable'; then
  echo "vagrant-update must wait for cluster status after flynn-host update (blobstore can be briefly unhealthy)" >&2
  exit 1
fi
if ! grep -Fq 'start_one_cluster_node' "${script}"; then
  echo "rolling reload must start flynn-host on the node that just rebooted before probing" >&2
  exit 1
fi
if ! grep -Fq 'TRIES="${TRIES:-180}"' "${mod}/guest/start-node.sh"; then
  echo "start-node.sh must wait long enough for router/controller after a crash-style reboot" >&2
  exit 1
fi
if ! grep -Fq 'no listed async present, using unassigned peer as new sync' "${ROOT}/pkg/sirenia/state/state.go"; then
  echo "sirenia sync takeover must use unassigned live peers after a crash-style node loss" >&2
  exit 1
fi
if ! grep -Fq 'TestPostgresSyncTakesOverWhenPrimaryGoneWithUnassignedPeer' "${ROOT}/pkg/sirenia/state/applyconfig_test.go"; then
  echo "sirenia must test takeover when the listed async is gone and an unassigned peer is live" >&2
  exit 1
fi
if ! grep -Fq 'TestPostgresAsyncTakesOverWhenPrimaryAndSyncGone' "${ROOT}/pkg/sirenia/state/applyconfig_test.go"; then
  echo "sirenia async must take over when primary and sync die together (same-host reboot)" >&2
  exit 1
fi
if ! grep -Fq 'TestPostgresAsyncTakesOverAsSolePrimaryWhenNoOtherPeers' "${ROOT}/pkg/sirenia/state/applyconfig_test.go"; then
  echo "sirenia async must promote as sole primary when it is the only remaining replica" >&2
  exit 1
fi
if ! grep -Fq 'primary and sync gone' "${ROOT}/pkg/sirenia/state/state.go"; then
  echo "first async must startTakeover when primary and sync are both absent" >&2
  exit 1
fi
if ! grep -Fq 'placing sirenia sync on a different host than primary' "${ROOT}/host/fixer/sirenia.go"; then
  echo "FixSirenia must not restart primary and sync on the same host" >&2
  exit 1
fi
if ! grep -Fq 'TestPickSireniaSyncHostID' "${ROOT}/host/fixer/fixer_test.go"; then
  echo "FixSirenia must test that sync is placed off the primary host" >&2
  exit 1
fi
if ! grep -Fq 'GetHostSpreadCounts' "${ROOT}/controller/scheduler/scheduler.go"; then
  echo "HA sirenia placement must spread across hosts using release-first job counts" >&2
  exit 1
fi
if ! grep -Fq 'TestPickHostSpreadsHAAcrossReleases' "${ROOT}/controller/scheduler/resources_test.go"; then
  echo "scheduler must test HA postgres peers spread across releases/hosts" >&2
  exit 1
fi
if ! grep -Fq 'TestGetHostJobCountsByAppType' "${ROOT}/controller/scheduler/job_test.go"; then
  echo "scheduler must test app-wide host counts so HA placement ignores old releases" >&2
  exit 1
fi
if ! grep -Fq 'controller wait timed out' "${script}"; then
  echo "rolling reload must keep waiting for Flynn after start-node controller timeout" >&2
  exit 1
fi
if ! awk '/^start_one_cluster_node\(\)/,/^}/' "${script}" | grep -Fq 'if [[ "${st}" -ne 0 ]]; then'; then
  echo "start_one_cluster_node must return non-zero from if so bash 3.2 + set -e cannot abort rolling reload" >&2
  exit 1
fi
if ! grep -Fq 'UnmarshalState reconnects to containers' "${ROOT}/host/state.go"; then
  echo "host Restore must UnmarshalState outside the bolt View so SetStatusFailed cannot deadlock after a crash reboot" >&2
  exit 1
fi
if ! grep -Fq 'TestRestoreUnmarshalStateOutsideBoltView' "${ROOT}/host/state_test.go"; then
  echo "host Restore must test that UnmarshalState/SetStatusFailed does not deadlock on bolt View" >&2
  exit 1
fi
if ! grep -Fq 'discoverdRepublishInterval' "${ROOT}/flannel/subnet/subnet.go"; then
  echo "flannel LeaseRenewer must republish often after prune drops a reboot host's subnet lease" >&2
  exit 1
fi
if ! grep -Fq 'TestLeaseRepublishWaitCapsDiscoverdPlaceholderExpiry' "${ROOT}/flannel/subnet/subnet_test.go"; then
  echo "flannel must test that discoverd placeholder expiry cannot skip lease republish" >&2
  exit 1
fi
if ! grep -Fq 'TestReturningHostLeaseIsKeptAfterPrune' "${ROOT}/flannel/discoverd/registry_prune_test.go"; then
  echo "flannel must test prune-then-UpdateSubnet keeps a returning host lease" >&2
  exit 1
fi
if ! grep -Fq 'TestCreateAndUpdateSubnetDoNotPrunePeers' "${ROOT}/flannel/discoverd/registry_prune_test.go"; then
  echo "flannel Create/UpdateSubnet must not prune a registering peer" >&2
  exit 1
fi
if ! grep -Fq 'TestParseSubnetWatchResponseEmitsEveryPeer' "${ROOT}/flannel/subnet/subnet_test.go"; then
  echo "flannel Watch must emit every peer lease after reload" >&2
  exit 1
fi
if ! grep -Fq 'reconcilePeerOverlay' "${ROOT}/flannel/backend/vxlan/vxlan.go"; then
  echo "flannel must re-apply peer overlay neigh/routes after crash-reload" >&2
  exit 1
fi
if ! grep -Fq 'waitForSyncResetDeadline' "${ROOT}/appliance/postgresql/process.go"; then
  echo "postgres waitForSync must not reset timeout when replication status is missing" >&2
  exit 1
fi
if ! grep -Fq 'enablePrimaryWrites' "${ROOT}/appliance/postgresql/process.go"; then
  echo "postgres waitForSync timeout must enable primary writes" >&2
  exit 1
fi
if ! grep -Fq 'TestWaitForSyncResetDeadline' "${ROOT}/appliance/postgresql/process_health_test.go"; then
  echo "postgres must test waitForSync timeout policy" >&2
  exit 1
fi
if ! grep -Fq 'waitForSyncAppearanceTimeout' "${ROOT}/appliance/postgresql/process.go"; then
  echo "sync waitForSync must wait through pg_basebackup before the 1m stall timer" >&2
  exit 1
fi
if ! grep -Fq 'TestWaitForReplSyncSucceedsWhenXLogMatchesWithoutSyncedDownstream' "${ROOT}/pkg/sirenia/client/client_test.go"; then
  echo "WaitForReplSync must succeed when WAL matches even if SyncedDownstream was never set" >&2
  exit 1
fi
if ! grep -Fq 'pingLeaseURLs' "${ROOT}/flannel/main.go"; then
  echo "flannel status must ping PublicIP so update/reload is not blocked by overlay mesh pings" >&2
  exit 1
fi
if ! grep -Fq 'decodeNetwork' "${ROOT}/flannel/discoverd/registry.go"; then
  echo "flannel must decode discoverd ServiceMeta.Data JSON strings or new flanneld cannot start after update" >&2
  exit 1
fi
if ! grep -Fq 'TestDecodeNetworkAcceptsDiscoverdStringData' "${ROOT}/flannel/discoverd/registry_prune_test.go"; then
  echo "flannel must test ServiceMeta.Data string unwrap" >&2
  exit 1
fi
if ! grep -Fq 'func (n *Network) UnmarshalJSON' "${ROOT}/flannel/discoverd/registry.go"; then
  echo "Network must UnmarshalJSON so GetMeta string Data cannot block SubnetManager" >&2
  exit 1
fi
if ! grep -Fq 'TestDecodeNetworkFromDiscoverdServiceMeta' "${ROOT}/flannel/discoverd/registry_prune_test.go"; then
  echo "flannel must test the live GET /services/flannel/meta string-Data path" >&2
  exit 1
fi
if ! grep -Fq 'TestPingLeasesHealthyOnPublicIPWhenOverlayDown' "${ROOT}/flannel/ping_test.go"; then
  echo "flannel must test PublicIP /ping keeps the cluster healthy when overlay ping fails" >&2
  exit 1
fi
if ! grep -Fq 'TestDeprovisionAnyRetriesAfterObjectNotFoundJSON' "${ROOT}/pkg/resource/resource_test.go"; then
  echo "resource.DeprovisionAny must retry FLYNN_* name after plugin UUID 404" >&2
  exit 1
fi
if ! grep -Fq '${#builders[@]} -gt 0' "${script}"; then
  echo "rolling reload must not expand empty builders[@] under set -u (node-only VM=)" >&2
  exit 1
fi
if grep -Fq 'drain_one_cluster_node' "${script}"; then
  echo "rolling reload must not drain; a reboot is a node failure and remaining hosts recover" >&2
  exit 1
fi
if ! grep -Fq 'node failure' "${script}"; then
  echo "rolling reload must treat a VM reboot as a node failure" >&2
  exit 1
fi
if ! grep -Fq 'wait_tenant_http' "${script}"; then
  echo "rolling reload must wait for tenant HTTP after Flynn recovers from the node reboot" >&2
  exit 1
fi
if ! grep -Fq 'list_tenant_apps' "${script}"; then
  echo "rolling reload must snapshot tenant apps before a crash so wait still covers them" >&2
  exit 1
fi
if ! grep -Fq 'TENANT_APPS' "${mod}/guest/wait-tenant-http.sh"; then
  echo "wait-tenant-http.sh must accept a pre-crash tenant app list" >&2
  exit 1
fi
if ! grep -Fq 'monitorcheck.HaveQuorum' "${ROOT}/host/monitor.go"; then
  echo "cluster-monitor must repair with remaining hosts instead of waiting for a dead node" >&2
  exit 1
fi
if ! grep -Fq 'postgres has no leader' "${ROOT}/host/monitor.go"; then
  echo "cluster-monitor must repair when postgres has no sirenia leader after a node crash" >&2
  exit 1
fi
if ! grep -Fq 'code not in ("502", "503")' "${mod}/guest/wait-tenant-http.sh"; then
  echo "wait-tenant-http.sh must treat HTTP 502/503 as down (no router backends)" >&2
  exit 1
fi
if ! grep -Fq 'controller-scheduler' "${mod}/guest/probe-cluster.sh"; then
  echo "probe-cluster.sh must require controller-scheduler or tenant apps stay 503" >&2
  exit 1
fi
if ! grep -Fq '"controller-scheduler" in unhealthy' "${mod}/guest/probe-cluster.sh"; then
  echo "probe must not settle when controller-scheduler is unhealthy" >&2
  exit 1
fi
if grep -E 'reload\|restart\)' -A5 "${script}" | grep -Fq 'start_existing_cluster'; then
  echo "reload must not start every node only after all VMs reboot" >&2
  exit 1
fi
if ! grep -Fq 'status.${DOMAIN}' "${mod}/guest/probe-cluster.sh"; then
  echo "probe-cluster.sh must check status.<domain> so rolling reload waits for a healthy cluster" >&2
  exit 1
fi
if ! grep -Fq '"healthy"' "${mod}/guest/probe-cluster.sh"; then
  echo "probe-cluster.sh must require an overall healthy status payload" >&2
  exit 1
fi
if ! grep -Fq 'scode}" != "200" && "${scode}" != "500"' "${mod}/guest/probe-cluster.sh"; then
  echo "probe must parse status HTTP 500 JSON (overall unhealthy is 500, not a missing app)" >&2
  exit 1
fi
if ! grep -Fq 'unhealthy == ["controller"]' "${mod}/guest/probe-cluster.sh"; then
  echo "probe must settle when only status.detail.controller is down and controller.<domain> answers" >&2
  exit 1
fi
if ! grep -Fq 'PROBE_QUICK' "${script}"; then
  echo "rolling reload must skip flynn login/apps while waiting for the cluster to settle" >&2
  exit 1
fi
if ! grep -Fq 'PROBE_QUICK=${PROBE_QUICK:-}' "${script}"; then
  echo "probe_live_cluster must pass PROBE_QUICK into the guest script" >&2
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
  echo "bootstrap-flynn (single-machine nested cluster) must use the alloc flannel backend" >&2
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
if ! grep -Fq 'Log in with' "${ROOT}/cli/cluster.go"; then
  echo "cluster:add must remind the operator to flynn login" >&2
  exit 1
fi
sample_add=$'Install the Flynn CLI\n\nflynn cluster:add -p KGCENkp53YF5OvOKkZIry71+czFRkSw2ZdMszZ/0ljs= default 1.localflynn.com\nflynn login --email <admin@1.localflynn.com>\n'
python3 - <<PY
import re
text = """${sample_add}"""
m = re.search(r"flynn cluster:add -p (\S+) \S+ (\S+)\s*$", text, re.M)
if not m or m.group(2) != "1.localflynn.com":
    raise SystemExit("cli-add-command parse failed")
if "flynn login --email" not in text:
    raise SystemExit("cli-add-command must print flynn login")
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
if ! grep -Fq 'sudo -n' "${mod}/host/mac.sh"; then
  echo "host/mac.sh must use sudo -n when FLYNN_VAGRANT_YES=1" >&2
  exit 1
fi
if ! grep -Fq 'FLYNN_VAGRANT_FORCE_BUILD' "${mod}/guest/build-needed.sh"; then
  echo "build-needed.sh must honor FLYNN_VAGRANT_FORCE_BUILD=1" >&2
  exit 1
fi
if ! grep -Fq -- '--yes|-y)' "${script}"; then
  echo "vagrant.sh must accept --yes" >&2
  exit 1
fi
if ! grep -Fq 'probe)' "${script}"; then
  echo "vagrant.sh must have a probe command" >&2
  exit 1
fi
if ! grep -Fq '192.168.57.20' "${mod}/host/mac.sh"; then
  echo "host/mac.sh must point the laptop at cluster node1 (192.168.57.20), not the builder" >&2
  exit 1
fi
if grep -Fq '192.168.57.10' "${mod}/host/mac.sh"; then
  echo "host/mac.sh must not use the builder IP for the live cluster" >&2
  exit 1
fi
if ! grep -Fq 'auth.${DOMAIN}' "${mod}/host/mac.sh"; then
  echo "host/mac.sh must map auth.<domain> for controller-owned cluster login" >&2
  exit 1
fi
if ! grep -Fq 'auth.${DOMAIN}' "${mod}/guest/node-dns.sh"; then
  echo "node-dns.sh must map auth.<domain> for in-guest cluster login" >&2
  exit 1
fi
if ! grep -Fq 'FLYNN_CLUSTER_IP' "${mod}/guest/creds.sh"; then
  echo "guest/creds.sh must resolve the controller on the cluster node IP" >&2
  exit 1
fi
for s in \
  "${mod}/lib/cluster.sh" \
  "${mod}/guest/install-node.sh" \
  "${mod}/guest/init-node.sh" \
  "${mod}/guest/bootstrap-node.sh" \
  "${mod}/guest/update-cluster.sh" \
  "${mod}/guest/node-dns.sh" \
  "${mod}/guest/start-node.sh" \
  "${mod}/guest/probe-cluster.sh" \
  "${mod}/guest/drain-node.sh" \
  "${mod}/guest/wait-tenant-http.sh" \
  "${mod}/guest/ensure-go.sh" \
  "${mod}/guest/ensure-flynn-root.sh" \
  "${mod}/guest/ensure-qemu-binfmt.sh"; do
  if ! bash -n "${s}"; then
    echo "${s} failed bash -n" >&2
    exit 1
  fi
done
if ! grep -Fq './build.sh cluster' "${mod}/guest/build-images.sh"; then
  echo "guest/build-images.sh must run ./build.sh cluster when the base layer exists" >&2
  exit 1
fi
if ! grep -Fq 'pack-release.sh' "${mod}/guest/build-images.sh"; then
  echo "guest/build-images.sh must pack a release tarball so setup can restore layers" >&2
  exit 1
fi
if grep -Fq 'FLYNN_KEEP_CLUSTER=1' "${mod}/guest/build-images.sh"; then
  echo "build-images must not keep a live operator cluster on the builder" >&2
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
if ! grep -Fq 'halt_builder' "${script}"; then
  echo "vagrant.sh must halt the builder after compile so it does not sit on ~30GB RAM" >&2
  exit 1
fi
if grep -E 'build\)' -A3 "${script}" | grep -Fq 'build_images' && ! grep -E 'build\)' -A4 "${script}" | grep -Fq 'halt_builder'; then
  echo "vagrant.sh build must halt the builder after images are built" >&2
  exit 1
fi
if grep -E 'update\)' -A4 "${script}" | grep -Fq 'update_running_cluster' && ! grep -E 'update\)' -A5 "${script}" | grep -Fq 'halt_builder'; then
  echo "vagrant.sh update must halt the builder after compile before cluster update" >&2
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
if ! grep -Fq 'wait_for_flynn_host' "${ROOT}/build.sh"; then
  echo "build.sh start must wait for flynn-host in discoverd, not only /ping" >&2
  exit 1
fi
if ! grep -Fq '/services/flynn-host/instances' "${ROOT}/build.sh"; then
  echo "flynn_host_registered must probe discoverd flynn-host instances" >&2
  exit 1
fi
if ! grep -Fq 'd.get("discoverd")' "${ROOT}/build.sh"; then
  echo "flynn_host_registered must require the current daemon's discoverd.url, not a stale raft instance" >&2
  exit 1
fi
if ! grep -Fq 'wait_for_host_http' "${ROOT}/script/start-all"; then
  echo "start-all must wait for flynn-host HTTP before starting discoverd" >&2
  exit 1
fi
if ! grep -Fq 'rm -rf /tmp/discoverd-data' "${ROOT}/script/start-all"; then
  echo "start-all must drop leftover discoverd raft so wait_for_flynn_host cannot see a dead flynn-host instance" >&2
  exit 1
fi
if ! grep -Fq 'failed to notify: %s; retrying' "${ROOT}/discoverd/main.go"; then
  echo "discoverd Notify must retry when flynn-host HTTP is not listening yet" >&2
  exit 1
fi
if ! grep -Fq 'busy (image layer), skipping' "${ROOT}/host/cli/destroy-volumes.go"; then
  echo "destroy-volumes must skip busy ext2/squashfs layers so start-all can launch the daemon" >&2
  exit 1
fi
if ! grep -Fq 'destroy-volumes did not finish' "${ROOT}/script/start-flynn-host"; then
  echo "start-flynn-host must start the daemon if destroy-volumes fails" >&2
  exit 1
fi
if ! grep -Fq 'unmounting leftover Flynn volumes' "${ROOT}/script/kill-flynn"; then
  echo "kill-flynn must unmount leftover zfs/overlay mounts before start-all destroy-volumes" >&2
  exit 1
fi
if ! grep -Fq 'startStopDaemonRestartScript' "${ROOT}/host/cli/github_updater.go"; then
  echo "flynn-host update must restart start-stop-daemon when the builder runs start-all" >&2
  exit 1
fi
if grep -Fq -e '--bin-dir=' "${mod}/guest/update.sh"; then
  echo "guest/update.sh must not apply the tarball on the builder" >&2
  exit 1
fi
if ! grep -Fq 'update_running_cluster' "${script}"; then
  echo "vagrant.sh update must apply the tarball on running cluster nodes" >&2
  exit 1
fi
if ! grep -Fq 'node-dns.sh' "${mod}/guest/update-cluster.sh"; then
  echo "update-cluster.sh must refresh /etc/hosts so auth.<domain> exists after tarball apply" >&2
  exit 1
fi
if ! grep -Fq 'timeout 20 flynn login' "${mod}/guest/probe-cluster.sh"; then
  echo "probe-cluster must not hang flynn login when auth.<domain> DNS fails" >&2
  exit 1
fi
if ! grep -Fq 'timeout 20 flynn apps' "${mod}/guest/probe-cluster.sh"; then
  echo "probe-cluster must not hang flynn apps while waiting for a rolling reload" >&2
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
bash "${ROOT}/script/vagrant/test/update-build.sh"
help="$(bash "${entry}" help)"
for want in "monitor)" "chaos)" "lib/chaos.sh"; do
  if ! grep -Fq "${want}" "${script}"; then
    echo "script/vagrant/vagrant.sh missing ${want}" >&2
    exit 1
  fi
done
for f in guest/check-health.sh guest/chaos-fault.sh lib/chaos.sh; do
  if ! bash -n "${mod}/${f}"; then
    echo "script/vagrant/${f} failed bash -n" >&2
    exit 1
  fi
done
for f in guest/check-health.sh guest/chaos-fault.sh; do
  if [[ ! -x "${mod}/${f}" ]]; then
    echo "script/vagrant/${f} must be executable" >&2
    exit 1
  fi
done
# The health sample must cover every layer the chaos loop can break.
for want in "PROBE_FULL_STRENGTH=1" "PROBE_SIRENIA=1" "wait-tenant-http.sh" "-plugin" "dashboard." "/providers" "/leader" "create_connection"; do
  if ! grep -Fq -e "${want}" "${mod}/guest/check-health.sh"; then
    echo "guest/check-health.sh missing ${want}" >&2
    exit 1
  fi
done
# Resource accounting covers the cluster datastores: the baseline inventory
# is passed back to every sample, and each cluster engine job is checked on
# its /metrics/log.
for want in "section resources" "EXPECT_RESOURCES" ":9180/metrics/log" "live_brokers" "healthy_members" "readonly_replicas" "inventory="; do
  if ! grep -Fq -e "${want}" "${mod}/guest/check-health.sh"; then
    echo "guest/check-health.sh resources section missing ${want}" >&2
    exit 1
  fi
done
for want in "CHAOS_EXPECT_RESOURCES" "inventory="; do
  if ! grep -Fq -e "${want}" "${mod}/lib/chaos.sh"; then
    echo "lib/chaos.sh missing ${want}" >&2
    exit 1
  fi
done
if ! grep -Fq 'host.endswith(".discoverd")' "${mod}/guest/chaos-fault.sh"; then
  echo "guest/chaos-fault.sh must treat <app>.discoverd (clickhouse, kafka) resources as datastores" >&2
  exit 1
fi
# The embedded python in the health sample and fault scripts must compile.
for f in guest/check-health.sh guest/chaos-fault.sh; do
  awk '/<<.?PY.?$/ { on = 1; n++; next } /^PY$/ { on = 0; next } on { print > ("'"${TMPDIR:-/tmp}"'/py-check-" n ".py") }' "${mod}/${f}"
  for py in "${TMPDIR:-/tmp}"/py-check-*.py; do
    python3 -m py_compile "${py}" || { echo "${f}: embedded python does not compile (${py})" >&2; exit 1; }
  done
  rm -f "${TMPDIR:-/tmp}"/py-check-*.py
done
for want in system-job plugin-job datastore-primary datastore-replica tenant-job host-restart node-reboot; do
  if ! grep -Fq "${want}" "${mod}/lib/chaos.sh"; then
    echo "lib/chaos.sh missing fault ${want}" >&2
    exit 1
  fi
done
if ! grep -Fq 'flynn-host", "stop"' "${mod}/guest/chaos-fault.sh"; then
  echo "guest/chaos-fault.sh must stop jobs with flynn-host stop (no drain)" >&2
  exit 1
fi
if ! grep -Fq 'print(stop.stdout.strip())' "${mod}/guest/chaos-fault.sh"; then
  echo "guest/chaos-fault.sh must print why flynn-host stop failed (inject-*.log)" >&2
  exit 1
fi
# chaos runs the monitor itself and writes a report; render one from fixtures.
report_dir="$(mktemp -d)"
printf '1\t19:00:00\tbaseline\thealthy\t-\t-\n2\t19:00:15\tround 1 node-reboot\tUNHEALTHY\tcluster,plugins\tcluster: below formation x 1/3 ;; plugins: dashboard=502\n3\t19:00:30\tround 1 node-reboot\tUNHEALTHY\tcluster\tcluster: below formation x 2/3 <db> & more\n' > "${report_dir}/samples.tsv"
printf '1\tnode-reboot\tnode=dev-node2\t0\t0\trecovered\t75s\n2\ttenant-job\t-\t0\t0\tskipped\t-\n' > "${report_dir}/rounds.tsv"
report="$(bash -c 'set -euo pipefail; flynn_vagrant_get_nodes() { echo 3; }; source "$1"; chaos_write_report "$2" 42 2 "node-reboot tenant-job" start "PASSED: 1 recovered, 1 skipped"; cat "$2/report.txt"' _ "${mod}/lib/chaos.sh" "${report_dir}")"
html="$(cat "${report_dir}/report.html")"
rm -rf "${report_dir}"
for want in "PASSED: 1 recovered, 1 skipped" "CHAOS_SEED=42" "node-reboot        recovered      75s       cluster,plugins" "3 samples every 15s, 1 healthy (33%)" "cluster    2/3            1        ~30s" "plugins: dashboard=502" "report.html"; do
  if ! grep -Fq -e "${want}" <<<"${report}"; then
    echo "chaos report missing: ${want}" >&2
    printf '%s\n' "${report}" >&2
    exit 1
  fi
done
for want in "<!DOCTYPE html>" "</html>" "PASSED: 1 recovered, 1 skipped" "CHAOS_SEED=42" '<span class="badge ok">recovered</span>' '<span class="badge skip">skipped</span>' "75s" ">33%<" "<td>cluster</td><td class=\"num\">2 / 3</td>" "plugins: dashboard=502" "below formation x 2/3 &lt;db&gt; &amp; more"; do
  if ! grep -Fq -e "${want}" <<<"${html}"; then
    echo "chaos HTML report missing: ${want}" >&2
    exit 1
  fi
done
if grep -Fq "<db>" <<<"${html}"; then
  echo "chaos HTML report must escape failure messages" >&2
  exit 1
fi
# The background monitor must not sample from a node under host-restart or
# node-reboot: its local discoverd and router are down, so every check fails.
chaos_stubs='set -euo pipefail; SRC=/src
flynn_vagrant_running_cluster_nodes() { printf "dev-node1\ndev-node2\n"; }
flynn_vagrant_node_ip() { echo 10.0.0.1; }; flynn_vagrant_cluster_domain() { echo d; }; flynn_vagrant_get_nodes() { echo 2; }
vagrant() { :; }; sleep() { :; }; flynn_vagrant_up_one() { :; }; start_one_cluster_node() { :; }
source "$1"'
mon_dir="$(mktemp -d)"
echo dev-node1 > "${mon_dir}/avoid"
echo "round 1 node-reboot" > "${mon_dir}/phase"
MON_DIR="${mon_dir}" bash -c "${chaos_stubs}"'
run_as_root_on() { echo "$1" >> "${MON_DIR}/used"; rm -f "${MON_DIR}/phase"; echo "ok cluster: x"; echo healthy; }
CHAOS_MONITOR_INTERVAL=0 chaos_monitor_loop "${MON_DIR}"' _ "${mod}/lib/chaos.sh"
if [[ "$(cat "${mon_dir}/used")" != "dev-node2" ]] || ! grep -q $'\thealthy\t' "${mon_dir}/samples.tsv"; then
  echo "chaos monitor must sample from a node other than the one in avoid" >&2
  exit 1
fi
for kind in node-reboot host-restart; do
  rm -f "${mon_dir}/avoid"
  CHAOS_RUN_DIR="${mon_dir}" bash -c "${chaos_stubs}"'
run_as_root_on() { :; }
chaos_inject "$2" 1' _ "${mod}/lib/chaos.sh" "${kind}"
  if ! grep -Eq '^dev-node[12]$' "${mon_dir}/avoid" 2>/dev/null; then
    echo "chaos ${kind} must write the faulted node to \$CHAOS_RUN_DIR/avoid" >&2
    exit 1
  fi
done
rm -rf "${mon_dir}"
if [[ "$(grep -cF 'rm -f "${CHAOS_RUN_DIR}/avoid"' "${mod}/lib/chaos.sh")" -lt 2 ]]; then
  echo "chaos_cluster must clear the avoid file at each round start and on recovery" >&2
  exit 1
fi
# A check that cannot run (node unreachable) must still say why.
health="$(bash -c "${chaos_stubs}"'
run_as_root_on() { echo "ssh: connect to host dev-node1 port 22: Connection refused"; return 255; }
chaos_health || true; printf "%s\n" "${CHAOS_HEALTH_OUT}"' _ "${mod}/lib/chaos.sh")"
if ! grep -Fq "FAIL monitor: health check on dev-node1 exited 255 without a result: ssh: connect to host dev-node1 port 22: Connection refused" <<<"${health}"; then
  echo "chaos_health must report a FAIL monitor line when the check prints none" >&2
  printf '%s\n' "${health}" >&2
  exit 1
fi
for want in "chaos_monitor_loop" "chaos_stop_monitor" "flynn-logs/chaos" "report.txt"; do
  if ! grep -Fq "${want}" "${mod}/lib/chaos.sh"; then
    echo "lib/chaos.sh missing ${want}" >&2
    exit 1
  fi
done
if ! grep -Fq 'vagrant halt --force' "${mod}/lib/chaos.sh"; then
  echo "chaos node-reboot must hard power off the VM (vagrant halt --force)" >&2
  exit 1
fi
if grep -Eq 'print\([^)]*(env\[|val\b|DATABASE_URL)' "${mod}/guest/check-health.sh" "${mod}/guest/chaos-fault.sh"; then
  echo "monitor/chaos guest scripts must not print resource env values" >&2
  exit 1
fi
# vagrant-plugin installs a sibling checkout, or updates it when the manifest's
# app is already in plugin:list. Both rebuild so source changes ship.
plug_dir="$(mktemp -d)"
mkdir -p "${plug_dir}/bin" "${plug_dir}/flynn-plugin-widget"
echo '{"name":"widget","app":{"name":"widget-plugin"}}' > "${plug_dir}/flynn-plugin-widget/flynn-plugin.json"
cat > "${plug_dir}/bin/flynn-host" <<'EOF'
#!/bin/bash
if [[ "$1" == "plugin:list" ]]; then
  echo "NAME  KIND  CLI  SOURCE  VERSION"
  cat "${PLUG_LIST}"
  exit 0
fi
echo "$*" > "${PLUG_CALL}"
EOF
chmod +x "${plug_dir}/bin/flynn-host"
plug_run() {
  PATH="${plug_dir}/bin:${PATH}" PLUG_LIST="${plug_dir}/list" PLUG_CALL="${plug_dir}/call" \
    FLYNN_VAGRANT_YES="$1" bash "${mod}/guest/plugin.sh" "${plug_dir}/flynn-plugin-widget" >/dev/null
}
echo "other-plugin app - /x -" > "${plug_dir}/list"
plug_run 0
if [[ "$(cat "${plug_dir}/call")" != "plugin:install --rebuild ${plug_dir}/flynn-plugin-widget" ]]; then
  echo "guest/plugin.sh must plugin:install --rebuild a plugin that is not installed" >&2
  exit 1
fi
echo "widget-plugin app - /x -" > "${plug_dir}/list"
plug_run 1
if [[ "$(cat "${plug_dir}/call")" != "plugin:update --rebuild --yes ${plug_dir}/flynn-plugin-widget" ]]; then
  echo "guest/plugin.sh must plugin:update --rebuild an installed plugin (and pass --yes with YES=1)" >&2
  exit 1
fi
if bash "${mod}/guest/plugin.sh" "${plug_dir}/missing" >/dev/null 2>&1; then
  echo "guest/plugin.sh must fail when the checkout has no flynn-plugin.json" >&2
  exit 1
fi
rm -rf "${plug_dir}"
for want in "plugin)" "guest/plugin.sh" "/opt/flynn-plugins/"; do
  if ! grep -Fq "${want}" "${script}"; then
    echo "script/vagrant/vagrant.sh missing ${want}" >&2
    exit 1
  fi
done
for want in "vagrant-monitor:" "vagrant-chaos:" "vagrant-plugin:"; do
  if ! grep -Fq "${want}" "${ROOT}/Makefile"; then
    echo "Makefile missing ${want}" >&2
    exit 1
  fi
done
for want in "make vagrant-monitor" "make vagrant-chaos" "make vagrant-plugin" "CHAOS_FAULTS" "CHAOS_SEED" "CHAOS_MONITOR_INTERVAL" "flynn-logs/chaos" "report.txt"; do
  if ! grep -Fq "${want}" "${ROOT}/docs/content/installation/vagrant.md"; then
    echo "docs/content/installation/vagrant.md missing ${want}" >&2
    exit 1
  fi
done
for want in "setup" "cli" "bootstrap" "update" "plugin" "probe" "monitor" "chaos" "CHAOS_ROUNDS" "MONITOR_INTERVAL" "CHAOS_MONITOR_INTERVAL" "report.txt" "reload" "restart" "stop" "destroy" "teardown" "build-dev" "flynn -c local apps" "make vagrant-setup" "dev-node1" "build images if none exist" "no cluster required" "if Flynn source changed" "--all-nodes" "compile-only" "--yes" "one at a time" "status" "halt the builder" "node failure" "tenant HTTP" "scheduler"; do
  if ! grep -Fq -e "${want}" <<<"${help}"; then
    echo "help missing ${want}" >&2
    exit 1
  fi
done
echo "ok"
