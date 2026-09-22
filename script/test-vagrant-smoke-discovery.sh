#!/bin/bash
# Regression: smoke can boot a singleton, install the in-cluster discovery
# plugin, switch node1 to that token, and join node2+node3 with
# flynn-host init --discovery (not peer-ips).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
vagrant="${ROOT}/Vagrantfile"
docs="${ROOT}/docs/content/development.html.md"

need() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE -- "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need "${smoke}" 'step_discovery_join_nodes' \
  "smoke must join extra VMs using the in-cluster discovery plugin"
need "${smoke}" 'flynn-host init --discovery' \
  "joining via discovery must use flynn-host init --discovery, not only peer-ips"
need "${smoke}" 'Authorization: Bearer' \
  "discovery instance list/register requires the cluster id as Authorization: Bearer"
need "${smoke}" 'seed_joining_host_secrets' \
  "discovery joiners must copy DISCOVERD_AUTH_KEY from node1 before flynn-host starts"
need "${smoke}" 'host_json_set_discovery' \
  "node1 host.json must gain --discovery without restarting the singleton daemon"
need "${smoke}" 'read_discovery_join_token' \
  "smoke must read the token hooks.ready wrote (or GET /.well-known/cluster)"
need "${smoke}" 'smoke_http_discovery_token' \
  "Vagrant joiners must use http://discovery.CLUSTER_DOMAIN (no ACME)"
need "${smoke}" 'discovery_route_up' \
  "smoke must wait for the discovery HTTP route on node1 before joining"
need "${smoke}" 'step_verify_discovery_join' \
  "after join, smoke must require 3 flynn-host peers and 3 discovery instances"
need "${smoke}" 'discovery_instances_ready' \
  "discovery instance GET must wait until postgres is reachable (500 until then)"
need "${smoke}" 'postgres read-write after discovery join' \
  "discovery verify must wait for postgres before GET /instances"
need "${smoke}" 'TOPOLOGY_ACTION=discovery' \
  "discovery topology must boot 1-node then join node2 and node3"
need "${smoke}" 'TOPOLOGY_LABEL="1-node-discovery"' \
  "discovery report phases must be 1-node-discovery/"
need "${smoke}" 'remember_teardown_node node2' \
  "discovery topology must record node2 for teardown in the parent shell"
need "${smoke}" 'remember_teardown_node node3' \
  "discovery topology must record node3 for teardown in the parent shell"
need "${smoke}" 'resume/plugins already installed' \
  "backup/restore resume and SKIP_PLUGIN_INSTALL must not re-join node2/node3"
need "${smoke}" 'wait_sirenia_ha' \
  "discovery join must wait for postgres HA after growing 1→3 hosts"
need "${smoke}" 'still singleton in discoverd meta' \
  "sirenia HA wait must reject frozen singleton state with unassigned extras"
need "${smoke}" 'after discovery join' \
  "smoke must wait for postgres HA immediately after discovery join"
need "${smoke}" 'wait_sirenia_ha_if_cluster "after restore"' \
  "discovery --from-backup must wait for sirenia HA after restore"
need "${smoke}" 'append_live_node node2' \
  "parent shell must keep node2 in NODES after discovery join"
need "${smoke}" 'append_live_node node3' \
  "parent shell must keep node3 in NODES after discovery join"
need "${smoke}" 'PLUGIN_SMOKE_APPS_REQUESTED' \
  "discovery must append the discovery plugin without leaking into later topologies"
need "${smoke}" 'discovery\.\$\{CLUSTER_DOMAIN\}' \
  "/etc/hosts must resolve discovery.CLUSTER_DOMAIN for joiners"
need "${smoke}" 'sync_cluster_monitor_hosts' \
  "discovery join must update cluster-monitor hosts before later upgrades"
need "${vagrant}" 'flynn-plugin.json' \
  "Vagrantfile must mount every sibling with flynn-plugin.json, not only flynn-plugin-*"
need "${docs}" 'SMOKE_TOPOLOGIES=1,3,5,add,remove,discovery' \
  "development docs must list the discovery topology next to add/remove"
need "${docs}" 'flynn-plugin-discovery' \
  "development docs must list the discovery sibling as flynn-plugin-discovery"
need "${ROOT}/../flynn-plugin-discovery/script/ready.sh" '127.0.0.1:1111' \
  "discovery ready hook must resolve discovery.discoverd via discoverd HTTP, not host DNS"
need "${ROOT}/../flynn-plugin-discovery/script/ready.sh" 'DISCOVERD_AUTH_KEY' \
  "discovery ready hook must send DISCOVERD_AUTH_KEY when resolving *.discoverd (SEC-003)"
need "${ROOT}/../flynn-plugin-discovery/script/ready.sh" '--resolve' \
  "discovery ready hook must curl --resolve the overlay addr like plugin wait probes"
need "${ROOT}/docs/content/plugins.md" '../flynn-plugin-discovery' \
  "plugins docs must install discovery from ../flynn-plugin-discovery"
if grep -qE '\.\./flynn-discovery([[:space:]|]|$)' "${ROOT}/docs/content/plugins.md" "${docs}"; then
  echo "docs must not keep the old ../flynn-discovery checkout path" >&2
  exit 1
fi

if grep -qE 'Name[[:space:]]*==[[:space:]]*"discovery"|name[[:space:]]*==[[:space:]]*"discovery"' \
  "${ROOT}/pkg/plugin/install.go" "${ROOT}/pkg/plugin/manifest.go"; then
  echo "Flynn core must not special-case discovery by name" >&2
  exit 1
fi

if grep -q 'discovery grows a singleton' "${smoke}"; then
  echo "discovery topology must run --from-backup, not skip it" >&2
  exit 1
fi

if grep -v '^#' "${smoke}" | grep -qE -- '--discovery .*peer-ips'; then
  echo "discovery joiners must not mix --discovery with --peer-ips on extra nodes" >&2
  exit 1
fi

smoke_http_discovery_token() {
  local token=$1
  token="${token#"${token%%[![:space:]]*}"}"
  token="${token%"${token##*[![:space:]]}"}"
  token="${token/#https:/http:}"
  printf '%s' "${token}"
}

got="$(smoke_http_discovery_token 'https://discovery.example.com/clusters/abc')"
if [[ "${got}" != "http://discovery.example.com/clusters/abc" ]]; then
  echo "https join tokens must be rewritten to http for Vagrant, got '${got}'" >&2
  exit 1
fi
got="$(smoke_http_discovery_token '  http://discovery.example.com/clusters/abc  ')"
if [[ "${got}" != "http://discovery.example.com/clusters/abc" ]]; then
  echo "http tokens must be trimmed, got '${got}'" >&2
  exit 1
fi

# plugin_checkout discovery uses flynn-plugin-discovery; the sibling scan
# still finds alias mismatches (mysql→mariadb).
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
mkdir -p "${tmp}/flynn-plugin-discovery" "${tmp}/flynn-plugin-redis" "${tmp}/odd-mysql-checkout"
printf '%s\n' '{"name":"discovery","kind":"app"}' > "${tmp}/flynn-plugin-discovery/flynn-plugin.json"
printf '%s\n' '{"name":"redis","kind":"resource-provider"}' > "${tmp}/flynn-plugin-redis/flynn-plugin.json"
printf '%s\n' '{"name":"mysql","kind":"resource-provider"}' > "${tmp}/odd-mysql-checkout/flynn-plugin.json"
plugin_manifest_matches() {
  local json=$1 want=$2
  python3 - "$json" "$want" <<'PY'
import json, sys
path, want = sys.argv[1], sys.argv[2]
m = json.load(open(path))
raise SystemExit(0 if m.get("name") == want else 1)
PY
}
plugin_checkout() {
  local name=$1
  local root="${PLUGIN_REPO_ROOT}"
  local direct="${root}/flynn-plugin-${name}"
  if [[ -d "${direct}" && -f "${direct}/flynn-plugin.json" ]]; then
    echo "${direct}"
    return 0
  fi
  local dir json
  for dir in "${root}"/*; do
    [[ -d "${dir}" ]] || continue
    json="${dir}/flynn-plugin.json"
    [[ -f "${json}" ]] || continue
    if plugin_manifest_matches "${json}" "${name}"; then
      echo "${dir}"
      return 0
    fi
  done
  echo "${direct}"
}
PLUGIN_REPO_ROOT="${tmp}"
got="$(plugin_checkout discovery)"
if [[ "${got}" != "${tmp}/flynn-plugin-discovery" ]]; then
  echo "plugin_checkout discovery must resolve flynn-plugin-discovery, got '${got}'" >&2
  exit 1
fi
got="$(plugin_checkout redis)"
if [[ "${got}" != "${tmp}/flynn-plugin-redis" ]]; then
  echo "plugin_checkout redis must still resolve flynn-plugin-redis, got '${got}'" >&2
  exit 1
fi
got="$(plugin_checkout mysql)"
if [[ "${got}" != "${tmp}/odd-mysql-checkout" ]]; then
  echo "plugin_checkout mysql must still scan sibling flynn-plugin.json, got '${got}'" >&2
  exit 1
fi

echo "ok smoke discovery topology joins node2+node3 via the in-cluster plugin"
