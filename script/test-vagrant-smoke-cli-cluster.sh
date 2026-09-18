#!/bin/bash
# Regression: after --clean + re-bootstrap the CLI must replace a leftover
# cluster entry (new TLS pin). Skipping add when "default" already exists
# produced: pinned: the peer leaf certificate did not match the provided pin
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

force_cluster_add_cmd() {
  local cmd=$1
  case "${cmd}" in
    *' cluster:add -f '*|*' cluster:add --force '*|*' cluster add -f '*|*' cluster add --force '*) echo "${cmd}" ;;
    *' cluster:add '*) echo "${cmd/flynn cluster:add /flynn cluster:add -f }" ;;
    *) echo "${cmd/flynn cluster add /flynn cluster add -f }" ;;
  esac
}

got="$(force_cluster_add_cmd 'flynn cluster:add -p PIN default example.com KEY')"
want='flynn cluster:add -f -p PIN default example.com KEY'
if [[ "${got}" != "${want}" ]]; then
  echo "force_cluster_add_cmd missing -f: got ${got}" >&2
  exit 1
fi

already="$(force_cluster_add_cmd 'flynn cluster:add -f -p PIN default example.com KEY')"
if [[ "${already}" != 'flynn cluster:add -f -p PIN default example.com KEY' ]]; then
  echo "force_cluster_add_cmd must not duplicate -f: got ${already}" >&2
  exit 1
fi

long="$(force_cluster_add_cmd 'flynn cluster:add --force -p PIN default example.com KEY')"
if [[ "${long}" != 'flynn cluster:add --force -p PIN default example.com KEY' ]]; then
  echo "force_cluster_add_cmd must leave --force alone: got ${long}" >&2
  exit 1
fi

legacy="$(force_cluster_add_cmd 'flynn cluster add -p PIN default example.com KEY')"
if [[ "${legacy}" != 'flynn cluster add -f -p PIN default example.com KEY' ]]; then
  echo "force_cluster_add_cmd must still force the aliased cluster add form: got ${legacy}" >&2
  exit 1
fi

smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
if grep -n 'already registered' "${smoke}"; then
  echo "smoke script must not skip cluster add when 'default' exists (stale TLS pin)" >&2
  exit 1
fi
if ! grep -q 'force_cluster_add_cmd' "${smoke}"; then
  echo "smoke script must force-add the cluster (flynn cluster add -f) after bootstrap" >&2
  exit 1
fi
if ! grep -q 'flynn apps' "${smoke}"; then
  echo "smoke script must probe the controller after cluster add to catch a stale pin" >&2
  exit 1
fi

echo "ok CLI cluster force-add + stale-pin probe"
