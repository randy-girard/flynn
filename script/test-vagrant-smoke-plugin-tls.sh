#!/bin/bash
# Regression: web plugins enable Let's Encrypt through the generic plugin
# contract (routes.auto_tls and flynn-host plugin install --auto-tls), not a
# plugin-name switch. Smoke must not pass --auto-tls (Vagrant has no ACME).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
smoke="${ROOT}/script/vagrant-upgrade-smoke.sh"
install="${ROOT}/pkg/plugin/install.go"
manifest="${ROOT}/pkg/plugin/manifest.go"
cli="${ROOT}/host/cli/plugin.go"
docs="${ROOT}/docs/content/plugins.md"

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
  if ! grep -qE -- "${needle}" "${file}"; then
    echo "${msg}" >&2
    echo "  missing /${needle}/ in ${file}" >&2
    exit 1
  fi
}

need_file "${install}" "plugin installer must exist"
need_file "${manifest}" "plugin manifest must exist"
need_file "${cli}" "flynn-host plugin CLI must exist"

need_in "${manifest}" 'json:"auto_tls,omitempty"' \
  "flynn-plugin.json HTTP routes must be able to request Let's Encrypt"
need_in "${install}" 'ManagedCertificateDomain' \
  "plugin install must attach ACME the same way as flynn route add http --auto-tls"
need_in "${install}" 'skipping auto TLS' \
  "declared auto_tls must warn and continue when cluster ACME is off"
need_in "${install}" 'AutoTLS' \
  "plugin install must honor flynn-host plugin install --auto-tls"
need_in "${install}" 'spec.AutoTLS \|\| installAutoTLS \|\| acmeOn' \
  "plugin install must attach TLS on HTTP routes when cluster ACME is already enabled"
need_in "${cli}" '--auto-tls' \
  "flynn-host plugin install must expose --auto-tls for web plugins"
need_in "${cli}" 'plugin <plugin> route add http' \
  "flynn-host plugin must expose flynn-route-shaped route commands for installed plugins"
need_in "${cli}" 'plugin <plugin> route update' \
  "flynn-host plugin route update must exist so operators can enable TLS after install"
need_in "${docs}" 'auto_tls' \
  "plugin docs must describe route auto_tls and --auto-tls"
need_in "${docs}" 'flynn-host plugin dashboard route add http' \
  "plugin docs must show flynn-host plugin <name> route (kind: app is not a user flynn command)"

if grep -qE -- 'plugin install.*--auto-tls' "${smoke}"; then
  echo "smoke must not pass --auto-tls (Vagrant clusters do not run Let's Encrypt)" >&2
  exit 1
fi

if grep -nE 'Name[[:space:]]*==[[:space:]]*"dashboard"|name[[:space:]]*==[[:space:]]*"dashboard"' "${install}" "${manifest}" "${ROOT}/pkg/plugin/route.go" "${ROOT}/host/cli/plugin_route.go"; then
  echo "plugin route/TLS must not special-case dashboard by name" >&2
  exit 1
fi

echo "ok plugin install can enable Let's Encrypt on HTTP routes"
