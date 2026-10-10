#!/bin/bash
# Regression: overlay NAT/MAC/promisc and install-time udev pin.
# 2026-09-09: udev MACAddressPolicy=persistent rewrote flannel.1 after the
# lease was published; MASQUERADE without ! -d overlay SNAT'd VXLAN; Vagrant
# NIC2 needed promiscuous mode; --clean left stale flannel.1.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need "${ROOT}/script/install-flynn" '10-flynn-flannel.link' \
  "install-flynn must write /etc/systemd/network/10-flynn-flannel.link"
need "${ROOT}/script/install-flynn" 'MACAddressPolicy=none' \
  "install-flynn must pin udev MACAddressPolicy=none for flannel.*"
need "${ROOT}/script/install-flynn-release" '10-flynn-flannel.link' \
  "install-flynn-release must write /etc/systemd/network/10-flynn-flannel.link"
need "${ROOT}/script/install-flynn" 'OriginalName=flannel\.\*' \
  "install-flynn udev .link must match flannel.*"
need "${ROOT}/script/install-flynn-release" 'MACAddressPolicy=none' \
  "install-flynn-release must pin udev MACAddressPolicy=none for flannel.*"
need "${ROOT}/script/install-flynn-release" 'OriginalName=flannel\.\*' \
  "install-flynn-release udev .link must match flannel.*"

vagrant="${ROOT}/Vagrantfile"
need "${vagrant}" 'FLYNN_MAX_NODES' \
  "Vagrantfile must generate node1..N from FLYNN_MAX_NODES (not a hard-coded count)"
need "${vagrant}" 'nicpromisc2", "allow-all"' \
  "Vagrantfile must set --nicpromisc2 allow-all on cluster node NICs (flannel VXLAN)"
need "${vagrant}" '192\.168\.56\.\#\{19 \+ i\}' \
  "cluster node N must be 192.168.56.(19+N) (node1=.20)"
need "${vagrant}" 'FLYNN_VAGRANT_ENV' \
  "Vagrantfile must keep smoke and dev machines in separate environments"
need "${vagrant}" 'verify_host_key = :never' \
  "Vagrantfile must skip SSH host-key prompts so setup/vagrant ssh is non-interactive"
need "${vagrant}" '192\.168\.57\.10' \
  "dev-builder must use 192.168.57.10 so it can run beside smoke builder .10"
need "${vagrant}" './build-dev' \
  "dev must overlay ./build-dev onto guest build/ so smoke keeps ./build"
need "${vagrant}" 'private_network' \
  "cluster nodes must use a host-only private_network (not NAT forwards) for service ports"
if grep -vE '^\s*#' "${vagrant}" | grep -q 'forwarded_port'; then
  echo "Vagrantfile must not NAT-forward cluster ports (that bypasses guest UFW)" >&2
  exit 1
fi

smoke="${ROOT}/script/vagrant/suite.sh"
need "${smoke}" 'FLYNN_VAGRANT_ENV=smoke' \
  "smoke must pin FLYNN_VAGRANT_ENV=smoke"
need "${smoke}" 'VAGRANT_DOTFILE_PATH="\$\{ROOT\}/\.vagrant"' \
  "smoke must keep its machine index in .vagrant, not .vagrant-dev"
if grep -Fq 'build-dev' "${smoke}"; then
  echo "smoke must keep Flynn artifacts in ./build, not ./build-dev" >&2
  exit 1
fi
need "${smoke}" 'vbox_id_for_node' \
  "NIC promisc check must wait for the Vagrant id file (parallel vagrant up can lag)"
need "${smoke}" 'VBoxManage list vms' \
  "NIC promisc check must fall back to VBoxManage list vms if the id file is missing"
need "${ROOT}/flannel/subnet/subnet.go" 'discoverdRepublishInterval' \
  "flannel LeaseRenewer must republish often; discoverd meta has no TTL and prune can drop a reboot host's /24"
need "${ROOT}/flannel/subnet/subnet.go" 'leaseRepublishWait' \
  "flannel must cap renew wait so a 10-year discoverd placeholder expiry cannot skip republish"
need "${ROOT}/flannel/subnet/subnet_test.go" 'TestLeaseRepublishWaitCapsDiscoverdPlaceholderExpiry' \
  "flannel must test that discoverd placeholder expiry republishes on a short interval"
need "${ROOT}/flannel/discoverd/registry.go" 'net.Subnets\[sn\] = rawJSON' \
  "flannel UpdateSubnet must re-insert a lease after prune dropped a returning host"
need "${ROOT}/flannel/discoverd/registry_prune_test.go" 'TestReturningHostLeaseIsKeptAfterPrune' \
  "flannel must test that a crash-rebooted host's /24 is pruned while down and kept after UpdateSubnet"
need "${ROOT}/flannel/discoverd/registry.go" 'getNetwork()' \
  "flannel Create/UpdateSubnet must not prune peers on write"
need "${ROOT}/flannel/discoverd/registry_prune_test.go" 'TestCreateAndUpdateSubnetDoNotPrunePeers' \
  "flannel must test Create/UpdateSubnet do not persist-drop a registering peer"
need "${ROOT}/flannel/discoverd/registry.go" 'pruneHost' \
  "flannel Down events must prune that host only, not every missing instance"
need "${ROOT}/flannel/discoverd/registry_prune_test.go" 'TestPruneLeasesForHostOnlyDropsThatHost' \
  "flannel must test Down-host prune keeps other peer leases"
need "${ROOT}/flannel/discoverd/registry.go" 'go r.pruneDownHost' \
  "flannel Down events must not prune a host whose flannel re-registers after a discoverd reconnect"
need "${ROOT}/flannel/discoverd/registry_test.go" 'TestDownEventKeepsLeaseOfLiveHost' \
  "flannel must test a reconnect Down event keeps a live host's lease"
need "${ROOT}/flannel/subnet/subnet.go" 'var events EventBatch' \
  "flannel Watch must emit every peer lease, not overwrite the batch with the last event"
need "${ROOT}/flannel/subnet/subnet_test.go" 'TestParseSubnetWatchResponseEmitsEveryPeer' \
  "flannel must test Watch applies every peer /24 after reload"
need "${ROOT}/flannel/backend/vxlan/device.go" 'err != syscall.EEXIST' \
  "flannel AddRoute must ignore EEXIST so reconcile can re-apply overlay routes"
need "${ROOT}/flannel/backend/vxlan/device_test.go" 'TestAddL3SetsPermanentNeigh' \
  "flannel must test AddL3 installs a permanent overlay next-hop neigh"
need "${ROOT}/flannel/backend/vxlan/vxlan.go" 'reconcilePeerOverlay' \
  "flannel must periodically re-apply peer FDB/L3/route after crash-reload"
need "${ROOT}/flannel/discoverd/registry.go" 'decodeNetwork' \
  "flannel must decode discoverd ServiceMeta.Data whether it is a JSON object or a JSON string"
need "${ROOT}/flannel/discoverd/registry_prune_test.go" 'TestDecodeNetworkAcceptsDiscoverdStringData' \
  "flannel must test ServiceMeta.Data string unwrap so a new flanneld can start after update"
need "${ROOT}/flannel/discoverd/registry.go" 'func \(n \*Network\) UnmarshalJSON' \
  "Network must UnmarshalJSON so GetMeta string Data cannot block SubnetManager"
need "${ROOT}/flannel/discoverd/registry_prune_test.go" 'TestDecodeNetworkFromDiscoverdServiceMeta' \
  "flannel must test the live GET /services/flannel/meta string-Data path"
need "${ROOT}/flannel/main.go" 'pingLeaseURLs' \
  "flannel status must ping PublicIP so flynn-host update/reload is not blocked by overlay mesh pings"
need "${ROOT}/flannel/ping_test.go" 'TestPingLeasesHealthyOnPublicIPWhenOverlayDown' \
  "flannel must test that PublicIP /ping keeps the cluster healthy when overlay ping fails"
need "${ROOT}/host/state.go" 'resurrectAttempts' \
  "host restore must retry persistent discoverd/flannel when overlay mount races zvol"
need "${ROOT}/host/state.go" 'error running resurrected' \
  "resurrect must check backend.Run so overlay ENOENT is retried locally without a scheduler"
need "${ROOT}/pkg/updaterdeploy/host_infra_jobs.go" 'ReplaceHostInfraJobs' \
  "update must replace persistent flannel/discoverd jobs after image pull (host-daemon, not controller)"
need "${ROOT}/pkg/updaterdeploy/host_infra_jobs.go" 'SetupMountspecs' \
  "host-infra replace must rewrite Mountspecs from the pulled image so reload does not Dup the bootstrap layers"
need "${ROOT}/updater/updater.go" 'ReplaceHostInfraJobs' \
  "in-cluster updater must replace flannel/discoverd jobs one host at a time after images are on disk"
need "${ROOT}/host/cli/github_updater.go" 'ReplaceHostInfraJobs' \
  "tarball updater must replace flannel/discoverd jobs after image pull and before controller deploys"
need "${ROOT}/updater/updater.go" 'skipping controller deploy of host-infra app' \
  "controller must not all-at-once discoverd/flannel (exclusive host ports); ReplaceHostInfraJobs rolls them"
need "${ROOT}/host/cli/github_updater.go" 'skipping controller deploy of host-infra app' \
  "controller must not all-at-once discoverd/flannel (exclusive host ports); ReplaceHostInfraJobs rolls them"
if grep -F 'func reexecUpdateBinary' -A 30 "${ROOT}/host/cli/github_updater.go" | grep -Fq 'current == newPath'; then
  echo "same-version tarball overwrite must re-exec; path equality skips ReplaceHostInfraJobs" >&2
  exit 1
fi
need "${ROOT}/host/cli/github_updater.go" 'FLYNN_UPDATE_REEXEC is the only gate' \
  "tarball update must re-exec after overwriting flynn-host.vYYYYMMDD.N in place"
if grep -Fq 'skipping deploy of infrastructure app' "${ROOT}/updater/updater.go" "${ROOT}/host/cli/github_updater.go"; then
  echo "old skip without ReplaceHostInfraJobs leaves bootstrap Mountspecs forever" >&2
  exit 1
fi
need "${ROOT}/appliance/postgresql/process.go" 'waitForSyncResetDeadline' \
  "postgres waitForSync must not reset the catch-up timer when pg_stat_replication is empty"
need "${ROOT}/appliance/postgresql/process.go" 'enablePrimaryWrites' \
  "postgres waitForSync timeout must enable writes so a recycled-WAL replica cannot stall the primary"
need "${ROOT}/appliance/postgresql/process.go" 'Keep watching' \
  "waitForSync timeout must keep watching so a late-streaming replica can still become SyncedDownstream"
need "${ROOT}/appliance/postgresql/process.go" 'postgresWALSegmentRemoved' \
  "standby must detect FATAL requested WAL segment has already been removed"
need "${ROOT}/appliance/postgresql/process.go" 'reseedAfterWALRecycle' \
  "unexpected standby exit after WAL recycle must reseed instead of crash-looping"
need "${ROOT}/appliance/postgresql/process.go" 'standbyReplicationStableFor' \
  "two streaming samples must not skip reseed before the walreceiver can FATAL"
need "${ROOT}/appliance/postgresql/process.go" 'reusedHang := p.reusedStandbyStartup()' \
  "reused standby hung in crash recovery must reseed, not wait 5m for connections"
need "${ROOT}/appliance/postgresql/process.go" 'standbyReuseStartupTimeout' \
  "reused standby start must abort well under opTimeout when WAL is gone"
need "${ROOT}/appliance/postgresql/process.go" 'startupTimeoutNeedsReseed' \
  "reused standby startup timeout must reseed instead of retrying the hung datadir"
need "${ROOT}/appliance/postgresql/process.go" 'postgres start canceled by stop' \
  "postgres start must release /stop while a reused standby is still starting up"
need "${ROOT}/appliance/postgresql/process.go" 'pg_basebackup and initdb run while p.mtx is held' \
  "pg_basebackup must die when /stop arrives so a deploy is not stuck behind the process lock"
need "${ROOT}/pkg/sirenia/client/client.go" 'timeout awaiting response headers' \
  "sirenia /stop must treat the 10s client timeout as recoverable"
need "${ROOT}/appliance/postgresql/process.go" 'standbyWALRecycleShouldReseed' \
  "a standby that stays up after recycled WAL must reseed, not skip because it is running"
need "${ROOT}/appliance/postgresql/process_health_test.go" 'TestStartupTimeoutNeedsReseed' \
  "postgres must test the reused-standby startup hang that left the cluster unhealthy after node reload"
need "${ROOT}/appliance/postgresql/process.go" 'wal_keep_size = 4096MB' \
  "primary must keep enough WAL for a node to come back after rolling reload"
need "${ROOT}/appliance/postgresql/process_health_test.go" 'TestPostgresWALSegmentRemoved' \
  "postgres must test the WAL-segment-removed FATAL that killed postgres.5052"
need "${ROOT}/appliance/postgresql/process_health_test.go" 'TestWaitForSyncResetDeadline' \
  "postgres must test missing replication status does not reset waitForSync"
need "${ROOT}/appliance/postgresql/process.go" 'waitForSyncAppearanceTimeout' \
  "sync waitForSync must wait through pg_basebackup before the 1m stall timer"
need "${smoke}" 'VTEP MAC' "smoke script must diagnose device vs lease VTEP MAC"
need "${smoke}" 'extra_args\+=\(--clean\)' "smoke reinstall on existing VMs must pass --clean"
need "${smoke}" 'for link in flannel.1 flynnbr0' "smoke --clean must delete stale overlay devices"
need "${smoke}" 'cli_arch=arm64' "smoke must install flynn-linux-arm64 on aarch64 nodes"
need "${smoke}" 'ensure-qemu-binfmt.sh' \
  "smoke must register qemu-user-static binfmt on aarch64 nodes (x86_64 buildpacks)"
need "${smoke}" 'flynn version' "smoke must execute the CLI (wrong-arch binaries exist but fail with Exec format error)"
if grep -q 'cli_arch=386' "${smoke}"; then
  echo "smoke must not install 32-bit CLI binaries" >&2
  exit 1
fi
if ! grep -F 'ssh -F "${cfg}"' "${smoke}" >/dev/null; then
  echo "smoke must ssh via cached ssh-config (not vagrant ssh)" >&2
  exit 1
fi
need "${smoke}" 'postgres_is_read_write' "smoke must verify postgres on a cluster node (overlay :5433 is not reachable from the host)"
if ! grep -Fq 'http://${NODE1_IP}/' "${smoke}"; then
  echo "smoke HTTP/app probes must use host-only node IPs, not localhost NAT forwards" >&2
  exit 1
fi
if grep -qE '9079|9442' "${smoke}"; then
  echo "smoke must not use Vagrant NAT-forwarded HTTP/HTTPS host ports" >&2
  exit 1
fi

build="${ROOT}/build.sh"
need "${build}" 'default_gomemlimit' \
  "build.sh must set GOMEMLIMIT so concurrent mksquashfs does not OOM the builder"
need "${build}" '12GiB' \
  "build.sh must default GOMEMLIMIT to 12GiB locally"
need "${build}" '4GiB' \
  "build.sh must default GOMEMLIMIT to 4GiB on GitHub Actions"
need "${build}" 'MemTotal' \
  "build.sh must cap GOMEMLIMIT to guest RAM so a 12GB laptop-loop VM does not OOM"
need "${ROOT}/script/flynn-builder" 'GOMEMLIMIT' \
  "script/flynn-builder must set GOMEMLIMIT; a direct invoke used to OOM-kill the process"
need "${build}" 'GITHUB_ACTIONS' \
  "build.sh must detect GitHub Actions for lower memory/concurrency defaults"
need "${build}" '^exit 0$' \
  "build.sh must exit 0 after success (a later phase_start not-found used to fail a completed build)"
need "${ROOT}/builder/build.go" 'Location:  "/mnt"' \
  "layer jobs must bind-mount the host temp dir at /mnt so mksquashfs writes local disk"
if grep -qE 'Device:[[:space:]]+"9p"' "${ROOT}/builder/build.go"; then
  echo "layer jobs must not squash over 9p (host image mksquashfs stalls on netfs_begin_write)" >&2
  exit 1
fi
if [[ -f "${ROOT}/builder/fileserver.go" ]]; then
  echo "builder/fileserver.go served /mnt over 9p; layer out must be a host bind, not 9p" >&2
  exit 1
fi

echo "ok overlay/install/smoke cluster-setup regressions"
