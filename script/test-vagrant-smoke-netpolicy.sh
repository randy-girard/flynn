#!/bin/bash
# Regression: user overlay jobs must not mesh with other user jobs or
# internal discoverd names. Datastore access is leader.<service>.discoverd only.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
policy="${ROOT}/pkg/netpolicy/policy.go"
ipt="${ROOT}/pkg/iptables/iptables.go"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need "${policy}" 'UserMayResolveDiscoverd' \
  "netpolicy must gate user discoverd DNS to leader names"
need "${policy}" 'ServiceUser' \
  "netpolicy must publish user overlay IPs on a discoverd service"
need "${policy}" 'ClassDatastore' \
  "postgres/mysql/mongo/redis/kafka/clickhouse data-plane jobs must be classified"
need "${ipt}" 'EnableJobIsolation' \
  "flynn-host must install user overlay DROP rules"
need "${ipt}" 'conntrack", "--ctstate", "NEW' \
  "user overlay DROP must be NEW-only so router replies to user jobs are not dropped"
need "${ipt}" 'UserToDatastoreArgs' \
  "user jobs must be allowed to reach datastore IPs"
need "${ROOT}/pkg/iptables/ipset.go" 'EnsureSets' \
  "isolation uses ipsets synced across hosts"
need "${ROOT}/host/netpolicy.go" 'ServiceForClass' \
  "flynn-host must register user overlay IPs"
need "${ROOT}/host/netpolicy.go" 'EventKindCurrent' \
  "ipset watch must not flush on every EventUp (drops local datastore IPs)"
need "${ROOT}/pkg/iptables/ipset.go" 'UnionIPs' \
  "ipset Current sync must union local overlay IPs with the discoverd snapshot"
need "${ROOT}/host/libcontainer_backend.go" 'EnableJobIsolation' \
  "ConfigureNetworking must enable job isolation"
need "${ROOT}/host/libcontainer_backend.go" 'error enabling job isolation' \
  "ConfigureNetworking must fail closed if EnableJobIsolation cannot start"
need "${ROOT}/pkg/iptables/iptables.go" 'if err := EnsureSets' \
  "EnableJobIsolation must create ipsets before installing overlay rules"
need "${ROOT}/host/libcontainer_backend.go" 'enableBridgeNetfilter' \
  "same-host L2 isolation requires br_netfilter"
need "${ROOT}/host/img/packages.sh" 'ipset' \
  "host image must ship the ipset tool"
need "${ROOT}/script/install-flynn.tmpl" 'ipset' \
  "cluster nodes must install ipset (flynn-host EnableJobIsolation runs on the VM)"
need "${ROOT}/script/install-flynn" 'ipset' \
  "checked-in install-flynn must install ipset on cluster nodes"
need "${ROOT}/script/install-flynn-release" 'ipset' \
  "release tarball install-flynn must install ipset on cluster nodes"
need "${ROOT}/setup.sh" 'ipset' \
  "builder VM must install ipset so flynn-host can isolate during image builds"
need "${ROOT}/build.sh" 'ensure_builder_isolation_tools' \
  "build.sh must install ipset before start-all (existing builders skip setup.sh)"
need "${ROOT}/script/start-all" 'error configuring network' \
  "start-all must fail if flynn-host ConfigureNetworking failed"
need "${ROOT}/discoverd/server/dns.go" 'clientIsUser' \
  "discoverd DNS must hide internal names from user jobs"
need "${smoke}" 'net-isolate-peer' \
  "smoke must try (and fail) user→user overlay discoverd"
need "${smoke}" 'still reachable' \
  "peer isolation must retry: after restore flynn-net-user can lag and DNS fail-open"
need "${smoke}" 'net-isolate-internal' \
  "smoke must try (and fail) postgres.discoverd from a user job"
need "${smoke}" 'leader.postgres.discoverd' \
  "smoke must still reach the provisioned postgres URL host"
need "${ROOT}/pkg/netpolicy/policy.go" 'applianceUUIDName' \
  "per-app kafka/clickhouse/redis appliances must be datastore names, not only redis-"
need "${ROOT}/cli/plugin_cmd_test.go" 'leader\.' \
  "flynn redis redis-cli must dial leader.<redis-app>.discoverd (user DNS)"
need "${ROOT}/controller/jobs.go" 'flynn-system-app' \
  "one-off flynn run on system apps must inherit flynn-system-app (blobstore DNS)"

if grep -qE 'flynn -a .* run -- curl .*blobstore.discoverd' "${smoke}"; then
  echo "blobstore health must not run from a user slug (user jobs cannot reach blobstore.discoverd)" >&2
  exit 1
fi

echo "ok user job network isolation (iptables + leader-only discoverd DNS)"
