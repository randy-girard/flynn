#!/bin/bash
# Regression: plugin install must register flynn-host webhooks from the
# manifest (same API as `flynn-host webhooks add`) without special-casing a
# plugin name. Smoke must confirm the registration after install.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
install="${ROOT}/pkg/plugin/install.go"
manifest="${ROOT}/pkg/plugin/manifest.go"
docs="${ROOT}/docs/content/plugins.md"
host_webhook="${ROOT}/host/webhook.go"
helper="${ROOT}/pkg/httphelper/discoverd.go"

need_file() {
  local path=$1 msg=$2
  if [[ ! -f "${path}" ]]; then
    echo "${msg}" >&2
    echo "  missing ${path}" >&2
    exit 1
  fi
}

need_in() {
  local file=$1 needle=$2 msg=$3
  if ! grep -qE "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need_file "${install}" "plugin installer must exist"
need_file "${manifest}" "plugin manifest must exist"
need_file "${helper}" "discoverd HTTP resolve helper must exist so host unit tests cover webhook DNS"

need_in "${manifest}" 'json:"webhooks,omitempty"' \
  "flynn-plugin.json must have a generic webhooks field"
need_in "${manifest}" 'SecretEnv' \
  "webhook spec must support secret_env for X-Flynn-Webhook-Secret"
need_in "${install}" 'ensureWebhooks' \
  "plugin install must register host webhooks after deploy"
need_in "${install}" 'X-Flynn-Webhook-Secret' \
  "plugin install must send the same secret header as flynn-host webhooks add -H"
need_in "${install}" 'cluster.NewClient\(\).Hosts' \
  "plugin webhooks must use the flynn-host cluster API, not a plugin-name switch"
need_in "${smoke}" 'probe_plugin_webhooks' \
  "smoke must assert declared webhooks are registered on flynn-host"
need_in "${smoke}" 'flynn-host webhooks' \
  "smoke must list flynn-host webhooks after plugin install"
need_in "${docs}" 'webhooks' \
  "plugin docs must describe the webhooks manifest field"
need_in "${host_webhook}" 'ResolveDiscoverdAddr' \
  "host webhook dispatcher must resolve *.discoverd (systemd-resolved does not)"
need_in "${helper}" 'ResolveDiscoverdAddr' \
  "discoverd addr resolve must live in a Darwin-safe package covered by the host unit gate"

if grep -nE 'Name[[:space:]]*==[[:space:]]*"dashboard"|name[[:space:]]*==[[:space:]]*"dashboard"' "${install}" "${manifest}"; then
  echo "plugin install must not special-case dashboard by name for webhooks" >&2
  exit 1
fi

echo "ok plugin install registers generic flynn-host webhooks"
